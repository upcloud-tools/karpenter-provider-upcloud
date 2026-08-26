package instancetypes

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1alpha2 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/util"
)

// Provider caches UpCloud plans as Karpenter InstanceTypes, refreshed periodically from the UpCloud API.
type Provider struct {
	svc                 service.Cloud
	zone                string
	mu                  sync.RWMutex
	instanceTypesByName map[string]*cloudprovider.InstanceType
	plansByName         map[string]upcloud.Plan
	prices              map[string]float64
	lastFetch           time.Time
	cacheTTL            time.Duration
}

// NewProvider creates a Provider with a 30-minute price cache TTL. Call Refresh before first use.
func NewProvider(svc service.Cloud, zone string) *Provider {
	return &Provider{
		svc:                 svc,
		zone:                zone,
		instanceTypesByName: make(map[string]*cloudprovider.InstanceType),
		plansByName:         make(map[string]upcloud.Plan),
		prices:              make(map[string]float64),
		cacheTTL:            30 * time.Minute,
	}
}

// List returns all cached instance types. The list is empty until Refresh has been called at least once.
func (p *Provider) List() []*cloudprovider.InstanceType {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return lo.Values(p.instanceTypesByName)
}

// Get returns the cached UpCloud plan by name. Returns the plan and true if found, or a zero-value plan and false otherwise.
func (p *Provider) Get(name string) (upcloud.Plan, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	plan, ok := p.plansByName[name]
	return plan, ok
}

// Refresh fetches all plans and prices from the UpCloud API and caches each as a separate InstanceType.
// Spot plans are surfaced with a spot capacity-type offering; all others get on-demand.
func (p *Provider) Refresh(ctx context.Context) error {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("provider", "instancetypes"))

	plans, err := p.svc.GetPlans(ctx)
	if err != nil {
		return fmt.Errorf("fetching plans: %w", err)
	}

	if err := p.refreshPrices(ctx); err != nil {
		log.FromContext(ctx).Error(err, "failed to refresh prices, using cached values")
	}

	p.mu.RLock()
	pricing := make(map[string]float64, len(p.prices))
	for k, v := range p.prices {
		pricing[k] = v
	}
	p.mu.RUnlock()

	prices := pricing

	// Each plan (on-demand and spot) is surfaced as its own instance type. Spot plans are distinguished from on-demand by their
	// capacity-type offering, which Karpenter selects via the karpenter.sh/capacity-type requirement.
	built := make(map[string]*cloudprovider.InstanceType, len(plans.Plans))
	rawPlans := make(map[string]upcloud.Plan, len(plans.Plans))
	for _, plan := range plans.Plans {
		it := p.buildInstanceTypeWithPrices(plan, prices)
		if it != nil {
			built[plan.Name] = it
		}
		rawPlans[plan.Name] = plan
	}

	p.mu.Lock()
	p.instanceTypesByName = built
	p.plansByName = rawPlans
	p.mu.Unlock()

	log.FromContext(ctx).Info("refreshed instance types", "count", len(built))
	return nil
}

// refreshPrices fetches zone-level pricing from the API and caches it. The cache is refreshed at
// most once per cacheTTL to avoid excessive API calls during instance-type reconciliation.
func (p *Provider) refreshPrices(ctx context.Context) error {
	if time.Since(p.lastFetch) < p.cacheTTL {
		return nil
	}

	pricesByZone, err := p.svc.GetPricesByZone(ctx)
	if err != nil {
		return fmt.Errorf("fetching prices: %w", err)
	}

	zonePrices, ok := (*pricesByZone)[p.zone]
	if !ok {
		// Fall back to first available zone
		for _, zp := range *pricesByZone {
			zonePrices = zp
			break
		}
	}

	prices := make(map[string]float64, len(zonePrices))
	for itemName, price := range zonePrices {
		// Pricing items may be keyed as "server_plan_2xCPU-4GB" or directly as "2xCPU-4GB"
		name := strings.TrimPrefix(itemName, "server_plan_")
		prices[name] = price.Price
		if name == itemName {
			// Try the prefixed variant as well
			prices["server_plan_"+name] = price.Price
		}
	}

	p.mu.Lock()
	p.prices = prices
	p.lastFetch = time.Now()
	p.mu.Unlock()

	// Warn about plans missing from pricing data
	for _, it := range p.instanceTypesByName {
		if _, ok := prices[it.Name]; !ok {
			log.FromContext(ctx).V(1).Info("no pricing data for plan", "plan", it.Name)
		}
	}

	return nil
}

