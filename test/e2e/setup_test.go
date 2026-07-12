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
	"k8s.io/client-go/tools/clientcmd"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const upcloudProviderPrefix = "upcloud:////"

// e2eTestEnv holds shared cluster clients and the cloud provider for TTL e2e tests.
type e2eTestEnv struct {
	ctx              context.Context
	kubeClient       kclient.Client
	cp               *cloudprovider.UpCloudCloudProvider
	instanceProvider *instance.Provider
	runID            string
}

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
	if err != nil {
		t.Fatalf("getting cluster: %v", err)
	}

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
	itProvider := instancetypes.NewProvider(svc, cluster.Zone)
	if err := itProvider.Refresh(ctx); err != nil {
		t.Fatalf("refreshing instance types: %v", err)
	}

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
		runID:            fmt.Sprintf("%d", time.Now().UnixNano()),
	}
}

func (env *e2eTestEnv) envPlan() string {
	if p := os.Getenv("UPCLOUD_E2E_PLAN"); p != "" {
		return p
	}
	return "CLOUDNATIVE-2xCPU-4GB"
}

func (env *e2eTestEnv) envCapacityType() string {
	if c := os.Getenv("UPCLOUD_E2E_CAPACITY_TYPE"); c != "" {
		return c
	}
	return karpv1.CapacityTypeOnDemand
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
func (env *e2eTestEnv) provisionServer(t *testing.T, plan, capacityType string) *e2eServer {
	t.Helper()

	nodeclassName := "e2e-ttl-" + env.runID

	nodeclass := &v1alpha1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: nodeclassName},
		Spec: v1alpha1.UpCloudNodeClassSpec{
			Zone:        os.Getenv("UPCLOUD_E2E_ZONE"),
			Plan:        plan,
			StorageGB:   20,
			StorageTier: upcloud.StorageTierStandard,
			Labels:      map[string]string{"e2e-run": env.runID},
		},
	}
	if err := retryOnHTTP2Error(env.ctx, func() error {
		return env.kubeClient.Create(env.ctx, nodeclass)
	}); err != nil {
		t.Fatalf("creating nodeclass: %v", err)
	}
	t.Cleanup(func() {
		_ = retryOnHTTP2Error(context.WithoutCancel(env.ctx), func() error {
			return env.kubeClient.Delete(context.WithoutCancel(env.ctx), nodeclass)
		})
	})

	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-ttl-nc-" + env.runID},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Kind:  "UpCloudNodeClass",
				Group: "karpenter.upcloud.com",
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

	created, err := env.cp.Create(env.ctx, nodeClaim)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	t.Cleanup(func() {
		_ = env.cp.Delete(context.WithoutCancel(env.ctx), created)
	})

	createNC := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   created.Name,
			Labels: created.Labels,
			Annotations: map[string]string{
				v1alpha1.NodeClassHashAnnotationKey: created.Annotations[v1alpha1.NodeClassHashAnnotationKey],
			},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Kind:  "UpCloudNodeClass",
				Group: "karpenter.upcloud.com",
				Name:  nodeclassName,
			},
			Requirements: nodeClaim.Spec.Requirements,
		},
	}
	if err := env.kubeClient.Create(env.ctx, createNC); err != nil {
		t.Fatalf("creating NodeClaim k8s resource: %v", err)
	}
	if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: createNC.Name}, createNC); err != nil {
		t.Fatalf("re-fetching NodeClaim after creation: %v", err)
	}
	createNC.Status = created.Status
	if err := env.kubeClient.Status().Update(env.ctx, createNC); err != nil {
		t.Fatalf("updating NodeClaim status: %v", err)
	}
	t.Logf("provisioned server %s (nc=%s, node=%s)", created.Status.ProviderID, createNC.Name, created.Status.NodeName)

	env.waitForNode(t, created.Status.NodeName)

	return &e2eServer{
		nodeClaim:     created,
		ncK8sName:     createNC.Name,
		nodeclassName: nodeclassName,
		nodeName:      created.Status.NodeName,
		plan:          plan,
	}
}

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
		t.Fatalf("timed out waiting for label %s on node %s: %v", labelKey, nodeName, err)
	}
}

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
		t.Fatalf("timed out waiting for node %s: %v", nodeName, err)
	}
}

// waitForUnschedulablePod blocks until the pod has PodScheduled=False with Reason=Unschedulable
// set by the scheduler. Returns the pod with its fresh state.
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
		t.Fatalf("timed out waiting for pod %s to be Unschedulable: %v", name, err)
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
	if err := env.kubeClient.Create(env.ctx, pod); err != nil {
		t.Fatalf("creating busy pod: %v", err)
	}
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
		t.Fatalf("timed out waiting for pod %s to become Running: %v", name, err)
	}
	return pod
}

func (env *e2eTestEnv) patchTTLToExpire(t *testing.T, ncName string) {
	t.Helper()
	nc := &karpv1.NodeClaim{}
	if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: ncName}, nc); err != nil {
		t.Fatalf("fetching NodeClaim for TTL patch: %v", err)
	}
	patch := kclient.MergeFrom(nc.DeepCopy())
	if nc.Annotations == nil {
		nc.Annotations = map[string]string{}
	}
	nc.Annotations[v1alpha1.NodeClaimTTLResetAnnotationKey] = time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	if err := env.kubeClient.Patch(env.ctx, nc, patch); err != nil {
		t.Fatalf("patching TTL annotation: %v", err)
	}
	t.Logf("patched TTL annotation to 1h ago on NodeClaim %s", ncName)
}

func (env *e2eTestEnv) taintNode(t *testing.T, nodeName string) {
	t.Helper()
	node := &corev1.Node{}
	if err := env.kubeClient.Get(env.ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		t.Fatalf("fetching node for taint: %v", err)
	}
	patch := kclient.MergeFrom(node.DeepCopy())
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    "e2e-test.upcloud.com/no-schedule",
		Effect: corev1.TaintEffectNoSchedule,
	})
	if err := env.kubeClient.Patch(env.ctx, node, patch); err != nil {
		t.Fatalf("tainting node: %v", err)
	}
	t.Logf("added NoSchedule taint to node %s", nodeName)
}

func (env *e2eTestEnv) reconcileTTL(t *testing.T, ncName string) reconcile.Result {
	t.Helper()
	ctrl := &nodeclaimttl.Controller{Client: env.kubeClient, TTL: 10 * time.Minute}
	result, err := ctrl.Reconcile(env.ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: ncName},
	})
	if err != nil {
		t.Fatalf("TTL controller Reconcile failed: %v", err)
	}
	return result
}

// cleanupServers removes all UpCloud servers tagged with the run ID.
func (env *e2eTestEnv) cleanupServers() {
	ctx := context.WithoutCancel(env.ctx)
	cleanupE2EServers(ctx, env.instanceProvider, env.runID)
}

// ── Shared utility functions ──────────────────────────────────────────────────

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
		_ = ip.WaitForStop(ctx, s.UUID)
		for range 6 {
			if err := ip.Delete(ctx, s.UUID); err == nil {
				break
			}
			time.Sleep(10 * time.Second)
		}
	}
}
