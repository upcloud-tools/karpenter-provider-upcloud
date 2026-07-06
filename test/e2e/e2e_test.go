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
	apisv1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/cloudprovider"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instance"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instancetypes"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/userdata"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// TestLiveInstanceTypes exercises the instance-type discovery against the real UpCloud API.
// It is skipped unless UPCLOUD_TOKEN and UPCLOUD_KUBERNETES_CLUSTER_ID are set, so it never runs in CI without credentials.
//
// Run locally with:
//	UPCLOUD_TOKEN=... UPCLOUD_KUBERNETES_CLUSTER_ID=... go test ./test/e2e/ -run TestLiveInstanceTypes -v
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

	// CloudNative-first default must be honoured by the live catalog.
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

// TestLiveCloudProviderCreate exercises the full cloudprovider.Create path against a real UpCloud cluster: it builds a real k8s client 
// from the cluster kubeconfig, creates an UpCloudNodeClass, and calls Create which performs kubelet cert generation (self-approved
// CSR), userdata rendering, and the real CreateServer call. The created server is then discovered via Get and cleaned up.
//
// It is gated behind UPCLOUD_E2E_PROVISION=1 (in addition to the credentials) so it never incurs cost accidentally.
//
// Preconditions:
//   - UPCLOUD_TOKEN and UPCLOUD_KUBERNETES_CLUSTER_ID are set.
//   - UPCLOUD_E2E_PROVISION=1.
//   - The target cluster has the UpCloudNodeClass CRD installed (the provider is deployed there).
//   - The cluster kubeconfig allows CSR create+approve (admin); kube-root-ca.crt exists in kube-system.
//
// Run locally with:
//
//	UPCLOUD_TOKEN=... UPCLOUD_KUBERNETES_CLUSTER_ID=... UPCLOUD_E2E_PROVISION=1 \
//	  UPCLOUD_E2E_PLAN=GPU-SPOT-4xCPU-8GB UPCLOUD_E2E_CAPACITY_TYPE=spot \
//	  go test ./test/e2e/ -run TestLiveCloudProviderCreate -v -timeout 20m
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

	// Build a real k8s client + clientset from the cluster kubeconfig (admin, CSR-capable).
	kubeconfig, err := svc.GetKubernetesKubeconfig(ctx, &request.GetKubernetesKubeconfigRequest{UUID: clusterID})
	if err != nil {
		t.Fatalf("getting kubeconfig: %v", err)
	}
	var restConfig *rest.Config
	restConfig, err = clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		t.Fatalf("parsing kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("adding clientgo scheme: %v", err)
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding upcloud scheme: %v", err)
	}
	kubeClient, err := kclient.New(restConfig, kclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building kube client: %v", err)
	}
	kubeClientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("building kube clientset: %v", err)
	}

	// Resolve template + network from the cluster (mirror cmd/karpenter-upcloud/main.go).
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

	// Unique run id so cleanup can find our servers by the e2e-run label (forwarded from the nodeclass).
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	nodeclassName := "e2e-" + runID

	nodeclass := &apisv1alpha1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: nodeclassName},
		Spec: apisv1alpha1.UpCloudNodeClassSpec{
			Zone:        zone,
			Plan:        plan,
			StorageGB:   20,
			StorageTier: upcloud.StorageTierStandard,
			Labels:      map[string]string{"e2e-run": runID},
		},
	}
	if err := kubeClient.Create(ctx, nodeclass); err != nil {
		t.Fatalf("creating nodeclass: %v", err)
	}

	var created *karpv1.NodeClaim
	defer func() {
		if created != nil {
			_ = cp.Delete(context.WithoutCancel(ctx), created)
		}
		cleanupE2EServers(context.WithoutCancel(ctx), instanceProvider, runID)
		_ = kubeClient.Delete(context.WithoutCancel(ctx), nodeclass)
	}()

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

	createCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	created, err = cp.Create(createCtx, nodeClaim)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if created == nil {
		t.Fatal("Create returned nil nodeclaim")
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

	// The server must be discoverable through the provider's Get.
	got, err := cp.Get(ctx, created.Status.ProviderID)
	if err != nil {
		t.Errorf("Get after Create failed: %v", err)
	} else if got.Status.ProviderID != created.Status.ProviderID {
		t.Errorf("Get returned mismatched providerID: got %q want %q", got.Status.ProviderID, created.Status.ProviderID)
	}
}

// cleanupE2EServers best-effort deletes any servers this run created (tagged with the e2e-run label),
// so a partial failure during Create does not leave orphaned servers behind.
func cleanupE2EServers(ctx context.Context, ip *instance.Provider, runID string) {
	servers, err := ip.List(ctx)
	if err != nil {
		return
	}
	for _, s := range servers {
		matched := false
		for _, l := range s.Labels {
			if l.Key == "e2e-run" && l.Value == runID {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		_ = ip.Stop(ctx, s.UUID)
		for range 6 {
			if err := ip.Delete(ctx, s.UUID); err == nil {
				break
			}
			time.Sleep(10 * time.Second)
		}
	}
}
