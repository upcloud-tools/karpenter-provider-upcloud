package nodeclaimttl

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	v1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
)

// newScheme returns a scheme that knows about core types and karpv1.NodeClaim.
func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	s.AddKnownTypes(gv, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	return s
}

// nodeClaim is a test helper that builds a NodeClaim with the given identity and creation time.
func nodeClaim(name, nodeName string, annotations map[string]string, createdAt time.Time) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Annotations:       annotations,
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Status: karpv1.NodeClaimStatus{
			NodeName: nodeName,
		},
	}
}

// pod is a test helper that builds a Pod assigned to nodeName, optionally owned by a DaemonSet.
func pod(name, nodeName string, ds bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{NodeName: nodeName},
	}
	if ds {
		p.OwnerReferences = []metav1.OwnerReference{
			{Kind: "DaemonSet", Name: "ds"},
		}
	}
	return p
}

// node is a test helper that builds a Node with the given instance type label.
func node(name, plan string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				corev1.LabelInstanceTypeStable: plan,
			},
		},
	}
}

// pendingPod is a test helper that builds an unschedulable (pending, not assigned) pod with an optional instance-type nodeSelector.
// When plan is empty the pod has no instance-type constraint.
func pendingPod(name, plan string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionFalse},
			},
		},
	}
	if plan != "" {
		p.Spec.NodeSelector = map[string]string{corev1.LabelInstanceTypeStable: plan}
	}
	return p
}

// newController builds a Controller backed by a fake client containing the given objects.
func newController(t *testing.T, objs ...runtime.Object) *Controller {
	t.Helper()
	b := crfake.NewClientBuilder().WithScheme(newScheme(t)).WithRuntimeObjects(objs...)
	return &Controller{Client: b.Build(), TTL: 5 * time.Minute}
}

// reconcileOnce runs the controller once for the given NodeClaim name.
func reconcileOnce(t *testing.T, c *Controller, name string) (reconcile.Result, error) {
	t.Helper()
	return c.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name},
	})
}

// TestRequeueBeforeTTL verifies the controller requeues until the TTL window expires.
func TestRequeueBeforeTTL(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now())
	c := newController(t, nc)

	result, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue after non-zero duration")
	}
}

// TestDeleteEmptyNode verifies the controller deletes a NodeClaim whose TTL has expired and the node has no non-DaemonSet pods 
// and no matching pending pod exists.
func TestDeleteEmptyNode(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now().Add(-10*time.Minute))
	dsPod := pod("ds1", "node1", true)
	n := node("node1", "CLOUDNATIVE-2xCPU-4GB")
	c := newController(t, nc, dsPod, n)

	result, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatal("expected no requeue after deletion")
	}

	got := &karpv1.NodeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "nc1"}, got); err == nil {
		t.Fatal("expected NodeClaim to be deleted")
	}
}

// TestReuseForMatchingPendingPod verifies that when the TTL expires on an empty node but a pending pod requests the same 
// instance type, the TTL is reset (node reused).
func TestReuseForMatchingPendingPod(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now().Add(-10*time.Minute))
	n := node("node1", "CLOUDNATIVE-2xCPU-4GB")
	pp := pendingPod("pending-pod", "CLOUDNATIVE-2xCPU-4GB")
	c := newController(t, nc, n, pp)

	result, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != c.TTL {
		t.Fatalf("expected TTL reset (requeue after %v), got %v", c.TTL, result.RequeueAfter)
	}

	got := &karpv1.NodeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "nc1"}, got); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	if got.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey] == "" {
		t.Fatal("expected TTL reset annotation for pending pod reuse")
	}
}

// TestReuseForPendingPodNoConstraint verifies that a pending pod without any instance-type constraint also keeps the node 
// alive (it can be scheduled here).
func TestReuseForPendingPodNoConstraint(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now().Add(-10*time.Minute))
	n := node("node1", "CLOUDNATIVE-2xCPU-4GB")
	pp := pendingPod("pending-pod", "") // no instance-type constraint
	c := newController(t, nc, n, pp)

	result, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != c.TTL {
		t.Fatalf("expected TTL reset, got %v", result.RequeueAfter)
	}
}

// TestDeleteWhenPendingPodMismatch verifies that the node is decommissioned when the only pending pod requests a different instance type.
func TestDeleteWhenPendingPodMismatch(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now().Add(-10*time.Minute))
	n := node("node1", "CLOUDNATIVE-2xCPU-4GB")
	pp := pendingPod("pending-pod", "CLOUDNATIVE-4xCPU-8GB")
	c := newController(t, nc, n, pp)

	result, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected deletion, got requeue after %v", result.RequeueAfter)
	}

	// Verify the NodeClaim was deleted.
	got := &karpv1.NodeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "nc1"}, got); err == nil {
		t.Fatal("expected NodeClaim to be deleted")
	}
}

