package cloudprovider

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
	apisv1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instance"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instancetypes"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/userdata"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpentercloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// ---- fake UpCloud server service ----

type fakeServer struct {
	service.Server
	mu      sync.Mutex
	servers map[string]*upcloud.ServerDetails
	nextID  int
	lastReq *request.CreateServerRequest
}

func (f *fakeServer) CreateServer(_ context.Context, r *request.CreateServerRequest) (*upcloud.ServerDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.lastReq = r
	uuid := fmt.Sprintf("uuid-%d", f.nextID)
	sd := &upcloud.ServerDetails{
		Server: upcloud.Server{
			UUID:         uuid,
			Hostname:     r.Hostname,
			Plan:         r.Plan,
			CoreNumber:   4,
			MemoryAmount: 8192,
			Zone:         r.Zone,
			State:        upcloud.ServerStateStarted,
		},
	}
	if r.Labels != nil {
		sd.Labels = *r.Labels
	}
	f.servers[uuid] = sd
	return sd, nil
}

func (f *fakeServer) GetServerDetails(_ context.Context, r *request.GetServerDetailsRequest) (*upcloud.ServerDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sd, ok := f.servers[r.UUID]
	if !ok {
		return nil, fmt.Errorf("SERVER_NOT_FOUND")
	}
	return sd, nil
}

func (f *fakeServer) GetServers(_ context.Context) (*upcloud.Servers, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ss []upcloud.Server
	for _, sd := range f.servers {
		ss = append(ss, sd.Server)
	}
	return &upcloud.Servers{Servers: ss}, nil
}

func (f *fakeServer) StopServer(_ context.Context, r *request.StopServerRequest) (*upcloud.ServerDetails, error) {
	return f.GetServerDetails(context.Background(), &request.GetServerDetailsRequest{UUID: r.UUID})
}

func (f *fakeServer) WaitForServerState(_ context.Context, r *request.WaitForServerStateRequest) (*upcloud.ServerDetails, error) {
	return f.GetServerDetails(context.Background(), &request.GetServerDetailsRequest{UUID: r.UUID})
}

func (f *fakeServer) DeleteServerAndStorages(_ context.Context, r *request.DeleteServerAndStoragesRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.servers, r.UUID)
	return nil
}

type fakeCloud struct {
	service.Cloud
	plans  *upcloud.Plans
	prices *upcloud.PricesByZone
}

func (f *fakeCloud) GetPlans(_ context.Context) (*upcloud.Plans, error) { return f.plans, nil }
func (f *fakeCloud) GetPricesByZone(_ context.Context) (*upcloud.PricesByZone, error) {
	return f.prices, nil
}

// ---- harness construction ----

func newTestProvider(t *testing.T) (*UpCloudCloudProvider, *fakeServer, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add upcloud scheme: %v", err)
	}

	nodeClass := &apisv1alpha1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: apisv1alpha1.UpCloudNodeClassSpec{
			Zone:   "de-fra1",
			Plan:   "GPU-4xCPU-8GB",
			Labels: map[string]string{"team": "ai"},
			Taints: nil,
		},
	}
	caCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: metav1.NamespaceSystem},
		Data:       map[string]string{"ca.crt": "dummy-ca-cert"},
	}

	kubeClient := crfake.NewClientBuilder().WithScheme(scheme).WithObjects(nodeClass, caCM).Build()

	csClient := fake.NewSimpleClientset()
	csClient.PrependReactor("create", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		csr := action.(k8stesting.CreateAction).GetObject().(*certificatesv1.CertificateSigningRequest)
		csr.Status.Certificate = []byte("fake-signed-cert")
		if err := csClient.Tracker().Add(csr); err != nil {
			return true, nil, err
		}
		return true, csr, nil
	})

	fakeSrv := &fakeServer{servers: map[string]*upcloud.ServerDetails{}}
	instanceProvider := instance.NewProvider(fakeSrv, "template-uuid", "network-uuid")
	userDataProvider := userdata.NewProvider()

	itProvider := instancetypes.NewProvider(
		&fakeCloud{
			plans: &upcloud.Plans{Plans: []upcloud.Plan{
				{Name: "CLOUDNATIVE-2xCPU-4GB", CoreNumber: 2, MemoryAmount: 4096},
				{Name: "CLOUDNATIVE-4xCPU-8GB", CoreNumber: 4, MemoryAmount: 8192},
				{Name: "GPU-4xCPU-8GB", CoreNumber: 4, MemoryAmount: 8192, GPUAmount: 1, GPUModel: "NVIDIA L4"},
				{Name: "GPU-SPOT-4xCPU-8GB", CoreNumber: 4, MemoryAmount: 8192, GPUAmount: 1, GPUModel: "NVIDIA L4"},
			}},
			prices: &upcloud.PricesByZone{"de-fra1": map[string]upcloud.Price{
				"CLOUDNATIVE-2xCPU-4GB": {Price: 0.05},
				"CLOUDNATIVE-4xCPU-8GB": {Price: 0.10},
				"GPU-4xCPU-8GB":         {Price: 0.20},
				"GPU-SPOT-4xCPU-8GB":    {Price: 0.08},
			}},
		},
		"de-fra1",
	)
	if err := itProvider.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh instance types: %v", err)
	}

	cp := NewCloudProvider(kubeClient, kubernetes.Interface(csClient), instanceProvider, userDataProvider, itProvider, "de-fra1", "https://10.0.0.1:6443", 30*time.Minute)
	return cp, fakeSrv, kubeClient
}

