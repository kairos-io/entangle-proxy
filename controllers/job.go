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
	   pkill edgevpn
	   exit 0
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

func GenerateJob(manifests entangleproxyv1alpha1.Manifests, delete bool) *batchv1.Job {
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
						Image:           "bitnami/kubectl:1.24-debian-11",
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
	}
}
