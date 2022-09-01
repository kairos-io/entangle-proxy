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

	entangleproxyv1alpha1 "github.com/c3os-io/entangle-proxy/api/v1alpha1"
	"github.com/go-logr/logr"
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

const manifestsFinalizer = "entangle-proxy.c3os-io.io/finalizer"

const (
	manifestNoFinalize = "entanglement-proxy.c3os-x.io/no-finalize"
)

// ManifestsReconciler reconciles a Manifests object
type ManifestsReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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

//+kubebuilder:rbac:groups=entangle-proxy.c3os-x.io,resources=manifests,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=entangle-proxy.c3os-x.io,resources=manifests/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=entangle-proxy.c3os-x.io,resources=manifests/finalizers,verbs=update
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
	desiredJob := GenerateJob(*manifest, false)

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

	// Check if corresponding secret/job already exists in the specified namespace
	found := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: desiredSecret.Name, Namespace: desiredSecret.Namespace}, found)
	// If not exists, then create it
	if err != nil && errors.IsNotFound(err) {
		reqLogger.Info("Creating a new Secret", "Secret.Namespace", desiredSecret.Namespace, "Secret.Name", desiredSecret.Name)
		err = r.Client.Create(ctx, desiredSecret)
		if err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Check if job is there
	j := &batchv1.Job{}
	err = r.Client.Get(ctx, types.NamespacedName{Name: desiredJob.Name, Namespace: desiredJob.Namespace}, j)
	if err != nil && errors.IsNotFound(err) {
		reqLogger.Info("Creating a new Job", "Job.Namespace", desiredJob.Namespace, "Job.Name", desiredJob.Name)
		err = r.Client.Create(ctx, desiredJob)
		if err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// update secret if necessary and re-queue
	eq := reflect.DeepEqual(desiredSecret.Data, found.Data)
	if !eq {
		currentSecret := found.DeepCopy()
		currentSecret.Data = desiredSecret.Data
		reqLogger.Info("Update Secret", "Secret.Namespace", desiredSecret.Namespace, "Secret.Name", desiredSecret.Name)

		err := r.Client.Update(ctx, currentSecret)
		if err != nil {
			return reconcile.Result{}, err
		}
		reqLogger.Info("Delete old job", "Job.Namespace", j.Namespace, "Job.Name", j.Name)
		// Delete old job, we will recreate a new version of it on the next cycle
		bgr := metav1.DeletePropagationBackground // Delete also pods
		err = r.Client.Delete(ctx, j, &client.DeleteOptions{PropagationPolicy: &bgr})
		if err != nil {
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
	desiredJob = GenerateJob(*m, true)

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
