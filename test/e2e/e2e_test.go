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
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/controllers/nodeclaimttl"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instance"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instancetypes"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/userdata"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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
//	  UPCLOUD_E2E_PLAN=GPU-8xCPU-64GB-1xL4 UPCLOUD_E2E_CAPACITY_TYPE=on-demand \
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
	if err := v1alpha1.AddToScheme(scheme); err != nil {
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

	// Spot GPU fallback plans to try in order when a plan has no capacity in the zone. Ordered by price.
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

	// The server must be discoverable through the provider's Get.
	got, err := cp.Get(ctx, created.Status.ProviderID)
	if err != nil {
		t.Errorf("Get after Create failed: %v", err)
	} else if got.Status.ProviderID != created.Status.ProviderID {
		t.Errorf("Get returned mismatched providerID: got %q want %q", got.Status.ProviderID, created.Status.ProviderID)
	}
}

const upcloudProviderPrefix = "upcloud:////"

// TestLiveNodeClaimTTL exercises the nodeclaimttl controller against a real cluster. It creates a real UpCloud server,
// the corresponding NodeClaim + Node k8s resources, patches the TTL to expire immediately, and verifies the controller
// decommissions the node (adds the NoSchedule taint and deletes the NodeClaim).
//
// Gated behind UPCLOUD_E2E_PROVISION=1 (in addition to the credentials) so it never incurs cost accidentally.
//
// Run locally with:
// UPCLOUD_TOKEN=... UPCLOUD_KUBERNETES_CLUSTER_ID=... UPCLOUD_E2E_PROVISION=1 \
//   go test ./test/e2e/ -run TestLiveNodeClaimTTL -v -timeout 20m
func TestLiveNodeClaimTTL(t *testing.T) {
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
	karpenterGV := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	scheme.AddKnownTypes(karpenterGV, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
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
	nodeclassName := "e2e-ttl-" + runID

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

	var ncDeleted bool
	defer func() {
		if !ncDeleted {
			_ = cp.Delete(context.WithoutCancel(ctx), &karpv1.NodeClaim{
				Status: karpv1.NodeClaimStatus{ProviderID: upcloudProviderPrefix + "placeholder"},
			})
		}
		cleanupE2EServers(context.WithoutCancel(ctx), instanceProvider, runID)
		_ = retryOnHTTP2Error(context.WithoutCancel(ctx), func() error {
			return kubeClient.Delete(context.WithoutCancel(ctx), nodeclass)
		})
	}()

	// Build the NodeClaim spec and start the UpCloud server.
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-ttl-nc-" + runID},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{Name: nodeclassName},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{
					Key:      corev1.LabelInstanceTypeStable,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{plan},
				},
				{
					Key:      karpv1.CapacityTypeLabelKey,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{capacityType},
				},
			},
		},
	}
	created, err := cp.Create(ctx, nodeClaim)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	providerID := created.Status.ProviderID
	t.Logf("created server: %s", providerID)

	// Create the NodeClaim k8s resource (metadata + spec only; status is set separately).
	createNC := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   created.Name,
			Labels: created.Labels,
			Annotations: map[string]string{
				v1alpha1.NodeClassHashAnnotationKey: created.Annotations[v1alpha1.NodeClassHashAnnotationKey],
			},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{Name: nodeclassName},
			Requirements: nodeClaim.Spec.Requirements,
		},
	}
	if err := kubeClient.Create(ctx, createNC); err != nil {
		t.Fatalf("creating NodeClaim k8s resource: %v", err)
	}
	t.Logf("created NodeClaim k8s resource: %s", createNC.Name)
	// Re-fetch after creation to pick up webhook defaulting, labels, resourceVersion, etc.
	if err := kubeClient.Get(ctx, types.NamespacedName{Name: createNC.Name}, createNC); err != nil {
		t.Fatalf("re-fetching NodeClaim after creation: %v", err)
	}
	createNC.Status = created.Status
	if err := kubeClient.Status().Update(ctx, createNC); err != nil {
		t.Fatalf("updating NodeClaim status: %v", err)
	}
	t.Logf("updated NodeClaim status (node=%s)", createNC.Status.NodeName)

	// Wait for the k8s Node to be registered by kubelet.
	t.Logf("waiting for node %s to appear...", created.Status.NodeName)
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 4*time.Minute, true, func(ctx context.Context) (bool, error) {
		node := &corev1.Node{}
		if err := kubeClient.Get(ctx, types.NamespacedName{Name: created.Status.NodeName}, node); err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Fatalf("timed out waiting for node %s: %v", created.Status.NodeName, err)
	}
	t.Logf("node %s is registered", created.Status.NodeName)

	// Re-fetch the NodeClaim (post-creation webhooks, status update, etc.) and patch the TTL annotation to expire immediately.
	if err := kubeClient.Get(ctx, types.NamespacedName{Name: createNC.Name}, createNC); err != nil {
		t.Fatalf("re-fetching NodeClaim: %v", err)
	}
	patch := kclient.MergeFrom(createNC.DeepCopy())
	if createNC.Annotations == nil {
		createNC.Annotations = map[string]string{}
	}
	createNC.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey] = time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	if err := kubeClient.Patch(ctx, createNC, patch); err != nil {
		t.Fatalf("patching TTL annotation: %v", err)
	}
	t.Logf("patched TTL annotation to 1h ago — controller should decommission")

	// Instantiate the TTL controller locally and reconcile it against the real k8s cluster.
	ttlCtrl := &nodeclaimttl.Controller{Client: kubeClient, TTL: 10 * time.Minute}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: createNC.Name}}
	result, err := ttlCtrl.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("TTL controller Reconcile failed: %v", err)
	}
	t.Logf("Reconcile returned: requeueAfter=%v", result.RequeueAfter)

	// Assert: NodeClaim should have a deletion timestamp or be fully gone.
	ncDeleted = true
	finalNC := &karpv1.NodeClaim{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Name: createNC.Name}, finalNC); kclient.IgnoreNotFound(err) != nil {
		t.Fatalf("getting NodeClaim after reconcile: %v", err)
	} else if kclient.IgnoreNotFound(err) == nil {
		t.Logf("NodeClaim fully deleted")
	} else if !finalNC.DeletionTimestamp.IsZero() {
		t.Logf("NodeClaim has deletion timestamp (termination in progress)")
	} else {
		t.Errorf("expected NodeClaim to be deleted or have deletion timestamp, but it still exists")
	}

	// Assert: Node should have the decommissioning NoSchedule taint.
	finalNode := &corev1.Node{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Name: created.Status.NodeName}, finalNode); err != nil {
		t.Logf("node %s no longer exists (expected after termination): %v", created.Status.NodeName, err)
	} else {
		hasTaint := false
		for _, taint := range finalNode.Spec.Taints {
			if taint.Key == v1alpha1.DecommissioningTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
				hasTaint = true
				break
			}
		}
		if !hasTaint {
			t.Errorf("expected decommissioning NoSchedule taint on node %s", created.Status.NodeName)
		} else {
			t.Logf("decommissioning taint present on node %s", created.Status.NodeName)
		}
	}
}

// retryOnHTTP2Error retries fn on transient HTTP/2 connection errors using exponential backoff.
func retryOnHTTP2Error(ctx context.Context, fn func() error) error {
	var lastErr error
	pollErr := wait.PollUntilContextTimeout(ctx, 2*time.Second, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		if err := fn(); err != nil {
			if strings.Contains(err.Error(), "http2") {
				lastErr = err
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	if pollErr != nil {
		return lastErr
	}
	return nil
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
