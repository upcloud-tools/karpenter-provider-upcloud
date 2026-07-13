package nodeclaimttl

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	v1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
)

// Controller implements an absolute-lifetime TTL eviction controller for NodeClaims.
// It replaces Karpenter's built-in consolidateAfter behaviour.
//
// When the TTL expires the controller follows a three-way decision tree:
//  1. Node has non-DaemonSet pods → reset the TTL (node is still busy).
//  2. Node is empty but a pending pod with reason Unschedulable matches this node's plan → reset the TTL and reuse the node.
//  3. Node is empty and no matching pending pod → add a NoSchedule taint and delete the NodeClaim.
type Controller struct {
	client.Client
	TTL time.Duration
}

// Reconcile is the main reconciliation loop for the NodeClaim TTL controller.
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	nc := &karpv1.NodeClaim{}
	if err := c.Get(ctx, req.NamespacedName, nc); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// If the NodeClaim is being deleted or has no node assigned, exit early.
	if !nc.DeletionTimestamp.IsZero() || nc.Status.NodeName == "" {
		return reconcile.Result{}, nil
	}

	start := ttlStart(nc)
	elapsed := time.Since(start)
	if elapsed < c.TTL {
		return reconcile.Result{RequeueAfter: c.TTL - elapsed}, nil
	}

	// ── TTL expired ──────────────────────────────────────────────

	podList := &corev1.PodList{}
	if err := c.List(ctx, podList); err != nil {
		return reconcile.Result{}, fmt.Errorf("listing pods: %w", err)
	}

	// Path 1 – node still has non-DaemonSet pods: keep alive.
	if hasNonDSPods(podList.Items, nc.Status.NodeName) {
		return c.resetTTL(ctx, nc, "node has non-DS pods, resetting timer")
	}

	node := &corev1.Node{}
	if err := c.Get(ctx, types.NamespacedName{Name: nc.Status.NodeName}, node); err != nil {
		return reconcile.Result{}, fmt.Errorf("getting node %s: %w", nc.Status.NodeName, err)
	}

	// Path 2 – node is empty but a pending pod (reason Unschedulable) could reuse it.
	if hasMatchingPendingPod(podList.Items, node) {
		return c.resetTTL(ctx, nc, "matching pending pod found, reusing node")
	}

	// Path 3 – decommission: taint then delete.
	if err := c.taintNode(ctx, node); err != nil {
		return reconcile.Result{}, fmt.Errorf("tainting node %s: %w", nc.Status.NodeName, err)
	}
	if err := c.Delete(ctx, nc); err != nil {
		return reconcile.Result{}, fmt.Errorf("deleting NodeClaim: %w", err)
	}
	log.FromContext(ctx).Info("TTL expired, tainted and deleted NodeClaim", "node", nc.Status.NodeName)
	return reconcile.Result{}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// resetTTL patches the TTL reset annotation to the current time and requeues.
func (c *Controller) resetTTL(ctx context.Context, nc *karpv1.NodeClaim, reason string) (reconcile.Result, error) {
	patch := client.MergeFrom(nc.DeepCopy())
	if nc.Annotations == nil {
		nc.Annotations = map[string]string{}
	}
	nc.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey] = time.Now().Format(time.RFC3339)
	if err := c.Patch(ctx, nc, patch); err != nil {
		return reconcile.Result{}, fmt.Errorf("patching TTL reset: %w", err)
	}
	log.FromContext(ctx).Info("TTL reset, "+reason, "node", nc.Status.NodeName, "ttl", c.TTL)
	return reconcile.Result{RequeueAfter: c.TTL}, nil
}

// ttlStart returns the effective TTL start time: either the last reset annotation or the NodeClaim's metadata.creationTimestamp.
func ttlStart(nc *karpv1.NodeClaim) time.Time {
	if nc.Annotations != nil {
		if v, ok := nc.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey]; ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t
			}
		}
	}
	return nc.CreationTimestamp.Time
}

// hasNonDSPods returns true when the node has at least one non-DaemonSet, non-terminating pod.
func hasNonDSPods(pods []corev1.Pod, nodeName string) bool {
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName == nodeName && !isDaemonSet(p) && p.DeletionTimestamp.IsZero() {
			return true
		}
	}
	return false
}

// hasMatchingPendingPod returns true when there is a pending (unschedulable) pod whose instance-type requirements are compatible
// with the node's plan and whose tolerations accept the node's taints.
func hasMatchingPendingPod(pods []corev1.Pod, node *corev1.Node) bool {
	plan := instancePlan(node)
	for i := range pods {
		p := &pods[i]
		if isMatchingPending(p, plan, node.Spec.Taints) {
			return true
		}
	}
	return false
}

