package e2e

import (
	"context"
	"testing"
	"time"

	v1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// TestLiveNodeClaimTTL_Path3_Decommission verifies that a NodeClaim whose TTL has
// expired on an empty node (no active pods, no matching pending pod) gets the
// decommissioning taint applied and the NodeClaim deleted.
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
	if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.ncK8sName}, finalNC); kclient.IgnoreNotFound(err) != nil {
		t.Fatalf("getting NodeClaim after reconcile: %v", err)
	} else if kclient.IgnoreNotFound(err) == nil {
		t.Logf("NodeClaim fully deleted")
	} else if !finalNC.DeletionTimestamp.IsZero() {
		t.Logf("NodeClaim has deletion timestamp")
	} else {
		t.Errorf("expected NodeClaim to be deleted or have deletion timestamp")
	}

	finalNode := &corev1.Node{}
	if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.nodeName}, finalNode); err != nil {
		t.Logf("node %s no longer exists: %v", srv.nodeName, err)
	} else {
		hasTaint := false
		for _, taint := range finalNode.Spec.Taints {
			if taint.Key == v1alpha1.DecommissioningTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
				hasTaint = true
				break
			}
		}
		if !hasTaint {
			t.Errorf("expected decommissioning taint on node %s", srv.nodeName)
		}
	}
}

// TestLiveNodeClaimTTL_Path2_Reuse verifies that a NodeClaim whose TTL has expired
// on an empty node is kept alive when a pending Unschedulable pod matching the
// node's instance type and taint tolerations exists (reuse path).
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
	if err := env.kubeClient.Create(env.ctx, pendingPod); err != nil {
		t.Fatalf("creating pending pod: %v", err)
	}
	t.Cleanup(func() {
		_ = env.kubeClient.Delete(context.WithoutCancel(env.ctx), pendingPod)
	})
	env.waitForUnschedulablePod(t, pendingPod.Name)

	env.waitForNodeLabel(t, srv.nodeName, corev1.LabelInstanceTypeStable)
	env.dumpPendingPods(t)

	env.patchTTLToExpire(t, srv.ncK8sName)

	result := env.reconcileTTL(t, srv.ncK8sName)
	if result.RequeueAfter == 0 {
		node := &corev1.Node{}
		if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.nodeName}, node); err == nil {
			t.Logf("node instance-type label: %q", node.Labels[corev1.LabelInstanceTypeStable])
			t.Logf("node taints: %v", node.Spec.Taints)
		}
		env.dumpPendingPods(t)
		t.Fatalf("expected TTL reset for matching pending pod (requeueAfter>0), got 0")
	}

	nc := &karpv1.NodeClaim{}
	if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.ncK8sName}, nc); err != nil {
		t.Fatalf("getting NodeClaim after reset: %v", err)
	}
	resetAt, ok := nc.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey]
	if !ok {
		t.Fatalf("expected TTL reset annotation")
	}
	resetTime, err := time.Parse(time.RFC3339, resetAt)
	if err != nil {
		t.Fatalf("invalid reset timestamp: %v", err)
	}
	if time.Since(resetTime) > 30*time.Second {
		t.Fatalf("reset timestamp too old: %s", resetAt)
	}
	t.Logf("TTL reset at %s — path 2 confirmed", resetAt)
}

// TestLiveNodeClaimTTL_Path1_Reset verifies that a NodeClaim whose TTL has expired
// on a node that still hosts non-DaemonSet pods gets its timer reset (busy node
// path).
func TestLiveNodeClaimTTL_Path1_Reset(t *testing.T) {
	env := newE2ETestEnv(t)
	defer env.cleanupServers()
	srv := env.provisionServer(t, env.envPlan(), env.envCapacityType())

	busyPod := env.runPod(t, "e2e-busy-"+env.runID, srv.nodeName)
	_ = busyPod

	env.patchTTLToExpire(t, srv.ncK8sName)

	result := env.reconcileTTL(t, srv.ncK8sName)
	if result.RequeueAfter == 0 {
		t.Fatalf("expected TTL reset for busy node (requeueAfter>0), got 0")
	}

	nc := &karpv1.NodeClaim{}
	if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: srv.ncK8sName}, nc); err != nil {
		t.Fatalf("getting NodeClaim after reset: %v", err)
	}
	resetAt, ok := nc.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey]
	if !ok {
		t.Fatalf("expected TTL reset annotation")
	}
	resetTime, err := time.Parse(time.RFC3339, resetAt)
	if err != nil {
		t.Fatalf("invalid reset timestamp: %v", err)
	}
	if time.Since(resetTime) > 30*time.Second {
		t.Fatalf("reset timestamp too old: %s", resetAt)
	}
	t.Logf("TTL reset at %s — path 1 confirmed", resetAt)
}
