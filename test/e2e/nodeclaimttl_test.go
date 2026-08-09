//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// TestLiveNodeClaimTTL_Path3_Decommission verifies that a NodeClaim whose TTL has expired on an empty node
// (no active pods, no matching pending pod) gets the decommissioning taint applied and the NodeClaim deleted.
func TestLiveNodeClaimTTL_Path3_Decommission(t *testing.T) {
	env := newE2ETestEnv(t)
	defer env.cleanupServers()
	srv := env.provisionServer(t, env.envPlan(), env.envCapacityType())

	env.patchTTLToExpire(t, srv.ncK8sName)
	env.taintNode(t, srv.nodeName)

	result := env.reconcileTTL(t, srv.ncK8sName)
	if result.RequeueAfter != 0 {
		debugNC := &karpv1.NodeClaim{}
		_ = env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.ncK8sName}, debugNC)
		t.Fatalf("expected decommission (requeueAfter=0), got %v; NC exists=%v", result.RequeueAfter, debugNC.Name != "")
	}

	finalNC := &karpv1.NodeClaim{}
	getErr := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.ncK8sName}, finalNC)
	if getErr == nil {
		require.False(t, finalNC.DeletionTimestamp.IsZero(), "expected deletion timestamp")
		t.Logf("NodeClaim has deletion timestamp")
	} else {
		require.True(t, kclient.IgnoreNotFound(getErr) == nil, "unexpected error: %v", getErr)
		t.Logf("NodeClaim fully deleted")
	}

	finalNode := &corev1.Node{}
	nodeErr := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.nodeName}, finalNode)
	if nodeErr != nil {
		t.Logf("node %s no longer exists: %v", srv.nodeName, nodeErr)
	} else {
		hasTaint := false
		for _, taint := range finalNode.Spec.Taints {
			if taint.Key == v1alpha1.DecommissioningTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
				hasTaint = true
				break
			}
		}
		assert.True(t, hasTaint, "expected decommissioning taint on node %s", srv.nodeName)
	}
}

