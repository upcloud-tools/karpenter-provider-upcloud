//go:build e2e

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1alpha2 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"
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
	"k8s.io/client-go/tools/clientcmd"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const defaultTemplateUUID    = "01000000-0000-4000-8000-000160150100"
const defaultCloudnativePlan = "CLOUDNATIVE-1xCPU-4GB"

// e2eTestEnv holds shared cluster clients and the cloud provider for e2e tests.
type e2eTestEnv struct {
	ctx              context.Context
	kubeClient       kclient.Client
	cp               *cloudprovider.UpCloudCloudProvider
	instanceProvider *instance.Provider
	itProvider       *instancetypes.Provider
	runID            string
	zone             string
	debug            bool
}

// newE2ETestEnv creates a new e2eTestEnv with the cloud provider and Kubernetes clients.
func newE2ETestEnv(t *testing.T) *e2eTestEnv {
	t.Helper()
	token := os.Getenv("UPCLOUD_TOKEN")
	clusterID := os.Getenv("UPCLOUD_KUBERNETES_CLUSTER_ID")
	if token == "" || clusterID == "" {
		t.Skip("skipping live e2e: set UPCLOUD_TOKEN and UPCLOUD_KUBERNETES_CLUSTER_ID")
	}
	if os.Getenv("UPCLOUD_E2E_PROVISION") != "1" {
		t.Skip("skipping provisioning e2e: set UPCLOUD_E2E_PROVISION=1")
	}

	ctx := context.Background()
	svc := service.New(client.New("", "", client.WithBearerAuth(token)))
	cluster, err := svc.GetKubernetesCluster(ctx, &request.GetKubernetesClusterRequest{UUID: clusterID})
	require.NoError(t, err, "getting cluster")

	kubeconfig, err := svc.GetKubernetesKubeconfig(ctx, &request.GetKubernetesKubeconfigRequest{UUID: clusterID})
	require.NoError(t, err, "getting kubeconfig")

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	require.NoError(t, err, "parsing kubeconfig")

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme), "adding clientgo scheme")
	require.NoError(t, v1alpha2.AddToScheme(scheme), "adding upcloud scheme")

	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "karpenter.sh", Version: "v1"})
	scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	kubeClient, err := kclient.New(restConfig, kclient.Options{Scheme: scheme})
	require.NoError(t, err, "building kube client")

	kubeClientset, err := kubernetes.NewForConfig(restConfig)
	require.NoError(t, err, "building kube clientset")

	networkUUID := cluster.Network
	templateUUID := os.Getenv("UPCLOUD_TEMPLATE_UUID")
	if templateUUID == "" && len(cluster.NodeGroups) > 0 {
		templateUUID = cluster.NodeGroups[0].Storage
	} else if templateUUID == "" {
		templateUUID = defaultTemplateUUID
	}

	instanceProvider := instance.NewProvider(svc, templateUUID, networkUUID, clusterID, cluster.Name)
	itProvider := instancetypes.NewProvider(svc, cluster.Zone)
	require.NoError(t, itProvider.Refresh(ctx), "refreshing instance types")

	clusterEndpoint := restConfig.Host
	if ep := os.Getenv("CLUSTER_ENDPOINT"); ep != "" {
		clusterEndpoint = ep
	}

	cp := cloudprovider.NewCloudProvider(kubeClient, kubeClientset, instanceProvider, userdata.NewProvider(), itProvider, cluster.Zone, clusterEndpoint, 30*time.Minute)

	return &e2eTestEnv{
		ctx:              ctx,
		kubeClient:       kubeClient,
		cp:               cp,
		instanceProvider: instanceProvider,
		itProvider:       itProvider,
		runID:            fmt.Sprintf("%d", time.Now().UnixNano()),
		zone:             cluster.Zone,
		debug:            os.Getenv("UPCLOUD_E2E_DEBUG") == "1",
	}
}

// envPlan returns the plan to use for the test environment.
func (env *e2eTestEnv) envPlan() string {
	if p := os.Getenv("UPCLOUD_E2E_PLAN"); p != "" {
		return p
	}
	return defaultCloudnativePlan
}

// envCapacityType returns the capacity type to use for the test environment.
func (env *e2eTestEnv) envCapacityType() string {
	if c := os.Getenv("UPCLOUD_E2E_CAPACITY_TYPE"); c != "" {
		return c
	}
	return karpv1.CapacityTypeOnDemand
}

// createNodeClass creates the NodeClass, retrying on transient HTTP/2 errors.
func (env *e2eTestEnv) createNodeClass(t *testing.T, nc *v1alpha2.UpCloudNodeClass) {
	t.Helper()
	require.NoError(t, retryOnHTTP2Error(env.ctx, func() error {
		return env.kubeClient.Create(env.ctx, nc)
	}), "creating nodeclass")
}

