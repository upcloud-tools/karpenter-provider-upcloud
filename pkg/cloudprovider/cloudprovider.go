package cloudprovider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/awslabs/operatorpkg/status"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	v1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instance"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instancetypes"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/userdata"
)

const providerPrefix = "upcloud:////"

// defaultStorageGB is the root disk size used when UpCloudNodeClass.Spec.StorageGB is unset.
const defaultStorageGB = 20

// NodeClassDrifted is returned by IsDrifted when the live UpCloudNodeClass no longer matches
// the configuration a NodeClaim was provisioned against.
const NodeClassDrifted cloudprovider.DriftReason = "NodeClassDrifted"

type UpCloudCloudProvider struct {
	client.Client
	kubernetesInterface  kubernetes.Interface
	instanceProvider     *instance.Provider
	userDataProvider     *userdata.Provider
	instanceTypeProvider *instancetypes.Provider
	zone                 string
	clusterEndpoint      string
	repairToleration     time.Duration
}

func NewCloudProvider(
	kubeClient client.Client,
	kubeInterface kubernetes.Interface,
	instanceProvider *instance.Provider,
	userDataProvider *userdata.Provider,
	instanceTypeProvider *instancetypes.Provider,
	zone string,
	clusterEndpoint string,
	repairToleration time.Duration,
) *UpCloudCloudProvider {
	return &UpCloudCloudProvider{
		Client:               kubeClient,
		kubernetesInterface:  kubeInterface,
		instanceProvider:     instanceProvider,
		userDataProvider:     userDataProvider,
		instanceTypeProvider: instanceTypeProvider,
		zone:                 zone,
		clusterEndpoint:      clusterEndpoint,
		repairToleration:     repairToleration,
	}
}

func (p *UpCloudCloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	nodeClass, err := p.resolveNodeClass(ctx, nodeClaim)
	if err != nil {
		return nil, fmt.Errorf("resolving node class: %w", err)
	}

	serverName := fmt.Sprintf("karpenter-%s", strings.ReplaceAll(string(uuid.NewUUID()), "-", "")[:16])

	// Karpenter records the selected instance type (the UpCloud plan) on the NodeClaim's instance-type requirement. 
	// Use that when present to honour spot plans; fall back to NodeClass otherwise.
	plan := nodeClass.Spec.Plan
	if req := instanceTypeRequirement(nodeClaim); req != nil && len(req.Values) > 0 {
		plan = req.Values[0]
	}
	capacityType := karpv1.CapacityTypeOnDemand
	if isSpotPlan(plan) {
		capacityType = karpv1.CapacityTypeSpot
	}

	caCertPEM, err := getCABundle(ctx, p.Client)
	if err != nil {
		return nil, fmt.Errorf("getting CA bundle: %w", err)
	}

	certs, err := generateKubeletCert(ctx, p.kubernetesInterface, serverName)
	if err != nil {
		return nil, fmt.Errorf("generating kubelet certificate: %w", err)
	}

	// Merge labels: topology (from zone) + instance-type info + nodeClass.Spec.Labels + nodeClaim.Labels
	labels := lo.Assign(
		map[string]string{
			"topology.kubernetes.io/region":            p.zone,
			"topology.kubernetes.io/zone":              p.zone,
			"failure-domain.beta.kubernetes.io/region": p.zone,
			"failure-domain.beta.kubernetes.io/zone":   p.zone,
			corev1.LabelInstanceTypeStable:             plan,
			corev1.LabelArchStable:                     "amd64",
			corev1.LabelOSStable:                       "linux",
			karpv1.CapacityTypeLabelKey:                capacityType,
		},
		nodeClass.Spec.Labels,
		nodeClaim.Labels,
	)

	// Merge taints: uninitialized (baseline) + nodeClass.Spec.Taints + nodeClaim.Spec.Taints
	taints := []corev1.Taint{karpv1.UnregisteredNoExecuteTaint}
	taints = append(taints, lo.Map(nodeClass.Spec.Taints, func(t upcloud.KubernetesTaint, _ int) corev1.Taint {
		return corev1.Taint{Key: t.Key, Value: t.Value, Effect: corev1.TaintEffect(t.Effect)}
	})...)
	taints = append(taints, nodeClaim.Spec.Taints...)

	userData, err := p.userDataProvider.Generate(&userdata.Options{
		ClusterEndpoint:   p.clusterEndpoint,
		CACertPEM:         caCertPEM,
		KubeletClientCert: certs.ClientCertPEM,
		KubeletClientKey:  certs.ClientKeyPEM,
		Labels:            labels,
		Taints:            taints,
	})
	if err != nil {
		return nil, fmt.Errorf("generating userdata: %w", err)
	}

	storageGB := nodeClass.Spec.StorageGB
	if storageGB <= 0 {
		storageGB = defaultStorageGB
	}
	storageTier := string(nodeClass.Spec.StorageTier)
	if storageTier == "" {
		storageTier = string(upcloud.StorageTierStandard)
	}

	server, err := p.instanceProvider.Create(ctx, serverName, plan, p.zone, userData, labels, storageGB, storageTier)
	if err != nil {
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("creating server: %w", err))
	}

	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewQuantity(int64(server.CoreNumber), resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(int64(server.MemoryAmount)*1024*1024, resource.BinarySI),
		corev1.ResourcePods:   *resource.NewQuantity(110, resource.DecimalSI),
	}
	allocatable := capacity.DeepCopy()

	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   serverName,
			Labels: labels,
			Annotations: map[string]string{
				v1alpha1.NodeClassHashAnnotationKey: nodeClass.Hash(),
			},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Name: nodeClaim.Spec.NodeClassRef.Name,
			},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID:  providerPrefix + server.UUID,
			NodeName:    server.Hostname,
			Capacity:    capacity,
			Allocatable: allocatable,
		},
	}, nil
}

