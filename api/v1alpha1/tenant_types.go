/*
Copyright 2026.

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

// TenantSpec defines the desired state of Tenant.
type TenantSpec struct {
	// displayName is a human-friendly name for the tenant.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// owners are the users or groups granted the "edit" role in the tenant
	// namespace. Use "user:" or "group:" prefixes, e.g. "user:alice@example.io".
	// +kubebuilder:validation:MinItems=1
	// +required
	Owners []string `json:"owners"`

	// resourceQuota sets the total resource limits for the tenant namespace.
	// +optional
	ResourceQuota TenantResourceQuota `json:"resourceQuota,omitempty"`

	// networkIsolation, when true, applies a default-deny NetworkPolicy plus an
	// allow-DNS rule so the tenant namespace is isolated by default.
	// +kubebuilder:default=true
	// +optional
	NetworkIsolation bool `json:"networkIsolation,omitempty"`
}

// TenantResourceQuota defines the resource limits applied to the tenant namespace.
type TenantResourceQuota struct {
	// cpu is the total CPU limit for the namespace.
	// +kubebuilder:default="4"
	// +optional
	CPU string `json:"cpu,omitempty"`

	// memory is the total memory limit for the namespace.
	// +kubebuilder:default="8Gi"
	// +optional
	Memory string `json:"memory,omitempty"`

	// pods is the maximum number of pods in the namespace.
	// +kubebuilder:default=20
	// +optional
	Pods int32 `json:"pods,omitempty"`
}

// TenantStatus defines the observed state of Tenant.
type TenantStatus struct {
	// namespace is the namespace provisioned for this tenant.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// observedGeneration is the spec generation the controller last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the Tenant resource.
	// "Ready" is True once the namespace and all policies are provisioned.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`

// Tenant is the Schema for the tenants API
type Tenant struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Tenant
	// +required
	Spec TenantSpec `json:"spec"`

	// status defines the observed state of Tenant
	// +optional
	Status TenantStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TenantList contains a list of Tenant
type TenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Tenant `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Tenant{}, &TenantList{})
}
