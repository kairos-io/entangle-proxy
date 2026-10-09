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
	"testing"

	entangleproxyv1alpha1 "github.com/kairos-io/entangle-proxy/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// These run Reconcile itself against a fake client, so they do not need the
// envtest control plane the Ginkgo suite in suite_test.go brings up.

const (
	testName      = "foo"
	testNamespace = "default"
	testSecretRef = "myentanglement"
)

var testKey = types.NamespacedName{Name: testName, Namespace: testNamespace}

var testJobKey = types.NamespacedName{Name: testName + "-apply", Namespace: testNamespace}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := entangleproxyv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// testManifests is a Manifests that already carries the finalizer, so Reconcile
// goes straight to the Secret and Job it owns.
func testManifests(serviceUUID string, body string) *entangleproxyv1alpha1.Manifests {
	ref := testSecretRef
	return &entangleproxyv1alpha1.Manifests{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testName,
			Namespace:  testNamespace,
			Finalizers: []string{manifestsFinalizer},
		},
		Spec: entangleproxyv1alpha1.ManifestsSpec{
			SecretRef:   &ref,
			ServiceUUID: serviceUUID,
			Manifests:   []string{body},
		},
	}
}

// mustJob is the apply Job the reconciler would have created for m.
func mustJob(t *testing.T, m entangleproxyv1alpha1.Manifests, image string) *batchv1.Job {
	t.Helper()
	j, err := GenerateJob(m, false, image)
	if err != nil {
		t.Fatalf("GenerateJob: %v", err)
	}
	return j
}

func newTestReconciler(t *testing.T, objs ...client.Object) (*ManifestsReconciler, client.Client) {
	t.Helper()
	s := testScheme(t)
	// Manifests carries +kubebuilder:subresource:status, and since
	// controller-runtime v0.15 the fake client only serves a status
	// subresource for the types it was told about.
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&entangleproxyv1alpha1.Manifests{}).
		Build()
	return &ManifestsReconciler{Client: c, Scheme: s, KubectlImage: "quay.io/kairos/kubectl:latest"}, c
}

func reconcileOnce(t *testing.T, r *ManifestsReconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: testKey})
	if err != nil {
		t.Fatalf("Reconcile returned an error: %v", err)
	}
	return res
}

func getJob(t *testing.T, c client.Client) *batchv1.Job {
	t.Helper()
	j := &batchv1.Job{}
	if err := c.Get(context.Background(), testJobKey, j); err != nil {
		t.Fatalf("getting the apply Job: %v", err)
	}
	return j
}

func getSecret(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	if err := c.Get(context.Background(), testKey, s); err != nil {
		t.Fatalf("getting the manifests Secret: %v", err)
	}
	return s
}

// A Manifests that has never been reconciled must come up in one pass. Both
// the Secret and the Job are absent, so neither may be read back from the
// zero-valued object a NotFound Get leaves behind.
func TestReconcileCreatesSecretAndJob(t *testing.T) {
	r, c := newTestReconciler(t, testManifests("service-a", "kind: ConfigMap\n"))

	if res := reconcileOnce(t, r); res.Requeue {
		t.Errorf("the first pass asked for a requeue: %+v", res)
	}

	if got := string(getSecret(t, c).Data["0-"+testName+".yaml"]); got != "kind: ConfigMap\n" {
		t.Errorf("Secret carries %q", got)
	}
	if got := getJob(t, c).Spec.Template.Labels[EntanglementServiceLabel]; got != "service-a" {
		t.Errorf("Job targets service %q, want service-a", got)
	}
}

// The Secret is stale and the Job has been deleted out from under us. The
// Secret has to be brought up to date, and the replacement Job must be created
// after that, not before, or it mounts the previous manifests.
func TestReconcileUpdatesSecretWhenTheJobIsGone(t *testing.T) {
	r, c := newTestReconciler(t,
		testManifests("service-a", "kind: Service\n"),
		GenerateSecret(*testManifests("service-a", "kind: ConfigMap\n")),
	)

	reconcileOnce(t, r)

	if got := string(getSecret(t, c).Data["0-"+testName+".yaml"]); got != "kind: Service\n" {
		t.Errorf("Secret still carries %q", got)
	}
	if getJob(t, c) == nil {
		t.Error("no Job was created")
	}
}

// Editing the manifests replaces the Job, which is the behaviour the drift
// check has always had.
func TestReconcileReplacesTheJobWhenTheManifestsChange(t *testing.T) {
	live := mustJob(t, *testManifests("service-a", "kind: ConfigMap\n"), "quay.io/kairos/kubectl:latest")
	live.Status.Succeeded = 1
	r, c := newTestReconciler(t,
		testManifests("service-a", "kind: Service\n"),
		GenerateSecret(*testManifests("service-a", "kind: ConfigMap\n")),
		live,
	)

	if res := reconcileOnce(t, r); !res.Requeue {
		t.Error("replacing the Job did not ask for a requeue")
	}
	j := &batchv1.Job{}
	if err := c.Get(context.Background(), testJobKey, j); err == nil {
		t.Error("the stale Job is still there")
	}
}