// deferNodeClassCleanup registers cleanup for the NodeClass and any servers tagged with the run ID.
// The NodeClaim (if one was created) must be cleaned up by the caller, since it does not exist yet at registration time.
func (env *e2eTestEnv) deferNodeClassCleanup(t *testing.T, nc *v1alpha2.UpCloudNodeClass) {
	t.Helper()
	t.Cleanup(func() {
		env.cleanupServers()
		_ = retryOnHTTP2Error(context.WithoutCancel(env.ctx), func() error {
			return env.kubeClient.Delete(context.WithoutCancel(env.ctx), nc)
		})
	})
}

// waitForServerStart blocks until the UpCloud server reaches the started state.
func (env *e2eTestEnv) waitForServerStart(t *testing.T, serverUUID string) {
	t.Helper()
	t.Logf("waiting for server %s to start...", serverUUID)
	waitCtx, waitCancel := context.WithTimeout(env.ctx, 3*time.Minute)
	defer waitCancel()
	if err := env.instanceProvider.WaitForStart(waitCtx, serverUUID); err != nil {
		t.Fatalf("waiting for server %s to start: %v", serverUUID, err)
	}
	t.Logf("server %s is now started", serverUUID)
}

// serverLabelMap converts a server's label slice into a map for assertions.
func serverLabelMap(srv *upcloud.ServerDetails) map[string]string {
	labels := make(map[string]string)
	for _, l := range srv.Labels {
		labels[l.Key] = l.Value
	}
	return labels
}

// verifyCreateGet confirms cloudprovider.Get returns the same providerID as Create.
func (env *e2eTestEnv) verifyCreateGet(t *testing.T, created *karpv1.NodeClaim) {
	t.Helper()
	t.Logf("verifying cloudprovider.Get...")
	got, err := env.cp.Get(env.ctx, created.Status.ProviderID)
	if assert.NoError(t, err, "Get after Create") {
		assert.Equal(t, created.Status.ProviderID, got.Status.ProviderID, "providerID mismatch")
	}
	t.Logf("✓ all assertions passed")
}

// e2eServer holds the resources created by provisionServer.
type e2eServer struct {
	nodeClaim     *karpv1.NodeClaim
	ncK8sName     string
	nodeclassName string
	nodeName      string
	plan          string
}

// dumpPendingPods logs all pending pods visible to the controller for diagnostics.
func (env *e2eTestEnv) dumpPendingPods(t *testing.T) {
	t.Helper()
	podList := &corev1.PodList{}
	if err := env.kubeClient.List(env.ctx, podList); err != nil {
		t.Logf("dump: listing pods failed: %v", err)
		return
	}
	for i := range podList.Items {
		p := &podList.Items[i]
		if p.Status.Phase != corev1.PodPending {
			continue
		}
		t.Logf("dump: pending pod %s phase=%s nodeName=%q conditions=%v",
			p.Name, p.Status.Phase, p.Spec.NodeName, p.Status.Conditions)
	}
}

// provisionServer creates a NodeClass, starts a real UpCloud server, creates the NodeClaim k8s resource,
// and waits for the k8s Node to register. It registers cleanup via t.Cleanup.
// Pass nil for storage to use plan-bundled storage (STARTER, PREMIUM plans).
func (env *e2eTestEnv) provisionServer(t *testing.T, plan, capacityType string, storage *v1alpha2.StorageSpec) *e2eServer {
	t.Helper()

	nodeclassName := "e2e-ttl-" + env.runID
	t.Logf("creating NodeClass %s for plan %s (storage=%v)...", nodeclassName, plan, storage != nil)
	nodeclass := &v1alpha2.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: nodeclassName},
		Spec: v1alpha2.UpCloudNodeClassSpec{
			Zone:    env.zone,
			Plan:    plan,
			Storage: storage,
			Labels:  map[string]string{"e2e-run": env.runID},
		},
	}

	require.NoError(t, retryOnHTTP2Error(env.ctx, func() error {
		return env.kubeClient.Create(env.ctx, nodeclass)
	}), "creating nodeclass")

	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-ttl-nc-" + env.runID},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Kind:  "UpCloudNodeClass",
				Group: "karpenter.k8s.upcloud",
				Name:  nodeclassName,
			},
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

	t.Logf("calling cloudprovider.Create for plan %s (this may take 1-2 minutes)...", plan)
	created, err := env.cp.Create(env.ctx, nodeClaim)
	require.NoError(t, err, "Create")
	t.Logf("server created: providerID=%s, nodeName=%s", created.Status.ProviderID, created.Status.NodeName)

	t.Cleanup(func() {
		t.Logf("cleaning up server %s...", created.Status.ProviderID)
		_ = env.cp.Delete(context.WithoutCancel(env.ctx), created)
		_ = retryOnHTTP2Error(context.WithoutCancel(env.ctx), func() error {
			return env.kubeClient.Delete(context.WithoutCancel(env.ctx), nodeclass)
		})
	})

	createNC := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   created.Name,
			Labels: created.Labels,
			Annotations: map[string]string{
				v1alpha2.NodeClassHashAnnotationKey: created.Annotations[v1alpha2.NodeClassHashAnnotationKey],
			},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Kind:  "UpCloudNodeClass",
				Group: "karpenter.k8s.upcloud",
				Name:  nodeclassName,
			},
			Requirements: nodeClaim.Spec.Requirements,
		},
	}
	t.Logf("creating NodeClaim k8s resource %s...", createNC.Name)
	require.NoError(t, env.kubeClient.Create(env.ctx, createNC), "creating NodeClaim k8s resource")
	require.NoError(t, env.kubeClient.Get(env.ctx, types.NamespacedName{Name: createNC.Name}, createNC), "re-fetching NodeClaim")
	createNC.Status = created.Status
	require.NoError(t, env.kubeClient.Status().Update(env.ctx, createNC), "updating NodeClaim status")
	t.Logf("provisioned server %s (nc=%s, node=%s)", created.Status.ProviderID, createNC.Name, created.Status.NodeName)

	t.Logf("waiting for node %s to be clean (Ready + no startup taints)...", created.Status.NodeName)
	env.waitForNodeClean(t, created.Status.NodeName)
	t.Logf("✓ node %s is clean and ready", created.Status.NodeName)

	return &e2eServer{
		nodeClaim:     created,
		ncK8sName:     createNC.Name,
		nodeclassName: nodeclassName,
		nodeName:      created.Status.NodeName,
		plan:          plan,
	}
}

