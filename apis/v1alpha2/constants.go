package v1alpha2

import (
	corev1 "k8s.io/api/core/v1"
	karpentercloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const (
	// ProviderIDPrefix is the prefix for NodeClaim status provider IDs.
	// The full format is "upcloud:////<server-uuid>".
	ProviderIDPrefix = "upcloud:////"

	// DefaultStorageGB is the root disk size used when UpCloudNodeClass.Spec.Storage.Size is unset.
	DefaultStorageGB = 20

	// ResourceNvidiaGPU is the standard Kubernetes GPU resource name.
	// Advertising it on GPU instance types lets Karpenter schedule pods that request nvidia.com/gpu.
	ResourceNvidiaGPU corev1.ResourceName = "nvidia.com/gpu"
)

// Drift reasons returned by IsDrifted when the live configuration no longer matches
// the NodeClaim's stored state.
const (
	// NodeClassDrifted is returned when the UpCloudNodeClass spec has changed since the NodeClaim was provisioned.
	NodeClassDrifted karpentercloudprovider.DriftReason = "NodeClassDrifted"
)