// Repointing a Manifests at another entangled service changes nothing in the
// Secret, only in the Job's pod labels, which is how the proxy sidecar is
// selected. The Job has to be replaced for the change to reach the cluster.
func TestReconcileReplacesTheJobWhenTheServiceChanges(t *testing.T) {
	live := mustJob(t, *testManifests("service-a", "kind: ConfigMap\n"), "quay.io/kairos/kubectl:latest")
	live.Status.Succeeded = 1
	r, c := newTestReconciler(t,
		testManifests("service-b", "kind: ConfigMap\n"),
		GenerateSecret(*testManifests("service-b", "kind: ConfigMap\n")),
		live,
	)

	if res := reconcileOnce(t, r); !res.Requeue {
		t.Fatal("the repointed Manifests did not ask for a requeue")
	}
	// The next cycle recreates it from the current spec.
	reconcileOnce(t, r)

	if got := getJob(t, c).Spec.Template.Labels[EntanglementServiceLabel]; got != "service-b" {
		t.Errorf("Job still targets service %q, want service-b", got)
	}
}

// Upgrading the operator changes the kubectl image the Job runs.
func TestReconcileReplacesTheJobWhenTheKubectlImageChanges(t *testing.T) {
	live := mustJob(t, *testManifests("service-a", "kind: ConfigMap\n"), "quay.io/kairos/kubectl:old")
	live.Status.Succeeded = 1
	r, c := newTestReconciler(t,
		testManifests("service-a", "kind: ConfigMap\n"),
		GenerateSecret(*testManifests("service-a", "kind: ConfigMap\n")),
		live,
	)

	if res := reconcileOnce(t, r); !res.Requeue {
		t.Fatal("the new kubectl image did not ask for a requeue")
	}
	reconcileOnce(t, r)

	if got := getJob(t, c).Spec.Template.Spec.Containers[0].Image; got != "quay.io/kairos/kubectl:latest" {
		t.Errorf("Job still runs %q", got)
	}
}

// defaultedJob is what the API server and the Job controller hand back for a
// Job that GenerateJob created: a selector, the two generated pod labels, and
// the defaults for every field the manifest left empty. The drift check has to
// read this as "no drift", or the reconciler deletes and recreates the Job on
// every pass forever.
func defaultedJob(t *testing.T, m entangleproxyv1alpha1.Manifests, image string) *batchv1.Job {
	t.Helper()
	j := mustJob(t, m, image)
	one := int32(1)
	six := int32(6)
	grace := int64(30)
	mode := int32(420)
	uid := "11111111-2222-3333-4444-555555555555"

	j.Spec.Completions = &one
	j.Spec.Parallelism = &one
	j.Spec.BackoffLimit = &six
	j.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"controller-uid": uid}}
	j.Spec.Template.Labels["controller-uid"] = uid
	j.Spec.Template.Labels["job-name"] = j.Name

	j.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirst
	j.Spec.Template.Spec.SchedulerName = "default-scheduler"
	j.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{}
	j.Spec.Template.Spec.TerminationGracePeriodSeconds = &grace
	j.Spec.Template.Spec.Volumes[0].Secret.DefaultMode = &mode

	c := &j.Spec.Template.Spec.Containers[0]
	c.TerminationMessagePath = corev1.TerminationMessagePathDefault
	c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
	c.Ports = []corev1.ContainerPort{}
	c.Resources = corev1.ResourceRequirements{}

	j.Status.Succeeded = 1
	return j
}

func TestReconcileDoesNotLoopOnADefaultedJob(t *testing.T) {
	m := testManifests("service-a", "kind: ConfigMap\n")
	r, c := newTestReconciler(t, m, GenerateSecret(*m), defaultedJob(t, *m, "quay.io/kairos/kubectl:latest"))

	for i := 1; i <= 3; i++ {
		if res := reconcileOnce(t, r); res.Requeue {
			t.Fatalf("pass %d asked for a requeue, so the Job was replaced", i)
		}
		if err := c.Get(context.Background(), testJobKey, &batchv1.Job{}); err != nil {
			t.Fatalf("pass %d deleted the Job: %v", i, err)
		}
	}

	cr := &entangleproxyv1alpha1.Manifests{}
	if err := c.Get(context.Background(), testKey, cr); err != nil {
		t.Fatal(err)
	}
	if !cr.Status.Executed {
		t.Error("the succeeded Job was never reported in status.executed")
	}
}
