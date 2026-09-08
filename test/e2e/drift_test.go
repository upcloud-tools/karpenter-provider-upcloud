//go:build e2e

package e2e

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/stretchr/testify/require"
	v1alpha2 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// updateNodeClass applies mutate to the live NodeClass. It retries on conflicts (a deployed
// controller may be updating the object concurrently) and on transient HTTP/2 errors.
func (env *e2eTestEnv) updateNodeClass(t *testing.T, name string, mutate func(*v1alpha2.UpCloudNodeClass)) {
	t.Helper()
	key := types.NamespacedName{Name: name}
	var lastErr error
	for range 10 {
		fresh := &v1alpha2.UpCloudNodeClass{}
		if err := env.kubeClient.Get(env.ctx, key, fresh); err != nil {
			lastErr = err
			if strings.Contains(err.Error(), "http2") {
				continue
			}
			require.NoError(t, err, "getting nodeclass %s", name)
			return
		}
		mutate(fresh)
		if err := env.kubeClient.Update(env.ctx, fresh); err != nil {
			lastErr = err
			if apierrors.IsConflict(err) || strings.Contains(err.Error(), "http2") {
				continue
			}
			require.NoError(t, err, "updating nodeclass %s", name)
			return
		}
		return
	}
	require.NoError(t, lastErr, "updating nodeclass %s: retries exhausted", name)
}

// driftSignal polls IsDrifted until it reports the expected reason (or the timeout expires).
// A single call is read-your-writes in principle; the poll absorbs API flakiness.
func (env *e2eTestEnv) driftSignal(t *testing.T, created *karpv1.NodeClaim, want bool) {
	t.Helper()
	var lastReason string
	var lastErr error
	require.Eventually(t, func() bool {
		reason, err := env.cp.IsDrifted(env.ctx, created)
		lastReason, lastErr = string(reason), err
		return err == nil && (reason != "") == want
	}, 30*time.Second, 2*time.Second, "IsDrifted never reported the expected state")
	require.NoError(t, lastErr)
	if want {
		require.Equal(t, string(v1alpha2.NodeClassDrifted), lastReason, "expected the NodeClassDrifted reason")
	} else {
		require.Empty(t, lastReason, "expected no drift reason")
	}
}

// provisionDriftNode creates a NodeClass, provisions a real UpCloud server through the production cp.Create path
// (so the NodeClaim carries the provider's hash stamping), waits for the server to start, and registers cleanup.
// The returned claim exists only in memory: a deployed karpenter in the test cluster never sees it, so drift assertions
// cannot be disrupted by live disruption loops.
func (env *e2eTestEnv) provisionDriftNode(t *testing.T, extra func(*v1alpha2.UpCloudNodeClassSpec)) (*v1alpha2.UpCloudNodeClass, *karpv1.NodeClaim) {
	t.Helper()

	capacityType := env.envCapacityType()
	plans := env.envPlans()
	if plans == nil {
		plans = []string{defaultCloudnativePlan}
	}

	nodeclassName := "e2e-drift-" + env.runID
	nodeclass := &v1alpha2.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: nodeclassName},
		Spec: v1alpha2.UpCloudNodeClassSpec{
			Zone:   env.zone,
			Plan:   plans[0],
			Labels: map[string]string{"e2e-run": env.runID},
		},
	}
	if extra != nil {
		extra(&nodeclass.Spec)
	}
	env.createNodeClass(t, nodeclass)

	t.Cleanup(func() {
		env.cleanupServers()
		_ = retryOnHTTP2Error(context.WithoutCancel(env.ctx), func() error {
			fresh := &v1alpha2.UpCloudNodeClass{}
			if err := env.kubeClient.Get(context.WithoutCancel(env.ctx), types.NamespacedName{Name: nodeclassName}, fresh); err != nil {
				return err
			}
			return env.kubeClient.Delete(context.WithoutCancel(env.ctx), fresh)
		})
	})

	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-drift-nc-" + env.runID},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Kind:  "UpCloudNodeClass",
				Group: v1alpha2.Group,
				Name:  nodeclassName,
			},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{
					Key:      corev1.LabelInstanceTypeStable,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{plans[0]},
				},
				{
					Key:      karpv1.CapacityTypeLabelKey,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{capacityType},
				},
			},
		},
	}

	var created *karpv1.NodeClaim
	var err error
	for _, plan := range plans {
		env.updateNodeClass(t, nodeclassName, func(nc *v1alpha2.UpCloudNodeClass) { nc.Spec.Plan = plan })
		nodeClaim.Spec.Requirements[0].Values = []string{plan}
		t.Logf("provisioning drift-test server on plan %s (this may take 1-2 minutes)...", plan)

		createCtx, cancel := context.WithTimeout(env.ctx, 3*time.Minute)
		created, err = env.cp.Create(createCtx, nodeClaim)
		cancel()
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "SERVER_RESOURCES_UNAVAILABLE") {
			created = nil
			t.Logf("plan %s has no capacity, trying next", plan)
			continue
		}
		require.NoError(t, err, "Create for plan %s", plan)
	}
	if created == nil {
		t.Skipf("no capacity for any plan %v in zone %s", plans, env.zone)
	}
	t.Logf("server created: providerID=%s", created.Status.ProviderID)
	env.waitForServerStart(t, strings.TrimPrefix(created.Status.ProviderID, v1alpha2.ProviderIDPrefix))

	require.NotEmpty(t, created.Annotations[v1alpha2.NodeClassHashAnnotationKey], "Create must stamp the NodeClass hash")
	require.Equal(t, v1alpha2.NodeClassHashVersion, created.Annotations[v1alpha2.NodeClassHashVersionAnnotationKey],
		"Create must stamp the current hash version")

	return nodeclass, created
}

