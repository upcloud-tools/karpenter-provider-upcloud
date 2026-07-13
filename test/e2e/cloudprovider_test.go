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
	v1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
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

	svc := service.New(client.New("", "", client.WithBearerAuth(token)))
	ctx := context.Background()

	cluster, err := svc.GetKubernetesCluster(ctx, &request.GetKubernetesClusterRequest{UUID: clusterID})
	require.NoError(t, err, "getting cluster")

	zone := cluster.Zone

	itp := instancetypes.NewProvider(svc, zone)
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
}

// TestLiveCloudProviderCreate provisions a real UpCloud server through the cloud provider, creates the corresponding NodeClaim k8s
// resource, and validates that the server is reachable via Get with the correct providerID and labels.
func TestLiveCloudProviderCreate(t *testing.T) {
	env := newE2ETestEnv(t)
	defer env.cleanupServers()

	plan := env.envPlan()
	capacityType := env.envCapacityType()

	nodeclassName := "e2e-" + env.runID
	nodeclass := &v1alpha1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: nodeclassName},
		Spec: v1alpha1.UpCloudNodeClassSpec{
			Zone:        env.zone,
			Plan:        plan,
			StorageGB:   20,
			StorageTier: upcloud.StorageTierStandard,
			Labels:      map[string]string{"e2e-run": env.runID},
		},
	}
	require.NoError(t, retryOnHTTP2Error(env.ctx, func() error {
		return env.kubeClient.Create(env.ctx, nodeclass)
	}), "creating nodeclass")

	var created *karpv1.NodeClaim
	defer func() {
		if created != nil {
			_ = env.cp.Delete(context.WithoutCancel(env.ctx), created)
		}
		cleanupE2EServers(context.WithoutCancel(env.ctx), env.instanceProvider, env.runID)
		_ = retryOnHTTP2Error(context.WithoutCancel(env.ctx), func() error {
			return env.kubeClient.Delete(context.WithoutCancel(env.ctx), nodeclass)
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

	assert.True(t, strings.HasPrefix(created.Status.ProviderID, "upcloud:////"), "expected upcloud providerID, got %q", created.Status.ProviderID)
	assert.True(t, created.Status.Capacity.Cpu().Value() > 0, "expected non-zero CPU capacity")
	assert.Equal(t, capacityType, created.Labels[karpv1.CapacityTypeLabelKey], "capacity-type label")
	assert.Equal(t, env.zone, created.Labels[corev1.LabelTopologyZone], "zone label")

	got, err := env.cp.Get(env.ctx, created.Status.ProviderID)
	if assert.NoError(t, err, "Get after Create") {
		assert.Equal(t, created.Status.ProviderID, got.Status.ProviderID, "providerID mismatch")
	}
}