// TestLiveNodeClaimTTL_Path2_Reuse verifies that a NodeClaim whose TTL has expired on an empty node is kept alive when a pending
// Unschedulable pod matching the node's instance type and taint tolerations exists (reuse path).
func TestLiveNodeClaimTTL_Path2_Reuse(t *testing.T) {
	env := newE2ETestEnv(t)
	defer env.cleanupServers()
	srv := env.provisionServer(t, env.envPlan(), env.envCapacityType())

	env.taintNode(t, srv.nodeName)

	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-pending-" + env.runID, Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{corev1.LabelInstanceTypeStable: srv.plan},
			Tolerations: []corev1.Toleration{
				{Key: "e2e-test.upcloud.com/no-schedule", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			},
			Containers: []corev1.Container{{
				Name:  "pause",
				Image: "pause",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("999999")},
				},
			}},
		},
	}
	require.NoError(t, env.kubeClient.Create(env.ctx, pendingPod), "creating pending pod")
	t.Cleanup(func() {
		_ = env.kubeClient.Delete(context.WithoutCancel(env.ctx), pendingPod)
	})
	env.waitForUnschedulablePod(t, pendingPod.Name)

	env.waitForNodeLabel(t, srv.nodeName, corev1.LabelInstanceTypeStable)
	if env.debug {
		env.dumpPendingPods(t)
	}

	// Pre-reconcile diagnostics (only when UPCLOUD_E2E_DEBUG=1): capture the exact node + pod state the controller will see.
	if env.debug {
		preNode := &corev1.Node{}
		if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.nodeName}, preNode); err == nil {
			t.Logf("[debug] pre-reconcile node instance-type label: %q", preNode.Labels[corev1.LabelInstanceTypeStable])
			t.Logf("[debug] pre-reconcile node taints: %v", preNode.Spec.Taints)
		} else {
			t.Logf("[debug] pre-reconcile node get error: %v", err)
		}
		prePod := &corev1.Pod{}
		if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: pendingPod.Name, Namespace: "default"}, prePod); err == nil {
			for _, c := range prePod.Status.Conditions {
				t.Logf("[debug] pre-reconcile pending pod condition: Type=%s Status=%s Reason=%q Message=%q", c.Type, c.Status, c.Reason, c.Message)
			}
			t.Logf("[debug] pre-reconcile pending pod NodeSelector: %v", prePod.Spec.NodeSelector)
			t.Logf("[debug] pre-reconcile pending pod Tolerations: %v", prePod.Spec.Tolerations)
		} else {
			t.Logf("[debug] pre-reconcile pending pod get error: %v", err)
		}
	}

	env.patchTTLToExpire(t, srv.ncK8sName)

	result := env.reconcileTTL(t, srv.ncK8sName)
	if result.RequeueAfter == 0 {
		// Post-reconcile diagnostics (only when UPCLOUD_E2E_DEBUG=1): re-fetch the test pod and node to see what the controller saw.
		if env.debug {
			node := &corev1.Node{}
			if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.nodeName}, node); err == nil {
				t.Logf("[debug] post-reconcile node instance-type label: %q", node.Labels[corev1.LabelInstanceTypeStable])
				t.Logf("[debug] post-reconcile node taints: %v", node.Spec.Taints)
			}
			pod := &corev1.Pod{}
			if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: pendingPod.Name, Namespace: "default"}, pod); err == nil {
				t.Logf("[debug] post-reconcile pending pod phase=%s nodeName=%q", pod.Status.Phase, pod.Spec.NodeName)
				for _, c := range pod.Status.Conditions {
					t.Logf("[debug] post-reconcile pending pod condition: Type=%s Status=%s Reason=%q Message=%q", c.Type, c.Status, c.Reason, c.Message)
				}
			} else {
				t.Logf("[debug] post-reconcile pending pod get error: %v", err)
			}
			env.dumpPendingPods(t)
		}
		t.Fatalf("expected TTL reset for matching pending pod (requeueAfter>0), got 0")
	}

	nc := &karpv1.NodeClaim{}
	require.NoError(t, env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.ncK8sName}, nc), "getting NodeClaim after reset")

	resetAt, ok := nc.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey]
	require.True(t, ok, "expected TTL reset annotation")

	resetTime, err := time.Parse(time.RFC3339, resetAt)
	require.NoError(t, err, "invalid reset timestamp: %s", resetAt)

	require.Greater(t, 30*time.Second, time.Since(resetTime), "reset timestamp too old: %s", resetAt)
	t.Logf("TTL reset at %s — path 2 confirmed", resetAt)
}

// TestLiveNodeClaimTTL_Path1_Reset verifies that a NodeClaim whose TTL has expired on a node that still hosts non-DaemonSet
// pods gets its timer reset (busy node path).
func TestLiveNodeClaimTTL_Path1_Reset(t *testing.T) {
	env := newE2ETestEnv(t)
	defer env.cleanupServers()
	srv := env.provisionServer(t, env.envPlan(), env.envCapacityType())

	busyPod := env.runPod(t, "e2e-busy-"+env.runID, srv.nodeName)
	_ = busyPod

	env.patchTTLToExpire(t, srv.ncK8sName)

	result := env.reconcileTTL(t, srv.ncK8sName)
	require.NotZero(t, result.RequeueAfter, "expected TTL reset for busy node (requeueAfter>0)")

	nc := &karpv1.NodeClaim{}
	require.NoError(t, env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.ncK8sName}, nc), "getting NodeClaim after reset")

	resetAt, ok := nc.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey]
	require.True(t, ok, "expected TTL reset annotation")

	resetTime, err := time.Parse(time.RFC3339, resetAt)
	require.NoError(t, err, "invalid reset timestamp: %s", resetAt)

	require.Greater(t, 30*time.Second, time.Since(resetTime), "reset timestamp too old: %s", resetAt)
	t.Logf("TTL reset at %s — path 1 confirmed", resetAt)
}