func newTestNodeClaim() *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-nc",
			Labels: map[string]string{"workload": "batch"},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{Name: "default"},
		},
	}
}

// ---- unit tests ----

func TestBuildNodeClaimCapacityAndProviderID(t *testing.T) {
	// Hostname follows the production convention from cloudprovider.Create: "karpenter-<16 hex chars>".
	const hostname = "karpenter-67c0166a11fa4293"
	const serverUUID = "2f1c3d4e-5a6b-7c8d-9e0f-1a2b3c4d5e6f"
	sd := upcloud.ServerDetails{
		Server: upcloud.Server{
			UUID:         serverUUID,
			Hostname:     hostname,
			Plan:         "4xCPU-8GB",
			CoreNumber:   4,
			MemoryAmount: 8192,
			Zone:         "de-fra1",
		},
	}
	nc := buildNodeClaim(sd, "de-fra1")

	if nc.Status.ProviderID != providerPrefix+serverUUID {
		t.Errorf("unexpected providerID %q", nc.Status.ProviderID)
	}
	if got := strings.TrimPrefix(nc.Status.ProviderID, providerPrefix); got != serverUUID {
		t.Errorf("expected trimmed providerID %s, got %q", serverUUID, got)
	}
	if nc.Status.Capacity.Cpu().Value() != 4 {
		t.Errorf("expected 4 CPU, got %d", nc.Status.Capacity.Cpu().Value())
	}
	if nc.Status.Capacity.Memory().Value() != 8192*1024*1024 {
		t.Errorf("expected 8192 MiB in bytes, got %d", nc.Status.Capacity.Memory().Value())
	}
	if nc.Labels[corev1.LabelTopologyZone] != "de-fra1" {
		t.Errorf("expected zone label de-fra1")
	}
}

// ---- integration tests ----

func TestGetInstanceTypes(t *testing.T) {
	cp, _, _ := newTestProvider(t)
	its, err := cp.GetInstanceTypes(context.Background(), &karpv1.NodePool{})
	if err != nil {
		t.Fatalf("GetInstanceTypes error: %v", err)
	}
	if len(its) == 0 {
		t.Fatal("expected at least one instance type")
	}
	for _, it := range its {
		ct := it.Requirements.Get(karpv1.CapacityTypeLabelKey).Values()[0]
		if strings.Contains(it.Name, "SPOT") {
			if ct != karpv1.CapacityTypeSpot {
				t.Errorf("expected Spot capacity type for %s, got %s", it.Name, ct)
			}
		} else if ct != karpv1.CapacityTypeOnDemand {
			t.Errorf("expected OnDemand capacity type for %s, got %s", it.Name, ct)
		}
	}
}