// TestLiveDriftRecycleSignal walks the full live chain: cp.Create stamps the hash, IsDrifted reports clean for an untouched NodeClass,
// and a spec change flips the signal to NodeClassDrifted: the trigger Karpenter core uses to cordon, drain, and replace the node.
func TestLiveDriftRecycleSignal(t *testing.T) {
	env := newE2ETestEnv(t)
	nodeclass, created := env.provisionDriftNode(t, nil)

	env.driftSignal(t, created, false)

	env.updateNodeClass(t, nodeclass.Name, func(nc *v1alpha2.UpCloudNodeClass) {
		nc.Spec.Labels["e2e-drift"] = "recycle-me"
	})

	env.driftSignal(t, created, true)
	t.Logf("✓ NodeClass spec change produces the live drift recycle signal")
}

// TestLiveDriftReorderIsNoDrift pins the hash-v2 guarantee against the live API: semantically equivalent list fields (taints, sshKeys)
// reordered in the NodeClass spec must NOT signal drift, while a genuine value change in the same field must.
func TestLiveDriftReorderIsNoDrift(t *testing.T) {
	env := newE2ETestEnv(t)
	nodeclass, created := env.provisionDriftNode(t, func(spec *v1alpha2.UpCloudNodeClassSpec) {
		spec.SSHKeys = []string{"ssh-rsa AAAAE2EdriftkeyOne", "ssh-rsa AAAAE2EdriftkeyTwo"}
		spec.Taints = []upcloud.KubernetesTaint{
			{Key: "e2e-a", Value: "one", Effect: upcloud.KubernetesClusterTaintEffectNoSchedule},
			{Key: "e2e-b", Value: "two", Effect: upcloud.KubernetesClusterTaintEffectNoExecute},
		}
	})

	env.driftSignal(t, created, false)

	env.updateNodeClass(t, nodeclass.Name, func(nc *v1alpha2.UpCloudNodeClass) {
		slices.Reverse(nc.Spec.SSHKeys)
		slices.Reverse(nc.Spec.Taints)
	})
	env.driftSignal(t, created, false)
	t.Logf("✓ reordering taints/sshKeys is not drift")

	env.updateNodeClass(t, nodeclass.Name, func(nc *v1alpha2.UpCloudNodeClass) {
		nc.Spec.Taints[0].Value = "changed-for-real"
	})
	env.driftSignal(t, created, true)
	t.Logf("✓ a real taint value change still signals drift")
}

// TestLiveDriftStaleVersionExempt verifies the upgrade-safety gate live: a claim whose stored hash version differs from the running one
// is never recycled, and comparison resumes once its version is current.
func TestLiveDriftStaleVersionExempt(t *testing.T) {
	env := newE2ETestEnv(t)
	nodeclass, created := env.provisionDriftNode(t, nil)

	// Make the live spec genuinely differ from what the claim was stamped against.
	env.updateNodeClass(t, nodeclass.Name, func(nc *v1alpha2.UpCloudNodeClass) {
		nc.Spec.Labels["e2e-drift"] = "matters-only-for-current-version"
	})

	// Simulate a claim persisted by a superseded Karpenter release: hash present, version stale.
	created.Annotations[v1alpha2.NodeClassHashVersionAnnotationKey] = "v0-pre-versioning"
	env.driftSignal(t, created, false)
	t.Logf("✓ superseded hash version is exempt from comparison")

	// Simulate the nodeclass controller re-baselining the claim: hash and version both current.
	created.Annotations[v1alpha2.NodeClassHashAnnotationKey] = nodeclassLiveHash(t, env, nodeclass.Name)
	created.Annotations[v1alpha2.NodeClassHashVersionAnnotationKey] = v1alpha2.NodeClassHashVersion
	env.driftSignal(t, created, false)

	// ...and drift is evaluated again from here: flip a field to confirm the gate re-engaged.
	env.updateNodeClass(t, nodeclass.Name, func(nc *v1alpha2.UpCloudNodeClass) {
		nc.Spec.Labels["e2e-drift-2"] = "post-rebaseline"
	})
	env.driftSignal(t, created, true)
	t.Logf("✓ comparison resumes after re-baseline")
}

// nodeclassLiveHash mirrors what rebaselineClaimHashes stamps: the current spec's hash, computed with the running algorithm.
func nodeclassLiveHash(t *testing.T, env *e2eTestEnv, name string) string {
	t.Helper()
	fresh := &v1alpha2.UpCloudNodeClass{}
	require.NoError(t, env.kubeClient.Get(env.ctx, types.NamespacedName{Name: name}, fresh),
		"getting nodeclass %s for hash", name)
	return fresh.Hash()
}