func (p *UpCloudCloudProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	serverUUID := strings.TrimPrefix(nodeClaim.Status.ProviderID, providerPrefix)

	// UpCloud requires servers to be stopped before deletion
	if err := p.instanceProvider.Stop(ctx, serverUUID); err != nil {
		if strings.Contains(err.Error(), "SERVER_NOT_FOUND") {
			return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("server not found: %w", err))
		}
		// SERVER_STATE_ILLEGAL on stop means already stopped → proceed to delete
		if !strings.Contains(err.Error(), "SERVER_STATE_ILLEGAL") {
			return err
		}
	} else {
		// Wait for the server to reach stopped state
		if err := p.instanceProvider.WaitForStop(ctx, serverUUID); err != nil {
			return fmt.Errorf("waiting for server to stop: %w", err)
		}
	}

	err := p.instanceProvider.Delete(ctx, serverUUID)
	if err != nil {
		if strings.Contains(err.Error(), "SERVER_NOT_FOUND") {
			return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("server not found: %w", err))
		}
		return err
	}
	return nil
}

func (p *UpCloudCloudProvider) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	serverUUID := strings.TrimPrefix(providerID, providerPrefix)
	server, err := p.instanceProvider.Get(ctx, serverUUID)
	if err != nil {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("getting server: %w", err))
	}

	return buildNodeClaim(*server, p.zone), nil
}

func (p *UpCloudCloudProvider) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	servers, err := p.instanceProvider.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}

	return lo.Map(servers, func(s upcloud.ServerDetails, _ int) *karpv1.NodeClaim {
		return buildNodeClaim(s, p.zone)
	}), nil
}

func (p *UpCloudCloudProvider) GetInstanceTypes(ctx context.Context, nodePool *karpv1.NodePool) ([]*cloudprovider.InstanceType, error) {
	return p.instanceTypeProvider.List(), nil
}

func (p *UpCloudCloudProvider) IsDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim) (cloudprovider.DriftReason, error) {
	nodeClass, err := p.resolveNodeClass(ctx, nodeClaim)
	if err != nil {
		return "", cloudprovider.IgnoreNodeClaimNotFoundError(fmt.Errorf("resolving node class: %w", err))
	}

	// Nodes created before drift detection existed carry no hash annotation; don't disrupt them.
	stored := nodeClaim.Annotations[v1alpha1.NodeClassHashAnnotationKey]
	if stored == "" {
		return "", nil
	}

	if stored != nodeClass.Hash() {
		return NodeClassDrifted, nil
	}
	return "", nil
}

func (p *UpCloudCloudProvider) Name() string {
	return "upcloud"
}

func buildNodeClaim(server upcloud.ServerDetails, zone string) *karpv1.NodeClaim {
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewQuantity(int64(server.CoreNumber), resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(int64(server.MemoryAmount)*1024*1024, resource.BinarySI),
		corev1.ResourcePods:   *resource.NewQuantity(110, resource.DecimalSI),
	}
	allocatable := capacity.DeepCopy()

	capacityType := karpv1.CapacityTypeOnDemand
	for _, l := range server.Labels {
		if l.Key == karpv1.CapacityTypeLabelKey {
			capacityType = l.Value
		}
	}

	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				corev1.LabelInstanceTypeStable: server.Plan,
				corev1.LabelArchStable:         "amd64",
				corev1.LabelOSStable:           "linux",
				corev1.LabelTopologyZone:       zone,
				karpv1.CapacityTypeLabelKey:    capacityType,
			},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID:  providerPrefix + server.UUID,
			NodeName:    server.Hostname,
			Capacity:    capacity,
			Allocatable: allocatable,
		},
	}
}

func (p *UpCloudCloudProvider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{
		&v1alpha1.UpCloudNodeClass{},
	}
}

// RepairPolicies tells Karpenter's node.health controller which Node conditions mark a node as unhealthy and how long to tolerate 
// them before force-terminating and replacing the node. We watch the standard Ready condition: a node that is NotReady (False) or
// Unknown (kubelet stopped reporting) for longer than repairToleration is recycled. Node termination (deletion timestamp set but not yet 
// removed) is handled separately by Karpenter's built-in node.termination controller, so it is not listed here.
func (p *UpCloudCloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return []cloudprovider.RepairPolicy{
		{
			ConditionType:   corev1.NodeReady,
			ConditionStatus: corev1.ConditionFalse,
			TolerationDuration: p.repairToleration,
		},
		{
			ConditionType:   corev1.NodeReady,
			ConditionStatus: corev1.ConditionUnknown,
			TolerationDuration: p.repairToleration,
		},
	}
}

// instanceTypeRequirement returns the instance-type requirement from the NodeClaim, if any.
func instanceTypeRequirement(nc *karpv1.NodeClaim) *karpv1.NodeSelectorRequirementWithMinValues {
	for i, r := range nc.Spec.Requirements {
		if r.Key == corev1.LabelInstanceTypeStable {
			return &nc.Spec.Requirements[i]
		}
	}
	return nil
}

// isSpotPlan reports whether a plan name denotes a spot variant.
func isSpotPlan(name string) bool {
	return strings.Contains(strings.ToUpper(name), "SPOT")
}

func (p *UpCloudCloudProvider) resolveNodeClass(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*v1alpha1.UpCloudNodeClass, error) {
	nc := &v1alpha1.UpCloudNodeClass{}
	if err := p.Client.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nc); err != nil {
		return nil, err
	}
	return nc, nil
}