func TestCreate(t *testing.T) {
	cp, fakeSrv, _ := newTestProvider(t)
	created, err := cp.Create(context.Background(), newTestNodeClaim())
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if !strings.HasPrefix(created.Status.ProviderID, providerPrefix) {
		t.Errorf("expected providerID with prefix, got %q", created.Status.ProviderID)
	}
	if created.Status.Capacity.Cpu().Value() != 4 {
		t.Errorf("expected 4 CPU from fake server, got %d", created.Status.Capacity.Cpu().Value())
	}
	// label merge: topology + nodeClass + nodeClaim labels
	if created.Labels["topology.kubernetes.io/zone"] != "de-fra1" {
		t.Errorf("expected topology zone label")
	}
	if created.Labels["team"] != "ai" {
		t.Errorf("expected nodeClass label team=ai")
	}
	if created.Labels["workload"] != "batch" {
		t.Errorf("expected nodeClaim label workload=batch")
	}
	// the created server must carry the managed label so List can find it
	if fakeSrv.lastReq == nil || fakeSrv.lastReq.Labels == nil {
		t.Fatal("expected labels on CreateServer request")
	}
	found := false
	for _, l := range *fakeSrv.lastReq.Labels {
		if l.Key == "karpenter.upcloud.com/managed" && l.Value == "true" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected managed=true label on created server")
	}
	// the NodeClaim must carry the NodeClass hash annotation for drift detection
	if created.Annotations[apisv1alpha1.NodeClassHashAnnotationKey] == "" {
		t.Errorf("expected NodeClass hash annotation on created NodeClaim")
	}
}

func TestCreateSelectsSpotPlan(t *testing.T) {
	cp, fakeSrv, _ := newTestProvider(t)

	nc := newTestNodeClaim()
	// Karpenter passes the selected instance type (the UpCloud plan) via the LabelInstanceTypeStable requirement. 
	// A plan name containing "SPOT" triggers spot capacity type.
	nc.Spec.Requirements = []karpv1.NodeSelectorRequirementWithMinValues{
		{
			Key:      corev1.LabelInstanceTypeStable,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{"GPU-SPOT-4xCPU-8GB"},
		},
	}

	created, err := cp.Create(context.Background(), nc)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if fakeSrv.lastReq == nil || fakeSrv.lastReq.Plan != "GPU-SPOT-4xCPU-8GB" {
		got := "<nil>"
		if fakeSrv.lastReq != nil {
			got = fakeSrv.lastReq.Plan
		}
		t.Errorf("expected spot plan launched, got %q", got)
	}
	if created.Labels[karpv1.CapacityTypeLabelKey] != karpv1.CapacityTypeSpot {
		t.Errorf("expected spot capacity-type label, got %q", created.Labels[karpv1.CapacityTypeLabelKey])
	}
}

func TestCreateUsesNodeClassPlanWhenNoInstanceTypeRequirement(t *testing.T) {
	cp, fakeSrv, _ := newTestProvider(t)

	nc := newTestNodeClaim()
	nc.Spec.NodeClassRef.Name = "default"
	// When Karpenter does not supply an instance-type requirement (LabelInstanceTypeStable),
	// Create falls back to the NodeClass plan. Capacity-type enforcement is handled by
	// Karpenter's scheduler, not the provider.
	stored := &apisv1alpha1.UpCloudNodeClass{}
	if err := cp.Client.Get(context.Background(), types.NamespacedName{Name: "default"}, stored); err != nil {
		t.Fatalf("getting nodeclass: %v", err)
	}
	stored.Spec.Plan = "CLOUDNATIVE-2xCPU-4GB"
	if err := cp.Client.Update(context.Background(), stored); err != nil {
		t.Fatalf("updating nodeclass: %v", err)
	}

	created, err := cp.Create(context.Background(), nc)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if fakeSrv.lastReq == nil || fakeSrv.lastReq.Plan != "CLOUDNATIVE-2xCPU-4GB" {
		got := "<nil>"
		if fakeSrv.lastReq != nil {
			got = fakeSrv.lastReq.Plan
		}
		t.Errorf("expected nodeclass plan launched, got %q", got)
	}
	if created.Labels[karpv1.CapacityTypeLabelKey] != karpv1.CapacityTypeOnDemand {
		t.Errorf("expected on-demand capacity-type label from nodeclass plan, got %q", created.Labels[karpv1.CapacityTypeLabelKey])
	}
}

