package nodeclass

import (
	"context"
	"testing"

	"github.com/awslabs/operatorpkg/status"
	apiv1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// testNodeClaim builds a launched NodeClaim (providerID set, as after provisioning) referencing nodeclassName,
// mirroring how karpenter core stamps the NodeClassRef GVK. With drifted, the claim carries an evaluated Drifted=True condition.
func testNodeClaim(name, nodeclassName string, annotations map[string]string, drifted bool) *karpv1.NodeClaim {
	nc := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Kind:  "UpCloudNodeClass",
				Name:  nodeclassName,
				Group: apiv1.Group,
			},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID: apiv1.ProviderIDPrefix + name,
		},
	}
	if drifted {
		nc.StatusConditions().SetTrue(karpv1.ConditionTypeDrifted)
	}
	return nc
}

func buildClient(t *testing.T, nc *apiv1.UpCloudNodeClass, extra ...client.Object) crfake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := apiv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add upcloud scheme: %v", err)
	}
	karpGV := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	metav1.AddToGroupVersion(scheme, karpGV)
	scheme.AddKnownTypes(karpGV, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})

	builder := crfake.NewClientBuilder().WithScheme(scheme).
		WithObjects(nc).WithStatusSubresource(&apiv1.UpCloudNodeClass{})
	// The field indexes below mirror what the karpenter operator registers on the manager cache;
	// nodeclaimutils.ForNodeClass selects claims via MatchingFields.
	for _, idx := range []struct {
		field string
		get   func(*karpv1.NodeClaim) string
	}{
		{"spec.nodeClassRef.group", func(n *karpv1.NodeClaim) string { return n.Spec.NodeClassRef.Group }},
		{"spec.nodeClassRef.kind", func(n *karpv1.NodeClaim) string { return n.Spec.NodeClassRef.Kind }},
		{"spec.nodeClassRef.name", func(n *karpv1.NodeClaim) string { return n.Spec.NodeClassRef.Name }},
	} {
		builder = builder.WithIndex(&karpv1.NodeClaim{}, idx.field, func(o client.Object) []string {
			ref := o.(*karpv1.NodeClaim).Spec.NodeClassRef
			if ref == nil {
				return nil
			}
			return []string{idx.get(o.(*karpv1.NodeClaim))}
		})
	}
	if len(extra) > 0 {
		builder = builder.WithObjects(extra...)
	}
	return *builder
}

// reconcileNodeClass runs one Reconcile for nc (plus the given extra objects in the cluster) and
// returns the post-reconcile NodeClass and the client, so callers can inspect patched NodeClaims.
func reconcileNodeClass(t *testing.T, nc *apiv1.UpCloudNodeClass, extra ...client.Object) (*apiv1.UpCloudNodeClass, client.Client) {
	t.Helper()
	builder := buildClient(t, nc, extra...)
	client := builder.Build()
	r := &Controller{Client: client}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: nc.Name}}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	got := &apiv1.UpCloudNodeClass{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: nc.Name}, got); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	return got, client
}

func reconcileAndGet(t *testing.T, nc *apiv1.UpCloudNodeClass) *apiv1.UpCloudNodeClass {
	t.Helper()
	got, _ := reconcileNodeClass(t, nc)
	return got
}

func TestReconcileValidSpecReady(t *testing.T) {
	nc := &apiv1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "valid", Finalizers: []string{apiv1.NodeClassFinalizer}},
		Spec:       apiv1.UpCloudNodeClassSpec{Zone: "de-fra1", Plan: "2xCPU-4GB"},
	}
	got := reconcileAndGet(t, nc)
	cond := got.StatusConditions().Get(status.ConditionReady)
	if cond == nil || !cond.IsTrue() {
		t.Errorf("expected Ready=True for valid spec, got %+v", cond)
	}
	if got.Status.Hash == "" {
		t.Errorf("expected resolved hash to be stored on status")
	}
	if got.Status.Hash != nc.Hash() {
		t.Errorf("expected stored hash %q to match computed hash %q", got.Status.Hash, nc.Hash())
	}
}

func TestReconcileMissingZoneNotReady(t *testing.T) {
	nc := &apiv1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid", Finalizers: []string{apiv1.NodeClassFinalizer}},
		Spec:       apiv1.UpCloudNodeClassSpec{Plan: "2xCPU-4GB"},
	}
	got := reconcileAndGet(t, nc)
	cond := got.StatusConditions().Get(status.ConditionReady)
	if cond == nil || !cond.IsFalse() {
		t.Errorf("expected Ready=False for missing zone, got %+v", cond)
	}
}

