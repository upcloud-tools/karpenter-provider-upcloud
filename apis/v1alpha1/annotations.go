package v1alpha1

const (
	// NodeClaimTTLResetAnnotationKey is set on a NodeClaim when its TTL expires but the corresponding node still has non-DaemonSet pods,
	// resetting the absolute-lifetime timer (consolidateAfter).
	NodeClaimTTLResetAnnotationKey = "karpenter.upcloud.com/ttl-reset-at"

	// DecommissioningTaintKey is applied to a Node when the TTL expires and no pending pod can reuse it,
	// preventing new pods from being scheduled before deletion completes.
	DecommissioningTaintKey = "karpenter.upcloud.com/decommissioning"
)
