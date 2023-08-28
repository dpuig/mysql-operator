/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	samplecontrollerv1alpha1 "github.com/dpuig/mysql-operator/api/v1alpha1"
)

var (
	deploymentOwnerKey = ".metadata.controller"
	apiGVStr           = samplecontrollerv1alpha1.GroupVersion.String()
)

// MySQLClusterReconciler reconciles a MySQLCluster object
type MySQLClusterReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=apps.dpuigerarde.com,resources=mysqlclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps.dpuigerarde.com,resources=mysqlclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=apps.dpuigerarde.com,resources=mysqlclusters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MySQLCluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.15.0/pkg/reconcile
func (r *MySQLClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	log := r.Log.WithValues("mysqlCluster", req.NamespacedName)

	var mysqlCluster samplecontrollerv1alpha1.MySQLCluster
	log.Info("fetching MySQLCluster Resource")
	if err := r.Get(ctx, req.NamespacedName, &mysqlCluster); err != nil {
		log.Error(err, "unable to fetch MySQLCluster")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.cleanupOwnedResources(ctx, log, &mysqlCluster); err != nil {
		log.Error(err, "failed to clean up old Deployment resources for this Foo")
		return ctrl.Result{}, err
	}

	// get deploymentName from mysqlCluster.Spec
	deploymentName := mysqlCluster.Spec.DeploymentName

	// define deployment template using deploymentName
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: req.Namespace,
		},
	}

	// Create or Update deployment object
	if _, err := ctrl.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		replicas := int32(1)
		if mysqlCluster.Spec.Replicas != nil {
			replicas = *mysqlCluster.Spec.Replicas
		}
		deploy.Spec.Replicas = &replicas

		labels := map[string]string{
			"app":        "mysql",
			"controller": req.Name,
		}

		// set labels to spec.selector for our deployment
		if deploy.Spec.Selector == nil {
			deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}

		// set labels to template.objectMeta for our deployment
		if deploy.Spec.Template.ObjectMeta.Labels == nil {
			deploy.Spec.Template.ObjectMeta.Labels = labels
		}

		// set a container for our deployment
		containers := []corev1.Container{
			{
				Name:  "db",
				Image: "mysql:" + mysqlCluster.Spec.Version,
				Env: []corev1.EnvVar{
					{
						Name:  "MYSQL_ROOT_PASSWORD",
						Value: mysqlCluster.Spec.Password,
					},
				},
				Command: []string{"mysqld", "--user=root"},
				Args:    []string{"--default-authentication-plugin=mysql_native_password"},
				Ports: []corev1.ContainerPort{
					{
						Name:          "mysql",
						ContainerPort: 3306,
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "mysql-persistent-storage",
						MountPath: "/var/lib/mysql",
					},
				},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser:  func() *int64 { i := int64(1001); return &i }(),
					RunAsGroup: func() *int64 { i := int64(1001); return &i }(),
				},
			},
		}

		// set containers to template.spec.containers for our deployment
		if deploy.Spec.Template.Spec.Containers == nil {
			deploy.Spec.Template.Spec.Containers = containers
		}

		deploy.Spec.Strategy.Type = "Recreate"
		deploy.Spec.Template.Spec.Volumes = []corev1.Volume{
			{
				Name: "mysql-persistent-storage",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "mysql-pv-claim",
					},
				},
			},
		}

		deploy.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			FSGroup: func() *int64 { i := int64(1001); return &i }(),
		}

		// set the owner so that garbage collection can kicks in
		if err := ctrl.SetControllerReference(&mysqlCluster, deploy, r.Scheme); err != nil {
			log.Error(err, "unable to set ownerReference from mysqlCluster to Deployment")
			return err
		}

		return nil
	}); err != nil {

		// error handling of ctrl.CreateOrUpdate
		log.Error(err, "unable to ensure deployment is correct")
		return ctrl.Result{}, err

	}

	// get deployment object from in-memory-cache
	var deployment appsv1.Deployment
	var deploymentNamespacedName = client.ObjectKey{Namespace: req.Namespace, Name: mysqlCluster.Spec.DeploymentName}
	if err := r.Get(ctx, deploymentNamespacedName, &deployment); err != nil {
		log.Error(err, "unable to fetch Deployment")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// set mysqlCluster.status.AvailableReplicas from deployment
	availableReplicas := deployment.Status.AvailableReplicas
	if availableReplicas == mysqlCluster.Status.AvailableReplicas {
		return ctrl.Result{}, nil
	}
	mysqlCluster.Status.AvailableReplicas = availableReplicas

	// update mysqlCluster.status
	if err := r.Status().Update(ctx, &mysqlCluster); err != nil {
		log.Error(err, "unable to update mysqlCluster status")
		return ctrl.Result{}, err
	}

	// create event for updated mysqlCluster.status
	r.Recorder.Eventf(&mysqlCluster, corev1.EventTypeNormal, "Updated", "Update mysqlCluster.status.AvailableReplicas: %d", mysqlCluster.Status.AvailableReplicas)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MySQLClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	// add deploymentOwnerKey index to deployment object which MySQLCluster resource owns
	if err := mgr.GetFieldIndexer().IndexField(ctx, &appsv1.Deployment{}, deploymentOwnerKey, func(rawObj client.Object) []string {
		// grab the deployment object, extract the owner...
		deployment := rawObj.(*appsv1.Deployment)
		owner := metav1.GetControllerOf(deployment)
		if owner == nil {
			return nil
		}
		// ...make sure it's a MySQLCluster...
		if owner.APIVersion != apiGVStr || owner.Kind != "MySQLCluster" {
			return nil
		}

		// ...and if so, return it
		return []string{owner.Name}
	}); err != nil {
		return err
	}

	// define to watch targets...Foo resource and owned Deployment
	return ctrl.NewControllerManagedBy(mgr).
		For(&samplecontrollerv1alpha1.MySQLCluster{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

// cleanupOwnedResources will delete any existing Deployment resources that
// were created for the given mysqlCluster that no longer match the
// mysqlCluster.spec.deploymentName field.
func (r *MySQLClusterReconciler) cleanupOwnedResources(ctx context.Context, log logr.Logger, mysqlCluster *samplecontrollerv1alpha1.MySQLCluster) error {
	log.Info("finding existing Deployments for Foo resource")

	// List all deployment resources owned by this mysqlCluster
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(mysqlCluster.Namespace), client.MatchingFields(map[string]string{deploymentOwnerKey: mysqlCluster.Name})); err != nil {
		return err
	}

	// Delete deployment if the deployment name doesn't match foo.spec.deploymentName
	for _, deployment := range deployments.Items {
		if deployment.Name == mysqlCluster.Spec.DeploymentName {
			// If this deployment's name matches the one on the Foo resource
			// then do not delete it.
			continue
		}

		// Delete old deployment object which doesn't match foo.spec.deploymentName
		if err := r.Delete(ctx, &deployment); err != nil {
			log.Error(err, "failed to delete Deployment resource")
			return err
		}

		log.Info("delete deployment resource: " + deployment.Name)
		r.Recorder.Eventf(mysqlCluster, corev1.EventTypeNormal, "Deleted", "Deleted deployment %q", deployment.Name)
	}

	return nil
}
