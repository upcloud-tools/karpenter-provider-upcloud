package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/client"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
	v1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/cloudprovider"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instance"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instancetypes"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/userdata"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// TestLiveInstanceTypes queries the UpCloud API for available instance types in the cluster's zone and verifies that at least
// one known plan (CLOUDNATIVE-2xCPU-4GB) and spot offerings (if any) are present.
func TestLiveInstanceTypes(t *testing.T) {
	token := os.Getenv("UPCLOUD_TOKEN")
	clusterID := os.Getenv("UPCLOUD_KUBERNETES_CLUSTER_ID")
	if token == "" || clusterID == "" {
		t.Skip("skipping live e2e: set UPCLOUD_TOKEN and UPCLOUD_KUBERNETES_CLUSTER_ID")
	}

	svc := service.New(client.New("", "", client.WithBearerAuth(token)))
	ctx := context.Background()

	cluster, err := svc.GetKubernetesCluster(ctx, &request.GetKubernetesClusterRequest{UUID: clusterID})
	if err != nil {
		t.Fatalf("getting cluster: %v", err)
	}
	zone := cluster.Zone

	itp := instancetypes.NewProvider(svc, zone)
	if err := itp.Refresh(ctx); err != nil {
		t.Fatalf("refreshing instance types: %v", err)
	}

	its := itp.List()
	if len(its) == 0 {
		t.Fatal("expected at least one instance type from the live API")
	}
	t.Logf("discovered %d instance types in zone %s", len(its), zone)

	foundCloudNative := false
	foundSpot := false
	for _, it := range its {
		if it.Name == "CLOUDNATIVE-2xCPU-4GB" {
			foundCloudNative = true
		}
		for _, o := range it.Offerings {
			if o.Requirements.Get(karpv1.CapacityTypeLabelKey).Has(karpv1.CapacityTypeSpot) {
				foundSpot = true
			}
		}
	}
	if !foundCloudNative {
		t.Errorf("expected CLOUDNATIVE-2xCPU-4GB in discovered instance types")
	}
	t.Logf("spot offerings present: %v", foundSpot)
}

