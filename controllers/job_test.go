package controllers

import (
	"testing"

	entangleproxyv1alpha1 "github.com/kairos-io/entangle-proxy/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func manifests(secretRef *string) entangleproxyv1alpha1.Manifests {
	return entangleproxyv1alpha1.Manifests{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: entangleproxyv1alpha1.ManifestsSpec{
			SecretRef:   secretRef,
			ServiceUUID: "svc",
			Manifests:   []string{"apiVersion: v1\nkind: ConfigMap\n"},
		},
	}
}

// secretRef is optional in the CRD, so GenerateJob has to report it missing.
// Dereferencing it took down the whole manager, because controller-runtime
// does not recover panics raised by Reconcile unless asked to.
func TestGenerateJobWithoutSecretRef(t *testing.T) {
	if _, err := GenerateJob(manifests(nil), false, "kubectl:latest"); err == nil {
		t.Fatal("GenerateJob accepted a Manifests with no secretRef")
	}
}

func TestGenerateJobWithEmptySecretRef(t *testing.T) {
	empty := ""
	if _, err := GenerateJob(manifests(&empty), false, "kubectl:latest"); err == nil {
		t.Fatal("GenerateJob accepted a Manifests with an empty secretRef")
	}
}

func TestGenerateJobLabelsTheSecret(t *testing.T) {
	ref := "mysecret"
	j, err := GenerateJob(manifests(&ref), false, "kubectl:latest")
	if err != nil {
		t.Fatalf("GenerateJob: %v", err)
	}
	if got := j.Spec.Template.Labels[EntanglementNameLabel]; got != ref {
		t.Fatalf("%s = %q, want %q", EntanglementNameLabel, got, ref)
	}
	if j.Name != "demo-apply" {
		t.Fatalf("job name = %q, want %q", j.Name, "demo-apply")
	}
}

func TestGenerateDeleteJobIsNamedApart(t *testing.T) {
	ref := "mysecret"
	j, err := GenerateJob(manifests(&ref), true, "kubectl:latest")
	if err != nil {
		t.Fatalf("GenerateJob: %v", err)
	}
	if j.Name != "demo-delete" {
		t.Fatalf("job name = %q, want %q", j.Name, "demo-delete")
	}
}