// isMatchingPending returns true when the pod is pending with reason Unschedulable (the scheduler couldn't find a fitting node),
// its instance-type requirements are compatible with plan, and it tolerates the node's taints. A pod stuck for another reason
// (e.g. ErrImagePull, CrashLoopBackOff) is not considered matching because it wouldn't make progress even if scheduled to this node.
// A pod without any instance-type constraint is considered matching because it can be scheduled on this node.
func isMatchingPending(pod *corev1.Pod, plan string, taints []corev1.Taint) bool {
	if pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName != "" {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type != corev1.PodScheduled {
			continue
		}
		if c.Status == corev1.ConditionTrue {
			return false // already scheduled
		}
		if c.Reason != "Unschedulable" {
			return false // stuck on something other than node availability
		}
	}
	if !podFitsInstanceType(pod, plan) {
		return false
	}
	if !podToleratesNodeTaints(pod, taints) {
		return false
	}
	return true
}

// podToleratesNodeTaints returns true when the pod's tolerations satisfy all NoSchedule and NoExecute taints on the node.
// PreferNoSchedule taints are treated as soft preferences and ignored.
func podToleratesNodeTaints(pod *corev1.Pod, taints []corev1.Taint) bool {
	for _, taint := range taints {
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if !tolerationMatches(taint, pod.Spec.Tolerations) {
			return false
		}
	}
	return true
}

// tolerationMatches returns true when at least one toleration covers the given taint, following k8s toleration semantics:
// key match (empty key matches all), effect match (empty matches all), and value match based on operator (Exists or Equal).
func tolerationMatches(taint corev1.Taint, tolerations []corev1.Toleration) bool {
	for _, t := range tolerations {
		if t.Key != "" && t.Key != taint.Key {
			continue
		}
		if t.Effect != "" && t.Effect != taint.Effect {
			continue
		}
		if t.Operator == corev1.TolerationOpExists {
			return true
		}
		if t.Value == taint.Value {
			return true
		}
	}
	return false
}

// podFitsInstanceType checks whether the pod's nodeSelector and node affinity allow the given plan (node.kubernetes.io/instance-type).
func podFitsInstanceType(pod *corev1.Pod, plan string) bool {
	// Explicit nodeSelector entry.
	if v, ok := pod.Spec.NodeSelector[corev1.LabelInstanceTypeStable]; ok && v != plan {
		return false
	}

	// RequiredDuringScheduling node affinity.
	aff := pod.Spec.Affinity
	if aff != nil && aff.NodeAffinity != nil && aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		for _, term := range aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			found := false
			failed := false
			for _, expr := range term.MatchExpressions {
				if expr.Key != corev1.LabelInstanceTypeStable {
					continue
				}
				if expr.Operator == corev1.NodeSelectorOpIn || expr.Operator == corev1.NodeSelectorOpExists {
					found = true
					if expr.Operator == corev1.NodeSelectorOpIn && !slices.Contains(expr.Values, plan) {
						failed = true
						break
					}
				}
			}
			if !found {
				// This term doesn't constrain instance type → pod fits.
				continue
			}
			if !failed {
				return true
			}
		}
		// If every term rejected the plan, the pod doesn't fit. If there were no terms at all (all skipped via !found), fall through.
		if aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms != nil {
			hasConstrainingTerm := false
			for _, term := range aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
				for _, expr := range term.MatchExpressions {
					if expr.Key == corev1.LabelInstanceTypeStable {
						hasConstrainingTerm = true
					}
				}
			}
			if hasConstrainingTerm {
				return false
			}
		}
	}

	return true
}

// instancePlan reads the node's instance-type label.
func instancePlan(node *corev1.Node) string {
	if node.Labels != nil {
		return node.Labels[corev1.LabelInstanceTypeStable]
	}
	return ""
}

// taintNode adds the decommissioning NoSchedule taint if not already present.
func (c *Controller) taintNode(ctx context.Context, node *corev1.Node) error {
	for _, t := range node.Spec.Taints {
		if t.Key == v1alpha1.DecommissioningTaintKey {
			return nil // already tainted
		}
	}
	patch := client.MergeFrom(node.DeepCopy())
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    v1alpha1.DecommissioningTaintKey,
		Effect: corev1.TaintEffectNoSchedule,
	})
	return c.Patch(ctx, node, patch)
}

func isDaemonSet(pod *corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// SetupWithManager registers the controller with the given manager to watch NodeClaims.
func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&karpv1.NodeClaim{}).
		Complete(c)
}