// waitForNodeLabel waits for a label to appear on a node.
func (env *e2eTestEnv) waitForNodeLabel(t *testing.T, nodeName, labelKey string) {
	t.Helper()
	t.Logf("waiting for label %s on node %s...", labelKey, nodeName)

	if err := wait.PollUntilContextTimeout(env.ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		node := &corev1.Node{}
		if err := env.kubeClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			return false, nil
		}
		_, ok := node.Labels[labelKey]
		return ok, nil
	}); err != nil {
		require.NoError(t, err, "timed out waiting for label %s on node %s", labelKey, nodeName)
	}
}

// waitForNode waits for a node to appear in the cluster.
func (env *e2eTestEnv) waitForNode(t *testing.T, nodeName string) {
	t.Helper()
	t.Logf("waiting for node %s to appear...", nodeName)

	if err := wait.PollUntilContextTimeout(env.ctx, 5*time.Second, 4*time.Minute, true, func(ctx context.Context) (bool, error) {
		node := &corev1.Node{}
		if err := env.kubeClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		require.NoError(t, err, "timed out waiting for node %s", nodeName)
	}
}

// startupTaintKeys are well-known taint keys that the kubelet and node-lifecycle controllers set on any new node during bootstrapping.
// We wait for them to clear before tests proceed, so the controller doesn't reject a pending pod for not tolerating a transient startup taint.
var startupTaintKeys = []string{
	"karpenter.sh/unregistered",
	"node.cilium.io/agent-not-ready",
	"node.kubernetes.io/not-ready",
	"node.kubernetes.io/unreachable",
}

// waitForNodeClean waits for a node to register and reach Ready, then waits up to 3 minutes for startup taints to clear.
// If the startup taints don't clear within that window but the node is Ready, it proceeds anyway. This avoids hanging in environments
// where a node-controller or CNI isn't actively removing taints.
func (env *e2eTestEnv) waitForNodeClean(t *testing.T, nodeName string) {
	t.Helper()
	env.waitForNode(t, nodeName)
	t.Logf("waiting for node %s to be clean (ready + no startup taints)...", nodeName)

	const startupTaintWait = 3 * time.Minute
	start := time.Now()

	require.NoError(t, wait.PollUntilContextTimeout(env.ctx, 5*time.Second, 8*time.Minute, true, func(ctx context.Context) (bool, error) {
		node := &corev1.Node{}
		if err := env.kubeClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			return false, nil
		}
		if !nodeReady(node) {
			return false, nil
		}
		if time.Since(start) > startupTaintWait {
			// Grace period expired — accept Ready even if startup taints remain.
			return true, nil
		}
		for _, taint := range node.Spec.Taints {
			for _, key := range startupTaintKeys {
				if taint.Key == key {
					if env.debug {
						t.Logf("node %s still has startup taint %s (%.0fs into grace period)", nodeName, taint.Key, time.Since(start).Seconds())
					}
					return false, nil
				}
			}
		}
		return true, nil
	}), "timed out waiting for node %s to become Ready", nodeName)
}

