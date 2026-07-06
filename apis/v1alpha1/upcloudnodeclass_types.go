package v1alpha1

import (
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// UpCloudNodeClassSpec defines the desired state of UpCloudNodeClass.
type UpCloudNodeClassSpec struct {
	// Zone is the UpCloud data center zone (e.g. "de-fra1", "fi-hel2").
	Zone string `json:"zone"`
	// Plan is the UKS node group plan name (e.g. "2xCPU-4GB", "PREMIUM-1xCPU-2GB", "CLOUDNATIVE-2xCPU-16GB").
	Plan string `json:"plan"`
	// StorageGB is the disk size in gigabytes for each node.
	// +optional
	StorageGB int `json:"storageGB,omitempty"`
	// StorageTier is the storage tier: maxiops, standard, or hdd.
	// +optional
	StorageTier upcloud.StorageTier `json:"storageTier,omitempty"`
	// SSHKeys is a list of SSH public keys to inject into each node.
	// +optional
	SSHKeys []string `json:"sshKeys,omitempty"`
	// KubeletArgs are additional kubelet arguments for each node.
	// +optional
	KubeletArgs []upcloud.KubernetesKubeletArg `json:"kubeletArgs,omitempty"`
	// Labels are Kubernetes labels applied to each node in the group.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Taints are Kubernetes taints applied to each node in the group.
	// +optional
	Taints []upcloud.KubernetesTaint `json:"taints,omitempty"`
	// AntiAffinity enables/disables anti-affinity placement for nodes.
	// +optional
	AntiAffinity *bool `json:"antiAffinity,omitempty"`
	// UtilityNetworkAccess enables/disables utility network access for nodes.
	// +optional
	UtilityNetworkAccess *bool `json:"utilityNetworkAccess,omitempty"`
}

// UpCloudNodeClassStatus defines the observed state of UpCloudNodeClass.
type UpCloudNodeClassStatus struct {
	// Conditions represent the latest available observations of the object's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Hash is the resolved hash of the NodeClass spec, used by drift detection to
	// compare the live configuration against the one a NodeClaim was provisioned with.
	// +optional
	Hash string `json:"hash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// UpCloudNodeClass is the Schema for the upcloudnodeclasses API.
type UpCloudNodeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UpCloudNodeClassSpec   `json:"spec,omitempty"`
	Status            UpCloudNodeClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// UpCloudNodeClassList contains a list of UpCloudNodeClass.
type UpCloudNodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UpCloudNodeClass `json:"items"`
}
