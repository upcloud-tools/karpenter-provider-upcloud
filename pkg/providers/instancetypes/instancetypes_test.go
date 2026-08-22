package instancetypes

import (
	"context"
	"math"
	"testing"
	"strings"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	v1alpha2 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"
)

// fakeCloud implements service.Cloud with canned plans and prices for testing.
type fakeCloud struct {
	service.Cloud
	plans  *upcloud.Plans
	prices *upcloud.PricesByZone
}

func (f *fakeCloud) GetPlans(_ context.Context) (*upcloud.Plans, error) { return f.plans, nil }
func (f *fakeCloud) GetPricesByZone(_ context.Context) (*upcloud.PricesByZone, error) {
	return f.prices, nil
}

// ptrPlans returns two CloudNative plans for price-resolution tests.
func ptrPlans() *upcloud.Plans {
	return &upcloud.Plans{Plans: []upcloud.Plan{
		{Name: "CLOUDNATIVE-2xCPU-4GB", CoreNumber: 2, MemoryAmount: 4096, StorageSize: 0, StorageTier: ""},
		{Name: "CLOUDNATIVE-4xCPU-8GB", CoreNumber: 4, MemoryAmount: 8192, StorageSize: 0, StorageTier: ""},
	}}
}

// ptrPrices returns zone-level prices for two CloudNative plans (one bare, one with server_plan_ prefix).
func ptrPrices(zone string) *upcloud.PricesByZone {
	p := upcloud.PricesByZone{}
	p[zone] = map[string]upcloud.Price{
		"CLOUDNATIVE-2xCPU-4GB":             {Price: 0.05},
		"server_plan_CLOUDNATIVE-4xCPU-8GB": {Price: 0.10},
	}
	return &p
}

// TestBuildInstanceTypeWithPrices verifies that a plan is converted to an InstanceType with correct CPU, memory, pods, zone, 
// architecture, capacity type, and price.
func TestBuildInstanceTypeWithPrices(t *testing.T) {
	p := NewProvider(nil, "de-fra1")
	plan := upcloud.Plan{Name: "2xCPU-4GB", CoreNumber: 2, MemoryAmount: 4096}

	it := p.buildInstanceTypeWithPrices(plan, map[string]float64{"2xCPU-4GB": 0.05})

	if it.Name != "2xCPU-4GB" {
		t.Errorf("expected name 2xCPU-4GB, got %s", it.Name)
	}
	if got := it.Capacity.Cpu().Value(); got != 2 {
		t.Errorf("expected 2 CPU, got %d", got)
	}
	if got := it.Capacity.Memory().Value(); got != 4096*1024*1024 {
		t.Errorf("expected memory 4096 MiB in bytes, got %d", got)
	}
	if got := it.Capacity.Pods().Value(); got != 110 {
		t.Errorf("expected 110 pods, got %d", got)
	}
	if len(it.Offerings) != 1 {
		t.Fatalf("expected 1 offering, got %d", len(it.Offerings))
	}
	if it.Offerings[0].Price != 0.05 {
		t.Errorf("expected price 0.05, got %f", it.Offerings[0].Price)
	}
	if !it.Offerings[0].Available {
		t.Errorf("expected offering to be available")
	}
	if it.Requirements.Get(corev1.LabelArchStable).Values()[0] != "amd64" {
		t.Errorf("expected arch amd64 requirement")
	}
	if it.Requirements.Get(corev1.LabelTopologyZone).Values()[0] != "de-fra1" {
		t.Errorf("expected zone de-fra1 requirement")
	}
	if it.Requirements.Get(karpv1.CapacityTypeLabelKey).Values()[0] != karpv1.CapacityTypeOnDemand {
		t.Errorf("expected OnDemand capacity type requirement")
	}
}

// TestBuildInstanceTypeGPUCapacity verifies that nvidia.com/gpu capacity is advertised for GPU plans.
func TestBuildInstanceTypeGPUCapacity(t *testing.T) {
	p := NewProvider(nil, "de-fra1")
	plan := upcloud.Plan{Name: "GPU-8xCPU-64GB-1xL4", CoreNumber: 8, MemoryAmount: 65536, GPUAmount: 1, GPUModel: "NVIDIA L4"}

	it := p.buildInstanceTypeWithPrices(plan, map[string]float64{"GPU-8xCPU-64GB-1xL4": 2.5})

	if got := it.Capacity[v1alpha2.ResourceNvidiaGPU]; got.Value() != 1 {
		t.Errorf("expected nvidia.com/gpu capacity 1, got %d", got.Value())
	}
}

// TestBuildInstanceTypeNoGPUCapacityForNonGPUPlan verifies non-GPU plans do not advertise nvidia.com/gpu.
func TestBuildInstanceTypeNoGPUCapacityForNonGPUPlan(t *testing.T) {
	p := NewProvider(nil, "de-fra1")
	plan := upcloud.Plan{Name: "CLOUDNATIVE-2xCPU-4GB", CoreNumber: 2, MemoryAmount: 4096}

	it := p.buildInstanceTypeWithPrices(plan, map[string]float64{"CLOUDNATIVE-2xCPU-4GB": 0.05})

	if _, ok := it.Capacity[v1alpha2.ResourceNvidiaGPU]; ok {
		t.Errorf("expected no nvidia.com/gpu capacity on non-GPU plan")
	}
}