// nodeReady reports whether the node has the NodeReady condition set to True.
func nodeReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// waitForUnschedulablePod blocks until the pod has PodScheduled=False with Reason=Unschedulable set by the scheduler.
// Returns the pod with its fresh state.
func (env *e2eTestEnv) waitForUnschedulablePod(t *testing.T, name string) *corev1.Pod {
	t.Helper()
	t.Logf("waiting for pod %s to be Unschedulable...", name)

	pod := &corev1.Pod{}
	if err := wait.PollUntilContextTimeout(env.ctx, 1*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := env.kubeClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, pod); err != nil {
			return false, nil
		}
		if pod.Spec.NodeName != "" {
			return false, nil
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == "Unschedulable" {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		require.NoError(t, err, "timed out waiting for pod %s to be Unschedulable", name)
	}
	return pod
}

// runPod creates a simple pause pod on the given node and waits for it to become Running.
func (env *e2eTestEnv) runPod(t *testing.T, name, nodeName string) *corev1.Pod {
	t.Helper()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "pause", Image: "pause"}},
		},
	}
	require.NoError(t, env.kubeClient.Create(env.ctx, pod), "creating busy pod")

	t.Cleanup(func() {
		_ = env.kubeClient.Delete(context.WithoutCancel(env.ctx), pod)
	})
	t.Logf("waiting for pod %s to be Running on node %s...", name, nodeName)

	if err := wait.PollUntilContextTimeout(env.ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := env.kubeClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, pod); err != nil {
			return false, nil
		}
		return pod.Status.Phase == corev1.PodRunning, nil
	}); err != nil {
		require.NoError(t, err, "timed out waiting for pod %s to become Running", name)
	}
	return pod
}

// patchTTLToExpire patches the TTL annotation on the NodeClaim to expire in the past, forcing a node replacement.
func (env *e2eTestEnv) patchTTLToExpire(t *testing.T, ncName string) {
	t.Helper()

	nc := &karpv1.NodeClaim{}
	require.NoError(t, env.kubeClient.Get(env.ctx, types.NamespacedName{Name: ncName}, nc), "fetching NodeClaim for TTL patch")

	patch := kclient.MergeFrom(nc.DeepCopy())
	if nc.Annotations == nil {
		nc.Annotations = map[string]string{}
	}
	nc.Annotations[v1alpha2.NodeClaimTTLResetAnnotationKey] = time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	require.NoError(t, env.kubeClient.Patch(env.ctx, nc, patch), "patching TTL annotation")
	t.Logf("patched TTL annotation to 1h ago on NodeClaim %s", ncName)
}

// taintNode taints the node with the e2e-test.upcloud.com/no-schedule taint, preventing scheduling of pods.
func (env *e2eTestEnv) taintNode(t *testing.T, nodeName string) {
	t.Helper()
	node := &corev1.Node{}
	require.NoError(t, env.kubeClient.Get(env.ctx, types.NamespacedName{Name: nodeName}, node), "fetching node for taint")

	patch := kclient.MergeFrom(node.DeepCopy())
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    "e2e-test.upcloud.com/no-schedule",
		Effect: corev1.TaintEffectNoSchedule,
	})
	require.NoError(t, env.kubeClient.Patch(env.ctx, node, patch), "tainting node")
	t.Logf("added NoSchedule taint to node %s", nodeName)
}

// reconcileTTL reconciles the TTL of the NodeClaim, forcing a node replacement.
func (env *e2eTestEnv) reconcileTTL(t *testing.T, ncName string) reconcile.Result {
	t.Helper()
	ctrl := &nodeclaimttl.Controller{Client: env.kubeClient, TTL: 10 * time.Minute}
	result, err := ctrl.Reconcile(env.ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: ncName},
	})
	require.NoError(t, err, "TTL controller Reconcile")
	return result
}

// cleanupServers removes all UpCloud servers tagged with the run ID.
func (env *e2eTestEnv) cleanupServers() {
	ctx := context.WithoutCancel(env.ctx)
	cleanupE2EServers(ctx, env.instanceProvider, env.runID)
}

// ── Shared utility functions ──────────────────────────────────────────────────

// retryOnHTTP2Error retries a function until it succeeds or returns a non-HTTP/2 error.
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

// cleanupE2EServers removes all UpCloud servers tagged with the run ID.
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
		if err := ip.Stop(ctx, s.UUID); err != nil {
			fmt.Printf("cleanup: stop server %s failed: %v\n", s.UUID, err)
		}
		stopCtx, stopCancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := ip.WaitForStop(stopCtx, s.UUID); err != nil {
			fmt.Printf("cleanup: wait for stop server %s failed/timed out: %v\n", s.UUID, err)
		}
		stopCancel()
		for range 6 {
			if err := ip.Delete(ctx, s.UUID); err == nil {
				break
			}
			time.Sleep(10 * time.Second)
		}
	}
}
