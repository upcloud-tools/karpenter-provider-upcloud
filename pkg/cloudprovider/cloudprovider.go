package cloudprovider

import (
	"context"
	"fmt"
	"strings"

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
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	v1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instance"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instancetypes"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/userdata"
)

const providerPrefix = "upcloud:////"

type UpCloudCloudProvider struct {
	client.Client
	kubernetesInterface kubernetes.Interface
	instanceProvider     *instance.Provider
	userDataProvider     *userdata.Provider
	instanceTypeProvider *instancetypes.Provider
	zone                 string
	clusterEndpoint      string
}

func NewCloudProvider(
	kubeClient client.Client,
	kubeInterface kubernetes.Interface,
	instanceProvider *instance.Provider,
	userDataProvider *userdata.Provider,
	instanceTypeProvider *instancetypes.Provider,
	zone string,
	clusterEndpoint string,
) *UpCloudCloudProvider {
	return &UpCloudCloudProvider{
		Client:               kubeClient,
		kubernetesInterface: kubeInterface,
		instanceProvider:     instanceProvider,
		userDataProvider:     userDataProvider,
		instanceTypeProvider: instanceTypeProvider,
		zone:                 zone,
		clusterEndpoint:      clusterEndpoint,
	}
}

func (p *UpCloudCloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	nodeClass, err := p.resolveNodeClass(ctx, nodeClaim)
	if err != nil {
		return nil, fmt.Errorf("resolving node class: %w", err)
	}

	serverName := fmt.Sprintf("karpenter-%s", strings.ReplaceAll(string(uuid.NewUUID()), "-", "")[:16])

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
			"topology.kubernetes.io/region":              p.zone,
			"topology.kubernetes.io/zone":                p.zone,
			"failure-domain.beta.kubernetes.io/region":   p.zone,
			"failure-domain.beta.kubernetes.io/zone":     p.zone,
			corev1.LabelInstanceTypeStable:               nodeClass.Spec.Plan,
			corev1.LabelArchStable:                       "amd64",
			corev1.LabelOSStable:                         "linux",
			karpv1.CapacityTypeLabelKey:                  karpv1.CapacityTypeOnDemand,
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
		ClusterEndpoint:    p.clusterEndpoint,
		CACertPEM:          caCertPEM,
		KubeletClientCert:  certs.ClientCertPEM,
		KubeletClientKey:   certs.ClientKeyPEM,
		Labels:             labels,
		Taints:             taints,
	})
	if err != nil {
		return nil, fmt.Errorf("generating userdata: %w", err)
	}

	server, err := p.instanceProvider.Create(ctx, serverName, nodeClass.Spec.Plan, p.zone, userData, labels)
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

	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				corev1.LabelInstanceTypeStable: server.Plan,
				corev1.LabelArchStable:         "amd64",
				corev1.LabelOSStable:           "linux",
				corev1.LabelTopologyZone:       zone,
				karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand,
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

func (p *UpCloudCloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return nil
}

func (p *UpCloudCloudProvider) resolveNodeClass(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*v1alpha1.UpCloudNodeClass, error) {
	nc := &v1alpha1.UpCloudNodeClass{}
	if err := 	p.Client.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nc); err != nil {
		return nil, err
	}
	return nc, nil
}