func TestReconcileMissingPlanNotReady(t *testing.T) {
	nc := &apiv1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid2", Finalizers: []string{apiv1.NodeClassFinalizer}},
		Spec:       apiv1.UpCloudNodeClassSpec{Zone: "de-fra1"},
	}
	got := reconcileAndGet(t, nc)
	cond := got.StatusConditions().Get(status.ConditionReady)
	if cond == nil || !cond.IsFalse() {
		t.Errorf("expected Ready=False for missing plan, got %+v", cond)
	}
}

func TestReconcileRebaselinesAndAdoptsClaimHashes(t *testing.T) {
	nc := &apiv1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "rebaseline", Finalizers: []string{apiv1.NodeClassFinalizer}},
		Spec:       apiv1.UpCloudNodeClassSpec{Zone: "de-fra1", Plan: "2xCPU-4GB"},
	}
	// legacy: v1-style hash, no version annotation -> re-baseline in place.
	legacy := testNodeClaim("legacy", "rebaseline", map[string]string{
		apiv1.NodeClassHashAnnotationKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}, false)
	// current: already at the running hash version -> must be left alone even if the hash is stale,
	// because a hash/version mismatch for a current-version claim is genuine drift.
	current := testNodeClaim("current", "rebaseline", map[string]string{
		apiv1.NodeClassHashAnnotationKey:        "not-the-live-hash",
		apiv1.NodeClassHashVersionAnnotationKey: apiv1.NodeClassHashVersion,
	}, false)
	// drifted: real drift evaluated before the upgrade -> keep the old hash so drift survives re-baseline.
	drifted := testNodeClaim("drifted", "rebaseline", map[string]string{
		apiv1.NodeClassHashAnnotationKey: "deadbeefdeadbeef",
	}, true)
	// adopted: launched claim with no hash at all (predates drift detection) -> adopted into drift detection.
	adopted := testNodeClaim("adopted", "rebaseline", nil, false)
	// inflight: not yet launched (no providerID) -> Create() will stamp it; the controller must not race with provisioning.
	inflight := testNodeClaim("inflight", "rebaseline", nil, false)
	inflight.Status.ProviderID = ""

	_, kubeClient := reconcileNodeClass(t, nc, legacy, current, drifted, adopted, inflight)

	for _, tc := range []struct {
		name        string
		wantHash    string
		wantVersion string
	}{
		{"legacy", nc.Hash(), apiv1.NodeClassHashVersion},
		{"current", "not-the-live-hash", apiv1.NodeClassHashVersion},
		{"drifted", "deadbeefdeadbeef", ""},
		{"adopted", nc.Hash(), apiv1.NodeClassHashVersion},
		{"inflight", "", ""},
	} {
		got := &karpv1.NodeClaim{}
		if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: tc.name}, got); err != nil {
			t.Fatalf("get claim %s: %v", tc.name, err)
		}
		if h := got.Annotations[apiv1.NodeClassHashAnnotationKey]; h != tc.wantHash {
			t.Errorf("claim %s: hash = %q, want %q", tc.name, h, tc.wantHash)
		}
		if v := got.Annotations[apiv1.NodeClassHashVersionAnnotationKey]; v != tc.wantVersion {
			t.Errorf("claim %s: hash version = %q, want %q", tc.name, v, tc.wantVersion)
		}
	}
}

func TestReconcileSkipsRebaselineWhenValidationFails(t *testing.T) {
	nc := &apiv1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid3", Finalizers: []string{apiv1.NodeClassFinalizer}},
		Spec:       apiv1.UpCloudNodeClassSpec{Plan: "2xCPU-4GB"},
	}
	claim := testNodeClaim("legacy2", "invalid3", map[string]string{
		apiv1.NodeClassHashAnnotationKey: "0123456789abcdef",
	}, false)

	_, kubeClient := reconcileNodeClass(t, nc, claim)

	got := &karpv1.NodeClaim{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "legacy2"}, got); err != nil {
		t.Fatalf("get claim: %v", err)
	}
	if v := got.Annotations[apiv1.NodeClassHashVersionAnnotationKey]; v != "" {
		t.Errorf("claim must not be re-baselined while the NodeClass is invalid, got version %q", v)
	}
}
