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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ManifestsSpec defines the desired state of Manifests
type ManifestsSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	SecretRef   *string  `json:"secretRef,omitempty"`
	ServiceUUID string   `json:"serviceUUID,omitempty"`
	Manifests   []string `json:"manifests,omitempty"`
}

// ManifestsStatus defines the observed state of Manifests
type ManifestsStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Executed bool `json:"executed,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Manifests is the Schema for the manifests API
type Manifests struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManifestsSpec   `json:"spec,omitempty"`
	Status ManifestsStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ManifestsList contains a list of Manifests
type ManifestsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Manifests `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Manifests{}, &ManifestsList{})
}