func TestGetAndList(t *testing.T) {
	cp, _, _ := newTestProvider(t)
	created, err := cp.Create(context.Background(), newTestNodeClaim())
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	got, err := cp.Get(context.Background(), created.Status.ProviderID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Status.ProviderID != created.Status.ProviderID {
		t.Errorf("Get returned mismatched providerID")
	}

	list, err := cp.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(list) < 1 {
		t.Fatalf("expected at least one managed node from List")
	}
}

func TestIsDriftedNoDrift(t *testing.T) {
	cp, _, _ := newTestProvider(t)
	created, err := cp.Create(context.Background(), newTestNodeClaim())
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	reason, err := cp.IsDrifted(context.Background(), created)
	if err != nil {
		t.Fatalf("IsDrifted error: %v", err)
	}
	if reason != "" {
		t.Errorf("expected no drift when NodeClass unchanged, got %q", reason)
	}
}

func TestIsDriftedNodeClassChanged(t *testing.T) {
	cp, _, kubeClient := newTestProvider(t)
	created, err := cp.Create(context.Background(), newTestNodeClaim())
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	// Mutate the live NodeClass so its hash no longer matches the NodeClaim's annotation.
	nc := &apisv1alpha1.UpCloudNodeClass{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: "default"}, nc); err != nil {
		t.Fatalf("get nodeclass: %v", err)
	}
	nc.Spec.Zone = "fi-hel2"
	if err := kubeClient.Update(context.Background(), nc); err != nil {
		t.Fatalf("update nodeclass: %v", err)
	}

	reason, err := cp.IsDrifted(context.Background(), created)
	if err != nil {
		t.Fatalf("IsDrifted error: %v", err)
	}
	if reason != NodeClassDrifted {
		t.Errorf("expected NodeClassDrifted, got %q", reason)
	}
}

func TestIsDriftedNoAnnotation(t *testing.T) {
	cp, _, _ := newTestProvider(t)
	created, err := cp.Create(context.Background(), newTestNodeClaim())
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	// Simulate a node created before drift detection existed: no hash annotation.
	delete(created.Annotations, apisv1alpha1.NodeClassHashAnnotationKey)

	reason, err := cp.IsDrifted(context.Background(), created)
	if err != nil {
		t.Fatalf("IsDrifted error: %v", err)
	}
	if reason != "" {
		t.Errorf("expected no drift for annotation-less NodeClaim (avoid disrupting legacy nodes), got %q", reason)
	}
}

func TestRepairPolicies(t *testing.T) {
	cp, _, _ := newTestProvider(t)
	policies := cp.RepairPolicies()

	if len(policies) != 2 {
		t.Fatalf("expected 2 repair policies (NodeReady False/Unknown), got %d", len(policies))
	}

	for _, p := range policies {
		if p.ConditionType != corev1.NodeReady {
			t.Errorf("expected ConditionType NodeReady, got %q", p.ConditionType)
		}
		if p.TolerationDuration != 30*time.Minute {
			t.Errorf("expected toleration 30m, got %v", p.TolerationDuration)
		}
	}
	if policies[0].ConditionStatus != corev1.ConditionFalse {
		t.Errorf("expected first policy ConditionFalse, got %q", policies[0].ConditionStatus)
	}
	if policies[1].ConditionStatus != corev1.ConditionUnknown {
		t.Errorf("expected second policy ConditionUnknown, got %q", policies[1].ConditionStatus)
	}
}

// matchesPolicy mirrors the karpenter node.health controller's unhealthy-condition check:
// a node is unhealthy when it carries a condition matching one of the repair policies.
func matchesPolicy(node *corev1.Node, policies []karpentercloudprovider.RepairPolicy) bool {
	for _, policy := range policies {
		for _, c := range node.Status.Conditions {
			if c.Type == policy.ConditionType && c.Status == policy.ConditionStatus {
				return true
			}
		}
	}
	return false
}

func TestRepairPoliciesFlagsUnhealthyNode(t *testing.T) {
	cp, _, _ := newTestProvider(t)
	policies := cp.RepairPolicies()

	healthy := &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
	}}}
	if matchesPolicy(healthy, policies) {
		t.Error("expected healthy (Ready=True) node not flagged")
	}

	notReady := &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
	}}}
	if !matchesPolicy(notReady, policies) {
		t.Error("expected NotReady (False) node flagged for repair")
	}

	unknown := &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionUnknown},
	}}}
	if !matchesPolicy(unknown, policies) {
		t.Error("expected Unknown node flagged for repair")
	}
}

func TestDelete(t *testing.T) {
	cp, _, _ := newTestProvider(t)
	created, err := cp.Create(context.Background(), newTestNodeClaim())
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if err := cp.Delete(context.Background(), created); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	list, err := cp.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected server removed after delete, got %d", len(list))
	}
}
