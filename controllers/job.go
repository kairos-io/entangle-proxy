package controllers

import (
	"fmt"

	entangleproxyv1alpha1 "github.com/kairos-io/entangle-proxy/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	EntanglementNameLabel      = "entanglement.kairos.io/name"
	EntanglementServiceLabel   = "entanglement.kairos.io/service"
	EntanglementDirectionLabel = "entanglement.kairos.io/direction"
	EntanglementPortLabel      = "entanglement.kairos.io/target_port"
	EntanglementHostLabel      = "entanglement.kairos.io/host"
)

// runner is the script the Job container runs. It waits for the remote API
// to answer through the edgevpn sidecar, runs kubectl once, stops the
// sidecar so the pod can terminate, and exits.
//
// The kubectl status is captured before pkill runs, and is the status the
// script exits with. The controller has nothing else to judge the operation
// by: Status.Executed and the finalizer are both decided from
// Job.Status.Succeeded, so a script that exits 0 whatever kubectl did would
// report a rejected manifest as applied, and a failed delete as finalized.
const (
	runner = `
	function wait_for {
		echo "Waiting for $1"
		timeout=300
		n=0; until ((n >= timeout)); do eval "$1" && break; n=$((n + 1)); sleep 1; done; ((n < timeout))
	}

	wait_for "kubectl get pods"
	ret=$?
	if [ $ret == 0 ]; then
	   kubectl %s -f /manifests
	   ret=$?
	   pkill edgevpn
	   exit $ret
	else
	  pkill edgevpn
	  exit $ret
	fi
`
)

func GenerateSecret(manifests entangleproxyv1alpha1.Manifests) *corev1.Secret {
	data := map[string][]byte{}

	for i, m := range manifests.Spec.Manifests {
		data[fmt.Sprintf("%d-%s.yaml", i, manifests.Name)] = []byte(m)
	}

	return &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:            manifests.Name,
			Namespace:       manifests.Namespace,
			OwnerReferences: genOwner(manifests),
		},
		Data: data,
	}
}

func GenerateJob(manifests entangleproxyv1alpha1.Manifests, delete bool, kubectlImage string) (*batchv1.Job, error) {
	// The job pod carries the secret name as a label so that the entangle
	// webhook can find the secret and point EDGEVPNTOKEN at it. secretRef is
	// optional in the CRD, so a Manifests can reach here without one, and
	// dereferencing it then takes the whole manager down.
	if manifests.Spec.SecretRef == nil || *manifests.Spec.SecretRef == "" {
		return nil, fmt.Errorf("secretRef is required")
	}

	privileged := false
	serviceAccount := false
	root := int64(0)
	shareproc := true
	action := "apply"
	if delete {
		action = "delete"
	}
	return &batchv1.Job{
		ObjectMeta: v1.ObjectMeta{
			Name:            fmt.Sprintf("%s-%s", manifests.Name, action),
			Namespace:       manifests.Namespace,
			OwnerReferences: genOwner(manifests),
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{

				ObjectMeta: v1.ObjectMeta{
					Name:      manifests.Name,
					Namespace: manifests.Namespace,
					//	OwnerReferences: genOwner(manifests),
					Labels: map[string]string{
						EntanglementNameLabel:    *manifests.Spec.SecretRef,
						EntanglementPortLabel:    "8080",
						EntanglementServiceLabel: manifests.Spec.ServiceUUID,
					},
				},
				Spec: corev1.PodSpec{
					ShareProcessNamespace:        &shareproc,
					RestartPolicy:                corev1.RestartPolicyOnFailure,
					AutomountServiceAccountToken: &serviceAccount,
					Containers: []corev1.Container{{
						SecurityContext: &corev1.SecurityContext{
							RunAsUser:  &root,
							Privileged: &privileged,
							Capabilities: &corev1.Capabilities{Add: []corev1.Capability{
								"SYS_PTRACE",
							}},
						},
						Name:            "proxy",
						Image:           kubectlImage,
						ImagePullPolicy: corev1.PullAlways,
						Command:         []string{"/bin/bash", "-c", "--"},
						Args:            []string{fmt.Sprintf(runner, action)},
						VolumeMounts: []corev1.VolumeMount{
							{
								MountPath: "/manifests",
								ReadOnly:  true,
								Name:      "manifests",
							},
						},
					}},
					Volumes: []corev1.Volume{{Name: "manifests", VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: manifests.Name,
						},
					}}},
				},
			},
		},
	}, nil
}