// TestLiveCloudProviderCreate provisions a real UpCloud server through the cloud provider, creates the corresponding NodeClaim k8s
// resource, and validates that the server is reachable via Get with the correct providerID and labels.
func TestLiveCloudProviderCreate(t *testing.T) {
	token := os.Getenv("UPCLOUD_TOKEN")
	clusterID := os.Getenv("UPCLOUD_KUBERNETES_CLUSTER_ID")
	if token == "" || clusterID == "" {
		t.Skip("skipping live e2e: set UPCLOUD_TOKEN and UPCLOUD_KUBERNETES_CLUSTER_ID")
	}
	if os.Getenv("UPCLOUD_E2E_PROVISION") != "1" {
		t.Skip("skipping provisioning e2e: set UPCLOUD_E2E_PROVISION=1")
	}

	plan := os.Getenv("UPCLOUD_E2E_PLAN")
	if plan == "" {
		plan = "CLOUDNATIVE-2xCPU-4GB"
	}
	capacityType := os.Getenv("UPCLOUD_E2E_CAPACITY_TYPE")
	if capacityType == "" {
		capacityType = karpv1.CapacityTypeOnDemand
	}

	ctx := context.Background()
	svc := service.New(client.New("", "", client.WithBearerAuth(token)))

	cluster, err := svc.GetKubernetesCluster(ctx, &request.GetKubernetesClusterRequest{UUID: clusterID})
	if err != nil {
		t.Fatalf("getting cluster: %v", err)
	}
	zone := cluster.Zone

	kubeconfig, err := svc.GetKubernetesKubeconfig(ctx, &request.GetKubernetesKubeconfigRequest{UUID: clusterID})
	if err != nil {
		t.Fatalf("getting kubeconfig: %v", err)
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		t.Fatalf("parsing kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("adding clientgo scheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding upcloud scheme: %v", err)
	}
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "karpenter.sh", Version: "v1"})
	scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	kubeClient, err := kclient.New(restConfig, kclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building kube client: %v", err)
	}
	kubeClientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("building kube clientset: %v", err)
	}

	templateUUID := os.Getenv("UPCLOUD_TEMPLATE_UUID")
	if templateUUID == "" && len(cluster.NodeGroups) > 0 {
		templateUUID = cluster.NodeGroups[0].Storage
	}
	if templateUUID == "" {
		templateUUID = "01000000-0000-4000-8000-000160150100"
	}
	networkUUID := cluster.Network

	instanceProvider := instance.NewProvider(svc, templateUUID, networkUUID)
	userDataProvider := userdata.NewProvider()
	itProvider := instancetypes.NewProvider(svc, zone)
	if err := itProvider.Refresh(ctx); err != nil {
		t.Fatalf("refreshing instance types: %v", err)
	}

	clusterEndpoint := restConfig.Host
	if ep := os.Getenv("CLUSTER_ENDPOINT"); ep != "" {
		clusterEndpoint = ep
	}

	cp := cloudprovider.NewCloudProvider(kubeClient, kubeClientset, instanceProvider, userDataProvider, itProvider, zone, clusterEndpoint, 30*time.Minute)

	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	nodeclassName := "e2e-" + runID

	nodeclass := &v1alpha1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: nodeclassName},
		Spec: v1alpha1.UpCloudNodeClassSpec{
			Zone:        zone,
			Plan:        plan,
			StorageGB:   20,
			StorageTier: upcloud.StorageTierStandard,
			Labels:      map[string]string{"e2e-run": runID},
		},
	}
	if err := retryOnHTTP2Error(ctx, func() error {
		return kubeClient.Create(ctx, nodeclass)
	}); err != nil {
		t.Fatalf("creating nodeclass: %v", err)
	}

	var created *karpv1.NodeClaim
	defer func() {
		if created != nil {
			_ = cp.Delete(context.WithoutCancel(ctx), created)
		}
		cleanupE2EServers(context.WithoutCancel(ctx), instanceProvider, runID)
		_ = retryOnHTTP2Error(context.WithoutCancel(ctx), func() error {
			return kubeClient.Delete(context.WithoutCancel(ctx), nodeclass)
		})
	}()

	gpuFallbackPlans := []string{
		"GPU-SPOT-8xCPU-64GB-1xL4",
		"GPU-SPOT-12xCPU-128GB-1xL4",
		"GPU-SPOT-16xCPU-192GB-1xL4",
	}
	plansToTry := []string{plan}
	if plan == gpuFallbackPlans[0] {
		plansToTry = gpuFallbackPlans
	}

	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-nc-" + runID},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{Name: nodeclassName},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{
					Key:      karpv1.CapacityTypeLabelKey,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{capacityType},
				},
				{
					Key:      corev1.LabelInstanceTypeStable,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{plan},
				},
			},
		},
	}

	for _, candidate := range plansToTry {
		plan = candidate
		nodeClaim.Spec.Requirements[1].Values = []string{plan}

		createCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		created, err = cp.Create(createCtx, nodeClaim)
		cancel()
		if err != nil {
			if strings.Contains(err.Error(), "SERVER_RESOURCES_UNAVAILABLE") {
				t.Logf("plan %s has no capacity, trying next", candidate)
				continue
			}
			t.Fatalf("Create failed: %v", err)
		}
		break
	}
	if created == nil {
		t.Skipf("all GPU plans have no capacity in zone %s", zone)
	}
	if !strings.HasPrefix(created.Status.ProviderID, "upcloud:////") {
		t.Errorf("expected upcloud providerID, got %q", created.Status.ProviderID)
	}
	if created.Status.Capacity.Cpu().Value() <= 0 {
		t.Errorf("expected non-zero CPU capacity")
	}
	if created.Labels[karpv1.CapacityTypeLabelKey] != capacityType {
		t.Errorf("expected capacity-type %q, got %q", capacityType, created.Labels[karpv1.CapacityTypeLabelKey])
	}
	if created.Labels[corev1.LabelTopologyZone] != zone {
		t.Errorf("expected zone %q, got %q", zone, created.Labels[corev1.LabelTopologyZone])
	}
	got, err := cp.Get(ctx, created.Status.ProviderID)
	if err != nil {
		t.Errorf("Get after Create failed: %v", err)
	} else if got.Status.ProviderID != created.Status.ProviderID {
		t.Errorf("Get returned mismatched providerID: got %q want %q", got.Status.ProviderID, created.Status.ProviderID)
	}
}