// TestRefreshSurfacesSpotAsSeparateInstanceType verifies that a spot plan variant is surfaced as its own
// InstanceType with a spot capacity-type offering, separate from the on-demand version.
func TestRefreshSurfacesSpotAsSeparateInstanceType(t *testing.T) {
	plans := &upcloud.Plans{Plans: []upcloud.Plan{
		{Name: "GPU-8xCPU-64GB-1xL4", CoreNumber: 8, MemoryAmount: 65536, GPUAmount: 1, GPUModel: "NVIDIA L4"},
		{Name: "GPU-SPOT-8xCPU-64GB-1xL4", CoreNumber: 8, MemoryAmount: 65536, GPUAmount: 1, GPUModel: "NVIDIA L4"},
	}}
	p := NewProvider(&fakeCloud{plans: plans, prices: &upcloud.PricesByZone{"de-fra1": {}}}, "de-fra1")
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}

	// Each plan is surfaced as a separate instance type with its own capacity-type offering.
	if len(p.List()) != 2 {
		t.Fatalf("expected 2 instance types (on-demand + spot), got %d", len(p.List()))
	}
	for _, it := range p.List() {
		if len(it.Offerings) != 1 {
			t.Fatalf("expected 1 offering per instance type, got %d for %s", len(it.Offerings), it.Name)
		}
		capacityType := it.Offerings[0].Requirements.Get(karpv1.CapacityTypeLabelKey).Values()[0]
		if strings.Contains(it.Name, "SPOT") {
			if capacityType != karpv1.CapacityTypeSpot {
				t.Errorf("expected spot capacity type for %s, got %s", it.Name, capacityType)
			}
		} else {
			if capacityType != karpv1.CapacityTypeOnDemand {
				t.Errorf("expected on-demand capacity type for %s, got %s", it.Name, capacityType)
			}
		}
	}
}

// TestBuildInstanceTypePriceFallback verifies that plans without pricing data use MaxFloat64 as the price.
func TestBuildInstanceTypePriceFallback(t *testing.T) {
	p := NewProvider(nil, "de-fra1")
	plan := upcloud.Plan{Name: "orphan-plan", CoreNumber: 1, MemoryAmount: 1024}

	it := p.buildInstanceTypeWithPrices(plan, map[string]float64{})
	if it.Offerings[0].Price != math.MaxFloat64 {
		t.Errorf("expected MaxFloat64 price for unpriced plan, got %f", it.Offerings[0].Price)
	}
}

// TestRefreshPopulatesInstanceTypes verifies that Refresh fetches plans and prices and caches them as InstanceTypes,
// including resolving server_plan_ prefixed pricing keys.
func TestRefreshPopulatesInstanceTypes(t *testing.T) {
	p := NewProvider(&fakeCloud{plans: ptrPlans(), prices: ptrPrices("de-fra1")}, "de-fra1")
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}

	its := p.List()
	if len(its) != 2 {
		t.Fatalf("expected 2 instance types, got %d", len(its))
	}

	byName := map[string]bool{}
	for _, it := range its {
		byName[it.Name] = true
	}
	if !byName["CLOUDNATIVE-2xCPU-4GB"] || !byName["CLOUDNATIVE-4xCPU-8GB"] {
		t.Errorf("expected both plan names present, got %v", byName)
	}

	// CLOUDNATIVE-2xCPU-4GB is priced directly; CLOUDNATIVE-4xCPU-8GB only via the server_plan_ prefix.
	two := findInstanceType(its, "CLOUDNATIVE-2xCPU-4GB")
	four := findInstanceType(its, "CLOUDNATIVE-4xCPU-8GB")
	if two == nil || two.Offerings[0].Price != 0.05 {
		t.Errorf("expected CLOUDNATIVE-2xCPU-4GB price 0.05, got %v", two)
	}
	if four == nil || four.Offerings[0].Price != 0.10 {
		t.Errorf("expected CLOUDNATIVE-4xCPU-8GB price 0.10 (server_plan_ prefix), got %v", four)
	}
}

// mixedPlans returns one plan from each relevant family (CloudNative, GPU, Starter, Premium) to exercise the scope filter.
func mixedPlans() *upcloud.Plans {
	return &upcloud.Plans{Plans: []upcloud.Plan{
		{Name: "CLOUDNATIVE-2xCPU-4GB", CoreNumber: 2, MemoryAmount: 4096, StorageSize: 0},
		{Name: "GPU-8xCPU-64GB-1xL4", CoreNumber: 8, MemoryAmount: 65536, StorageSize: 0, GPUAmount: 1, GPUModel: "NVIDIA L4"},
		{Name: "STARTER-1xCPU-2GB", CoreNumber: 1, MemoryAmount: 2048, StorageSize: 20, StorageTier: "standard"},
		{Name: "PREMIUM-2xCPU-2GB", CoreNumber: 2, MemoryAmount: 2048, StorageSize: 50, StorageTier: "maxiops"},
	}}
}

