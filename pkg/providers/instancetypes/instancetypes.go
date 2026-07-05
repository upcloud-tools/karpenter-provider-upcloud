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
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)



type Provider struct {
	svc          service.Cloud
	zone         string
	mu           sync.RWMutex
	instanceTypesByName map[string]*cloudprovider.InstanceType
	prices       map[string]float64
	lastFetch    time.Time
	cacheTTL     time.Duration
}

func NewProvider(svc service.Cloud, zone string) *Provider {
	return &Provider{
		svc:                 svc,
		zone:                zone,
		instanceTypesByName: make(map[string]*cloudprovider.InstanceType),
		prices:              make(map[string]float64),
		cacheTTL:            30 * time.Minute,
	}
}

func (p *Provider) List() []*cloudprovider.InstanceType {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return lo.Values(p.instanceTypesByName)
}

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

	built := make(map[string]*cloudprovider.InstanceType, len(plans.Plans))
	for _, plan := range plans.Plans {
		it := p.buildInstanceTypeWithPrices(plan, prices)
		if it != nil {
			built[plan.Name] = it
		}
	}

	p.mu.Lock()
	p.instanceTypesByName = built
	p.mu.Unlock()

	log.FromContext(ctx).Info("refreshed instance types", "count", len(built))
	return nil
}

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

func (p *Provider) buildInstanceType(plan upcloud.Plan) *cloudprovider.InstanceType {
	return p.buildInstanceTypeWithPrices(plan, p.prices)
}

func (p *Provider) buildInstanceTypeWithPrices(plan upcloud.Plan, prices map[string]float64) *cloudprovider.InstanceType {
	resources := corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewQuantity(int64(plan.CoreNumber), resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(int64(plan.MemoryAmount)*1024*1024, resource.BinarySI),
		corev1.ResourcePods:   *resource.NewQuantity(110, resource.DecimalSI),
	}

	price := math.MaxFloat64
	if p, ok := prices[plan.Name]; ok {
		price = p
	}
	offerings := cloudprovider.Offerings{
		{
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
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
		scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
	}

	return &cloudprovider.InstanceType{
		Name:         plan.Name,
		Requirements: scheduling.NewRequirements(reqs...),
		Offerings:    offerings,
		Capacity:     resources,
		Overhead:     &cloudprovider.InstanceTypeOverhead{},
	}
}