// TestResetOnNonDSPods verifies the controller resets the TTL (via annotation) when the node still hosts non-DaemonSet pods.
func TestResetOnNonDSPods(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now().Add(-10*time.Minute))
	appPod := pod("app1", "node1", false)
	c := newController(t, nc, appPod)

	result, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != c.TTL {
		t.Fatalf("expected requeue after %v, got %v", c.TTL, result.RequeueAfter)
	}

	got := &karpv1.NodeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "nc1"}, got); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	if got.Annotations == nil {
		t.Fatal("expected annotations after reset")
	}
	resetAt, ok := got.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey]
	if !ok {
		t.Fatal("expected TTL reset annotation")
	}
	if _, err := time.Parse(time.RFC3339, resetAt); err != nil {
		t.Fatalf("invalid reset timestamp: %v", err)
	}
}

// TestResetRespectedOnNextReconcile verifies a previously reset TTL deadline is picked up on the next reconcile cycle as the new start time.
func TestResetRespectedOnNextReconcile(t *testing.T) {
	resetAt := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
	nc := nodeClaim("nc1", "node1",
		map[string]string{v1alpha1.NodeClaimTTLResetAnnotationKey: resetAt},
		time.Now().Add(-1*time.Hour))
	appPod := pod("app1", "node1", false)
	c := newController(t, nc, appPod)

	result, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != c.TTL {
		t.Fatalf("expected requeue after %v based on reset timestamp, got %v", c.TTL, result.RequeueAfter)
	}
}

// TestTaintAddedBeforeDelete verifies that the controller adds the decommissioning NoSchedule taint before deleting the NodeClaim.
func TestTaintAddedBeforeDelete(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now().Add(-10*time.Minute))
	n := node("node1", "CLOUDNATIVE-2xCPU-4GB")
	dsPod := pod("ds1", "node1", true)
	c := newController(t, nc, n, dsPod)

	_, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gotNode := &corev1.Node{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "node1"}, gotNode); err != nil {
		t.Fatalf("get node: %v", err)
	}

	hasTaint := false
	for _, taint := range gotNode.Spec.Taints {
		if taint.Key == v1alpha1.DecommissioningTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
			hasTaint = true
			break
		}
	}
	if !hasTaint {
		t.Fatal("expected decommissioning NoSchedule taint on node")
	}
}

// TestDaemonSetPodsIgnored verifies that DaemonSet-owned pods are not counted as non-DS pods, so the node is still considered empty.
func TestDaemonSetPodsIgnored(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now().Add(-10*time.Minute))
	n := node("node1", "CLOUDNATIVE-2xCPU-4GB")
	dsPod := pod("ds1", "node1", true)
	c := newController(t, nc, n, dsPod)

	_, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &karpv1.NodeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "nc1"}, got); err == nil {
		t.Fatal("expected NodeClaim to be deleted when only DS pods exist")
	}
}

// TestSkipDeletingNodeClaim verifies the controller skips NodeClaims that are already being deleted (deletionTimestamp set).
func TestSkipDeletingNodeClaim(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now().Add(-10*time.Minute))
	nc.Finalizers = []string{"upcloud.com/termination"}
	now := metav1.Now()
	nc.DeletionTimestamp = &now
	c := newController(t, nc)

	result, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatal("expected no requeue for already-deleting NodeClaim")
	}
}

// TestSkipNoNodeName verifies the controller skips NodeClaims whose Node has not yet been registered (NodeName is empty).
func TestSkipNoNodeName(t *testing.T) {
	nc := nodeClaim("nc1", "", nil, time.Now().Add(-10*time.Minute))
	c := newController(t, nc)

	result, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatal("expected no requeue when node name is empty")
	}
}

// TestIgnoreTerminatingPods verifies that pods in the process of terminating (deletionTimestamp set, finalizers still blocking removal) 
// are treated as empty and do not prevent TTL expiry.
func TestIgnoreTerminatingPods(t *testing.T) {
	nc := nodeClaim("nc1", "node1", nil, time.Now().Add(-10*time.Minute))
	n := node("node1", "CLOUDNATIVE-2xCPU-4GB")
	appPod := pod("app1", "node1", false)
	appPod.Finalizers = []string{"some/finalizer"}
	now := metav1.Now()
	appPod.DeletionTimestamp = &now
	c := newController(t, nc, n, appPod)

	_, err := reconcileOnce(t, c, "nc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &karpv1.NodeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "nc1"}, got); err == nil {
		t.Fatal("expected NodeClaim to be deleted when pods are terminating")
	}
}
