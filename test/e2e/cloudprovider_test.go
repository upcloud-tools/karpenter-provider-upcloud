//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/client"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1alpha2 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instancetypes"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	t.Logf("querying UpCloud API for instance types in cluster %s...", clusterID)
	svc := service.New(client.New("", "", client.WithBearerAuth(token)))
	ctx := context.Background()

	cluster, err := svc.GetKubernetesCluster(ctx, &request.GetKubernetesClusterRequest{UUID: clusterID})
	require.NoError(t, err, "getting cluster")

	zone := cluster.Zone
	t.Logf("cluster zone: %s", zone)

	itp := instancetypes.NewProvider(svc, zone)
	t.Logf("refreshing instance types from UpCloud API...")
	require.NoError(t, itp.Refresh(ctx), "refreshing instance types")

	its := itp.List()
	require.NotEmpty(t, its, "expected at least one instance type from the live API")
	t.Logf("discovered %d instance types in zone %s", len(its), zone)

	foundCloudNative := false
	foundSpot := false
	for _, it := range its {
		if it.Name == defaultCloudnativePlan {
			foundCloudNative = true
		}
		for _, o := range it.Offerings {
			if o.Requirements.Get(karpv1.CapacityTypeLabelKey).Has(karpv1.CapacityTypeSpot) {
				foundSpot = true
			}
		}
	}
	assert.True(t, foundCloudNative, "expected CLOUDNATIVE-2xCPU-4GB in discovered instance types")
	t.Logf("spot offerings present: %v", foundSpot)
	t.Logf("✓ instance type discovery test passed")
}

// TestLiveCloudProviderCreateBundledStorage provisions a real UpCloud server with a bundled-storage plan (STARTER)
// and verifies that the server is created successfully without explicit storage configuration.
// This tests the fix for plans that include storage (STARTER, PREMIUM) where size/tier should be omitted.
func TestLiveCloudProviderCreateBundledStorage(t *testing.T) {
	env := newE2ETestEnv(t)

	plan := os.Getenv("UPCLOUD_E2E_BUNDLED_PLAN")
	if plan == "" {
		plan = "STARTER-1xCPU-1GB"
	}
	t.Logf("using bundled-storage plan: %s", plan)

	// Create NodeClass with Storage: nil to test bundled-storage path
	nodeclassName := "e2e-bundled-" + env.runID
	t.Logf("creating NodeClass %s with Storage=nil (bundled storage)", nodeclassName)
	nodeclass := &v1alpha2.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: nodeclassName},
		Spec: v1alpha2.UpCloudNodeClassSpec{
			Zone:    env.zone,
			Plan:    plan,
			Storage: nil, // Explicitly nil - use plan's bundled storage
			Labels:  map[string]string{"e2e-run": env.runID},
		},
	}
	env.createNodeClass(t, nodeclass)

	var created *karpv1.NodeClaim
	t.Cleanup(func() {
		if created != nil {
			t.Logf("cleaning up NodeClaim %s", created.Name)
			_ = env.cp.Delete(context.WithoutCancel(env.ctx), created)
		}
	})
	env.deferNodeClassCleanup(t, nodeclass)

	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-bundled-nc-" + env.runID},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{Name: nodeclassName},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{
					Key:      corev1.LabelInstanceTypeStable,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{plan},
				},
			},
		},
	}

	t.Logf("calling cloudprovider.Create for plan %s (this may take 1-2 minutes)...", plan)
	createCtx, cancel := context.WithTimeout(env.ctx, 3*time.Minute)
	defer cancel()

	var err error
	created, err = env.cp.Create(createCtx, nodeClaim)
	if err != nil {
		if strings.Contains(err.Error(), "SERVER_RESOURCES_UNAVAILABLE") {
			t.Skipf("plan %s has no capacity in zone %s", plan, env.zone)
		}
		require.NoError(t, err, "Create for bundled-storage plan %s", plan)
	}
	t.Logf("server created: providerID=%s, nodeName=%s", created.Status.ProviderID, created.Status.NodeName)

	serverUUID := strings.TrimPrefix(created.Status.ProviderID, "upcloud:////")
	env.waitForServerStart(t, serverUUID)

	// Verify the server was created with bundled storage
	t.Logf("fetching server details to verify labels...")
	server, err := env.instanceProvider.Get(env.ctx, serverUUID)
	if assert.NoError(t, err, "getting server details") {
		serverLabels := serverLabelMap(server)
		assert.Equal(t, env.runID, serverLabels["e2e-run"], "e2e-run label should be passed through")
		assert.Equal(t, plan, server.Plan, "server should use the bundled-storage plan")
		t.Logf("✓ server %s created successfully with bundled storage (plan=%s)", serverUUID, plan)
	}

	env.verifyCreateGet(t, created)
}

