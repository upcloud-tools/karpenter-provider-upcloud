// Package events defines the Kubernetes events emitted by the UpCloud Karpenter provider.
package events

import (
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpenterevents "sigs.k8s.io/karpenter/pkg/events"
)

// NodeClaimFailedToResolveNodeClass is emitted on a NodeClaim whose UpCloudNodeClass cannot be resolved
// (e.g. the NodeClass was deleted or the reference is missing). While unresolvable, drift evaluation is
// skipped rather than failing the reconcile loop, so this event is the operator's only signal that the
// NodeClaim's drift status is not being assessed.
func NodeClaimFailedToResolveNodeClass(nodeClaim *karpv1.NodeClaim) karpenterevents.Event {
	return karpenterevents.Event{
		InvolvedObject: nodeClaim,
		Type:           corev1.EventTypeWarning,
		Reason:         "NodeClaimFailedToResolveNodeClass",
		Message:        "Failed to resolve the UpCloudNodeClass for this NodeClaim; drift is not evaluated until the NodeClass exists again.",
		DedupeValues:   []string{string(nodeClaim.UID), "NodeClassUnresolvable"},
	}
}