// names extracts a set of instance type names for easy assertion in scope tests.
func names(its []*cloudprovider.InstanceType) map[string]bool {
	out := map[string]bool{}
	for _, it := range its {
		out[it.Name] = true
	}
	return out
}

// TestRefreshIncludesAllPlans verifies that all plans from the API are included as instance types.
func TestRefreshIncludesAllPlans(t *testing.T) {
	p := NewProvider(&fakeCloud{plans: mixedPlans(), prices: &upcloud.PricesByZone{"de-fra1": {}}}, "de-fra1")
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	got := names(p.List())
	if len(got) != 4 {
		t.Fatalf("expected all 4 plans included, got %v", got)
	}
	if !got["CLOUDNATIVE-2xCPU-4GB"] || !got["GPU-8xCPU-64GB-1xL4"] || !got["STARTER-1xCPU-2GB"] || !got["PREMIUM-2xCPU-2GB"] {
		t.Errorf("expected all plans included, got %v", got)
	}
}

// findInstanceType looks up an instance type by name from a slice.
func findInstanceType(its []*cloudprovider.InstanceType, name string) *cloudprovider.InstanceType {
	for _, it := range its {
		if it.Name == name {
			return it
		}
	}
	return nil
}

// TestInstanceTypeLabels verifies that UpCloud-specific instance type labels are set correctly.
func TestInstanceTypeLabels(t *testing.T) {
	p := NewProvider(nil, "de-fra1")

	tests := []struct {
		name     string
		plan     upcloud.Plan
		expected map[string]string
		absent   []string
	}{
		{
			name: "cloudnative plan",
			plan: upcloud.Plan{Name: "CLOUDNATIVE-2xCPU-4GB", CoreNumber: 2, MemoryAmount: 4096, StorageSize: 0},
			expected: map[string]string{
				v1alpha2.LabelInstanceFamily:      "CLOUDNATIVE",
				v1alpha2.LabelInstanceCPU:         "2",
				v1alpha2.LabelInstanceMemory:      "4096",
				v1alpha2.LabelInstanceStorageSize: "0",
			},
			absent: []string{v1alpha2.LabelInstanceGPUCount, v1alpha2.LabelInstanceGPUModel},
		},
		{
			name: "gpu plan",
			plan: upcloud.Plan{Name: "GPU-8xCPU-64GB-1xL4", CoreNumber: 8, MemoryAmount: 65536, StorageSize: 50, GPUAmount: 1, GPUModel: "NVIDIA L4"},
			expected: map[string]string{
				v1alpha2.LabelInstanceFamily:      "GPU",
				v1alpha2.LabelInstanceCPU:         "8",
				v1alpha2.LabelInstanceMemory:      "65536",
				v1alpha2.LabelInstanceStorageSize: "50",
				v1alpha2.LabelInstanceGPUCount:    "1",
				v1alpha2.LabelInstanceGPUModel:    "NVIDIA L4",
			},
		},
		{
			name: "starter plan",
			plan: upcloud.Plan{Name: "STARTER-1xCPU-2GB", CoreNumber: 1, MemoryAmount: 2048, StorageSize: 20},
			expected: map[string]string{
				v1alpha2.LabelInstanceFamily:      "STARTER",
				v1alpha2.LabelInstanceCPU:         "1",
				v1alpha2.LabelInstanceMemory:      "2048",
				v1alpha2.LabelInstanceStorageSize: "20",
			},
			absent: []string{v1alpha2.LabelInstanceGPUCount, v1alpha2.LabelInstanceGPUModel},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := p.buildInstanceTypeWithPrices(tt.plan, map[string]float64{tt.plan.Name: 0.1})

			for label, want := range tt.expected {
				got := it.Requirements.Get(label)
				if got == nil {
					t.Errorf("expected label %s to be set", label)
					continue
				}
				values := got.Values()
				if len(values) != 1 || values[0] != want {
					t.Errorf("label %s: expected %q, got %v", label, want, values)
				}
			}

			for _, label := range tt.absent {
				req := it.Requirements.Get(label)
				if req != nil && len(req.Values()) > 0 {
					t.Errorf("expected label %s to be absent, but it was set to %v", label, req.Values())
				}
			}
		})
	}
}

// TestInstanceFamily verifies the instanceFamily helper extracts the correct prefix.
func TestInstanceFamily(t *testing.T) {
	tests := []struct {
		planName string
		expected string
	}{
		{"CLOUDNATIVE-2xCPU-4GB", "CLOUDNATIVE"},
		{"GPU-8xCPU-64GB-1xL4", "GPU"},
		{"GPU-SPOT-8xCPU-64GB-1xL4", "GPU"},
		{"STARTER-1xCPU-2GB", "STARTER"},
		{"PREMIUM-2xCPU-4GB", "PREMIUM"},
		{"UNKNOWN-PLAN", "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.planName, func(t *testing.T) {
			got := instanceFamily(tt.planName)
			if got != tt.expected {
				t.Errorf("instanceFamily(%q) = %q, want %q", tt.planName, got, tt.expected)
			}
		})
	}
}