// TestLiveCloudProviderCreate provisions a real UpCloud server through the cloud provider, creates the corresponding NodeClaim k8s
// resource, and validates that the server is reachable via Get with the correct providerID and labels.
func TestLiveCloudProviderCreate(t *testing.T) {
	env := newE2ETestEnv(t)

	plan := env.envPlan()
	capacityType := env.envCapacityType()
	t.Logf("using plan: %s, capacity-type: %s", plan, capacityType)

	nodeclassName := "e2e-" + env.runID
	t.Logf("creating NodeClass %s...", nodeclassName)
	nodeclass := &v1alpha2.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: nodeclassName},
		Spec: v1alpha2.UpCloudNodeClassSpec{
			Zone: env.zone,
			Plan: plan,
			Storage: &v1alpha2.StorageSpec{
				Size: 20,
				Tier: upcloud.StorageTierStandard,
			},
			Labels: map[string]string{
				"e2e-run":                 env.runID,
				"node.kubernetes.io/test": "slash-label",
				"karpenter.sh/test":       "dot-label",
			},
		},
	}
	env.createNodeClass(t, nodeclass)

	var created *karpv1.NodeClaim
	t.Cleanup(func() {
		if created != nil {
			t.Logf("cleaning up NodeClaim %s", created.Name)
			_ = env.cp.Delete(context.WithoutCancel(env.ctx), created)
		}
	})
	env.deferNodeClassCleanup(t, nodeclass)

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
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-nc-" + env.runID},
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

	var err error
	for _, candidate := range plansToTry {
		plan = candidate
		nodeClaim.Spec.Requirements[1].Values = []string{plan}
		t.Logf("calling cloudprovider.Create for plan %s (this may take 1-2 minutes)...", plan)

		createCtx, cancel := context.WithTimeout(env.ctx, 3*time.Minute)
		created, err = env.cp.Create(createCtx, nodeClaim)
		cancel()
		if err != nil {
			if strings.Contains(err.Error(), "SERVER_RESOURCES_UNAVAILABLE") {
				t.Logf("plan %s has no capacity, trying next", candidate)
				continue
			}
			require.NoError(t, err, "Create for plan %s", candidate)
		}
		break
	}
	if created == nil {
		t.Skipf("all GPU plans have no capacity in zone %s", env.zone)
	}
	t.Logf("server created: providerID=%s, nodeName=%s", created.Status.ProviderID, created.Status.NodeName)

	serverUUID := strings.TrimPrefix(created.Status.ProviderID, "upcloud:////")
	env.waitForServerStart(t, serverUUID)

	assert.True(t, strings.HasPrefix(created.Status.ProviderID, "upcloud:////"), "expected upcloud providerID, got %q", created.Status.ProviderID)
	assert.True(t, created.Status.Capacity.Cpu().Value() > 0, "expected non-zero CPU capacity")
	assert.Equal(t, capacityType, created.Labels[karpv1.CapacityTypeLabelKey], "capacity-type label")
	assert.Equal(t, env.zone, created.Labels[corev1.LabelTopologyZone], "zone label")

	// Verify labels passed through to the UpCloud server
	t.Logf("fetching server details to verify labels...")
	server, err := env.instanceProvider.Get(env.ctx, serverUUID)
	if assert.NoError(t, err, "getting server details for label validation") {
		serverLabels := serverLabelMap(server)
		assert.Equal(t, "slash-label", serverLabels["node.kubernetes.io/test"], "label with slash should be passed through to UpCloud server")
		assert.Equal(t, "dot-label", serverLabels["karpenter.sh/test"], "label with dot should be passed through to UpCloud server")
		assert.Equal(t, env.runID, serverLabels["e2e-run"], "e2e-run label should be passed through to UpCloud server")
		t.Logf("✓ all labels verified on server")
	}

	env.verifyCreateGet(t, created)
}