// buildInstanceTypeWithPrices converts an UpCloud plan into a Karpenter InstanceType with CPU, memory, pods, optional GPU, zone, 
// capacity-type offerings, and pricing. Spot plans get a spot capacity-type offering; all others get on-demand.
func (p *Provider) buildInstanceTypeWithPrices(plan upcloud.Plan, prices map[string]float64) *cloudprovider.InstanceType {
	resources := corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewQuantity(int64(plan.CoreNumber), resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(int64(plan.MemoryAmount)*1024*1024, resource.BinarySI),
		corev1.ResourcePods:   *resource.NewQuantity(110, resource.DecimalSI),
	}
	// Surface GPU capacity so pods requesting nvidia.com/gpu can be scheduled.
	// Karpenter treats any dot-namespaced resource as an accelerator automatically.
	if plan.GPUAmount > 0 {
		resources[v1alpha2.ResourceNvidiaGPU] = *resource.NewQuantity(int64(plan.GPUAmount), resource.DecimalSI)
	}

	price := math.MaxFloat64
	if p, ok := prices[plan.Name]; ok {
		price = p
	}

	// Each plan is its own instance type. A spot plan (name contains "SPOT") gets a spot offering; otherwise on-demand.
	// Karpenter selects between them via the capacity-type requirement, and passes the chosen plan name back through the NodeClaim.
	capacityType := karpv1.CapacityTypeOnDemand
	if util.IsSpotPlan(plan.Name) {
		capacityType = karpv1.CapacityTypeSpot
	}
	offerings := cloudprovider.Offerings{
		{
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, capacityType),
			),
			Price:     price,
			Available: true,
		},
	}

	reqs := []*scheduling.Requirement{
		scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, plan.Name),
		scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
		scheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, string(corev1.Linux)),
		scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, p.zone),
		scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, capacityType),
		scheduling.NewRequirement(v1alpha2.LabelInstanceFamily, corev1.NodeSelectorOpIn, util.InstanceFamily(plan.Name)),
		scheduling.NewRequirement(v1alpha2.LabelInstanceCPU, corev1.NodeSelectorOpIn, fmt.Sprintf("%d", plan.CoreNumber)),
		scheduling.NewRequirement(v1alpha2.LabelInstanceMemory, corev1.NodeSelectorOpIn, fmt.Sprintf("%d", plan.MemoryAmount)),
		scheduling.NewRequirement(v1alpha2.LabelInstanceStorageSize, corev1.NodeSelectorOpIn, fmt.Sprintf("%d", plan.StorageSize)),
	}
	if plan.GPUAmount > 0 {
		reqs = append(reqs,
			scheduling.NewRequirement(v1alpha2.LabelInstanceGPUCount, corev1.NodeSelectorOpIn, fmt.Sprintf("%d", plan.GPUAmount)),
			scheduling.NewRequirement(v1alpha2.LabelInstanceGPUModel, corev1.NodeSelectorOpIn, plan.GPUModel),
		)
	}

	return &cloudprovider.InstanceType{
		Name:         plan.Name,
		Requirements: scheduling.NewRequirements(reqs...),
		Offerings:    offerings,
		Capacity:     resources,
		Overhead:     &cloudprovider.InstanceTypeOverhead{},
	}
}


