/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	entangleproxyv1alpha1 "github.com/kairos-io/entangle-proxy/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

const manifestsFinalizer = "entangle-proxy.kairos-io.io/finalizer"

const (
	manifestNoFinalize = "entanglement-proxy.kairos.io/no-finalize"
)

// ManifestsReconciler reconciles a Manifests object
type ManifestsReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	KubectlImage string
}

func genOwner(ent entangleproxyv1alpha1.Manifests) []metav1.OwnerReference {
	return []metav1.OwnerReference{
		*metav1.NewControllerRef(&ent.ObjectMeta, schema.GroupVersionKind{
			Group:   entangleproxyv1alpha1.GroupVersion.Group,
			Version: entangleproxyv1alpha1.GroupVersion.Version,
			Kind:    "Manifests",
		}),
	}
}

//+kubebuilder:rbac:groups=entangle-proxy.kairos.io,resources=manifests,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=entangle-proxy.kairos.io,resources=manifests/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=entangle-proxy.kairos.io,resources=manifests/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=create;get;list;watch;update
//+kubebuilder:rbac:groups="batch",resources=jobs,verbs=create;get;list;watch;update;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Manifests object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.1/pkg/reconcile
func (r *ManifestsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLogger := log.FromContext(ctx)

	// Creates a deployment targeting a service
	// TODO(user): your logic here
	manifest := &entangleproxyv1alpha1.Manifests{}
	if err := r.Get(ctx, req.NamespacedName, manifest); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	desiredSecret := GenerateSecret(*manifest)
	desiredJob, err := GenerateJob(*manifest, false, r.KubectlImage)
	if err != nil {
		reqLogger.Error(err, "Invalid Manifests", "Manifests.Namespace", manifest.Namespace, "Manifests.Name", manifest.Name)
		return ctrl.Result{}, err
	}

	// Allow to bypass finalizers for Manifests (one-shots)
	_, noFinalize := manifest.Annotations[manifestNoFinalize]

	// Finalizer logic. It calls `kubectl delete -f ` on the resources
	isManifestsMarkedToBeDeleted := manifest.GetDeletionTimestamp() != nil
	if isManifestsMarkedToBeDeleted && !noFinalize {
		if controllerutil.ContainsFinalizer(manifest, manifestsFinalizer) {
			// Run finalization logic for memcachedFinalizer. If the
			// finalization logic fails, don't remove the finalizer so
			// that we can retry during the next reconciliation.
			if err := r.finalize(ctx, reqLogger, manifest, desiredJob); err != nil {
				return ctrl.Result{}, err
			}

			// Remove memcachedFinalizer. Once all finalizers have been
			// removed, the object will be deleted.
			controllerutil.RemoveFinalizer(manifest, manifestsFinalizer)
			err := r.Update(ctx, manifest)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer for this CR
	if !controllerutil.ContainsFinalizer(manifest, manifestsFinalizer) && !noFinalize {
		controllerutil.AddFinalizer(manifest, manifestsFinalizer)
		err := r.Update(ctx, manifest)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile the Secret first, and completely: the Job below mounts it, so a
	// Job must never be created or restarted while the Secret still holds the
	// previous manifests.
	found := &corev1.Secret{}
	err = r.Client.Get(ctx, types.NamespacedName{Name: desiredSecret.Name, Namespace: desiredSecret.Namespace}, found)
	secretWritten := false
	switch {
	case err != nil && errors.IsNotFound(err):
		reqLogger.Info("Creating a new Secret", "Secret.Namespace", desiredSecret.Namespace, "Secret.Name", desiredSecret.Name)
		if err := r.Client.Create(ctx, desiredSecret); err != nil {
			return reconcile.Result{}, err
		}
		secretWritten = true
	case err != nil:
		return reconcile.Result{}, err
	case !reflect.DeepEqual(desiredSecret.Data, found.Data):
		currentSecret := found.DeepCopy()
		currentSecret.Data = desiredSecret.Data
		reqLogger.Info("Update Secret", "Secret.Namespace", desiredSecret.Namespace, "Secret.Name", desiredSecret.Name)
		if err := r.Client.Update(ctx, currentSecret); err != nil {
			return reconcile.Result{}, err
		}
		secretWritten = true
	}

	// Reconcile the Job. A Get that returned NotFound leaves j zero-valued, so
	// it can only be read after the Get succeeded: the name of a Job that does
	// not exist yet is the empty string, and both the Update and the Delete
	// below used to be handed one.
	j := &batchv1.Job{}
	err = r.Client.Get(ctx, types.NamespacedName{Name: desiredJob.Name, Namespace: desiredJob.Namespace}, j)
	switch {
	case err != nil && errors.IsNotFound(err):
		// Nothing ran yet, so there is no stale Job to replace and no status to
		// report. The Secret above is already current.
		reqLogger.Info("Creating a new Job", "Job.Namespace", desiredJob.Namespace, "Job.Name", desiredJob.Name)
		if err := r.Client.Create(ctx, desiredJob); err != nil {
			return reconcile.Result{}, err
		}
		return ctrl.Result{}, nil
	case err != nil:
		return reconcile.Result{}, err
	}

	// A Job's pod template is immutable, so drift is corrected by deleting it
	// and letting the next cycle recreate it from the current spec.
	if secretWritten || jobOutOfDate(desiredJob, j) {
		reqLogger.Info("Delete old job", "Job.Namespace", j.Namespace, "Job.Name", j.Name)
		bgr := metav1.DeletePropagationBackground // Delete also pods
		if err := r.Client.Delete(ctx, j, &client.DeleteOptions{PropagationPolicy: &bgr}); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{Requeue: true}, nil
	}

	// Update status to reflect job result
	if j.Status.Succeeded == 1 {
		copy := manifest.DeepCopy()
		copy.Status.Executed = true
		err = r.Client.Status().Update(ctx, copy)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// jobOutOfDate reports whether a live Job still runs what the Manifests spec
// asks for.
//
// It deliberately does not compare the two JobSpecs: the API server defaults
// most of a JobSpec, and the Job controller adds its own `controller-uid` and
// `job-name` pod labels, so a DeepEqual against a freshly generated Job reports
// drift on every pass and the reconciler would delete and recreate the Job
// forever. Only the fields GenerateJob derives from the Manifests spec are
// compared: the entanglement pod labels, which select the proxy sidecar, and
// the container image and args, which carry the kubectl image and the action.
func jobOutOfDate(desired, live *batchv1.Job) bool {
	for k, v := range desired.Spec.Template.Labels {
		if live.Spec.Template.Labels[k] != v {
			return true
		}
	}

	desiredContainers := desired.Spec.Template.Spec.Containers
	liveContainers := live.Spec.Template.Spec.Containers
	if len(desiredContainers) != len(liveContainers) {
		return true
	}
	for i, c := range desiredContainers {
		if c.Image != liveContainers[i].Image ||
			!reflect.DeepEqual(c.Args, liveContainers[i].Args) {
			return true
		}
	}

	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManifestsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&entangleproxyv1alpha1.Manifests{}).
		Owns(&batchv1.Job{}).
		// Watches(&source.Kind{Type: &batchv1.Job{}}, &handler.EnqueueRequestForOwner{
		// 	IsController: true,
		// 	OwnerType:    &entangleproxyv1alpha1.Manifests{},
		// }).
		Complete(r)
}

func (r *ManifestsReconciler) finalize(ctx context.Context, reqLogger logr.Logger, m *entangleproxyv1alpha1.Manifests, desiredJob *batchv1.Job) error {
	// Check if an apply job is pending there and delete it
	j := &batchv1.Job{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: desiredJob.Name, Namespace: desiredJob.Namespace}, j)
	if err == nil {
		bgr := metav1.DeletePropagationBackground // Delete also pods
		err := r.Client.Delete(ctx, j, &client.DeleteOptions{PropagationPolicy: &bgr})
		if err != nil {
			return err
		}
	}

	// Generate delete job
	desiredJob, err = GenerateJob(*m, true, r.KubectlImage)
	if err != nil {
		return err
	}

	j = &batchv1.Job{}
	err = r.Client.Get(ctx, types.NamespacedName{Name: desiredJob.Name, Namespace: desiredJob.Namespace}, j)
	if err != nil && errors.IsNotFound(err) {
		reqLogger.Info("Creating a new Job", "Job.Namespace", desiredJob.Namespace, "Job.Name", desiredJob.Name)
		err = r.Client.Create(ctx, desiredJob)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if j.Status.Succeeded != 1 {
		return fmt.Errorf("not finalized yet")
	}

	reqLogger.Info("Successfully finalized ")

	return nil
}
