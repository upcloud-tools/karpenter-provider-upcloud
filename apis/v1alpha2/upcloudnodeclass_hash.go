package v1alpha2

import (
	"fmt"

	"github.com/mitchellh/hashstructure/v2"
)

// NodeClassHashAnnotationKey is the annotation Karpenter stores on a NodeClaim at creation containing the hash of the UpCloudNodeClass
// it was provisioned against. IsDrifted compares this stored value against the live NodeClass hash to detect configuration drift.
const NodeClassHashAnnotationKey = "karpenter.k8s.upcloud/nodeclass-hash"

// NodeClassHashVersionAnnotationKey records which version of the hash algorithm produced the NodeClaim's stored hash.
// IsDrifted only compares claims whose version matches the running one, and the nodeclass controller refreshes stale claims,
// so upgrading the hash algorithm never recycles unchanged nodes.
const NodeClassHashVersionAnnotationKey = "karpenter.k8s.upcloud/nodeclass-hash-version"

// NodeClassHashVersion identifies the current Hash() implementation. Bump it whenever the algorithm or hash
// inputs change in a way that yields a different digest for an unchanged spec.
// Version history:
//   - v2: hashstructure/v2 with slices-as-sets; order-insensitive, zero-value-insensitive.
//   - v1 (unversioned): SHA-256 over the JSON-serialized spec; sensitive to slice element order.
const NodeClassHashVersion = "v2"

// Hash returns a stable hash of the UpCloudNodeClass desired state. Slices (taints, SSH keys, kubelet args)
// are hashed as sets and zero-valued fields are ignored, so semantically equivalent specs produce the same
// digest regardless of element ordering or empty-vs-omitted list fields.
func (u *UpCloudNodeClass) Hash() string {
	spec := u.Spec.DeepCopy()
	if len(spec.SSHKeys) == 0 {
		spec.SSHKeys = nil
	}
	if len(spec.KubeletArgs) == 0 {
		spec.KubeletArgs = nil
	}
	if len(spec.Labels) == 0 {
		spec.Labels = nil
	}
	if len(spec.Taints) == 0 {
		spec.Taints = nil
	}
	h, err := hashstructure.Hash(spec, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
	})
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%016x", h)
}
