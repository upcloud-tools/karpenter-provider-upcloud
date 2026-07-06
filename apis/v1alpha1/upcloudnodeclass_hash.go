package v1alpha1

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// NodeClassHashAnnotationKey is the annotation Karpenter stores on a NodeClaim at creation containing the hash of the UpCloudNodeClass 
// it was provisioned against. IsDrifted compares this stored value against the live NodeClass hash to detect configuration drift.
const NodeClassHashAnnotationKey = "karpenter.upcloud.com/nodeclass-hash"

// Hash returns a stable hash of the UpCloudNodeClass desired state. Go's json.Marshal sorts
// map keys, so the output is deterministic across runs and nodes.
func (u *UpCloudNodeClass) Hash() string {
	b, err := json.Marshal(u.Spec)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
