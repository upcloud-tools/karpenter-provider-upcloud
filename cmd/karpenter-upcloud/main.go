package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/client"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
	"github.com/upcloud-tools/karpenter-provider-upcloud/internal/version"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/cloudprovider"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instance"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/instancetypes"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/controllers/nodeclass"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/controllers/nodeclaimttl"
	"github.com/upcloud-tools/karpenter-provider-upcloud/pkg/providers/userdata"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/overlay"
	"sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator"
	"sigs.k8s.io/karpenter/pkg/state/prediction"
	"sigs.k8s.io/yaml"

	// Register UpCloudNodeClass types with the global scheme.
	_ "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
)

func main() {
	ctx := context.Background()
	ctxOp, op := operator.NewOperator()

	op.GetLogger().V(0).Info("starting operator", "version", version.Version, "commit", version.Commit, "treeState", version.TreeState)

	if err := run(ctx, ctxOp, op); err != nil {
		op.GetLogger().Error(err, "operator failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, ctxOp context.Context, op *operator.Operator) error {
	opts, err := providers.NewOptionsFromEnvironment()
	if err != nil {
		return fmt.Errorf("parsing options: %w", err)
	}

	svc := service.New(
		client.New("", "", client.WithBearerAuth(opts.Token)),
	)

	cluster, err := svc.GetKubernetesCluster(ctx, &request.GetKubernetesClusterRequest{
		UUID: opts.ClusterUUID,
	})
	if err != nil {
		return fmt.Errorf("fetching cluster details: %w", err)
	}

	clusterEndpoint, err := resolveClusterEndpoint(ctx, svc, opts.ClusterUUID)
	if err != nil {
		return fmt.Errorf("resolving cluster endpoint: %w", err)
	}

	zone := cluster.Zone

	storageUUID, err := requireEnv("UPCLOUD_TEMPLATE_UUID")
	if err != nil {
		return err
	}

	itProvider := instancetypes.NewProvider(svc, zone)
	if err := itProvider.Refresh(ctx); err != nil {
		return fmt.Errorf("refreshing instance types: %w", err)
	}

	instanceProvider := instance.NewProvider(svc, storageUUID, cluster.Network, opts.ClusterUUID, cluster.Name)
	userDataProvider := userdata.NewProvider()
	cp := cloudprovider.NewCloudProvider(
		op.GetClient(),
		op.KubernetesInterface,
		instanceProvider,
		userDataProvider,
		itProvider,
		zone,
		clusterEndpoint,
		opts.RepairToleration,
	)

	decCp := overlay.Decorate(cp, op.GetClient(), op.InstanceTypeStore)
	clusterState := state.NewCluster(op.Clock, op.GetClient(), decCp)
	predictionStore := prediction.NewStore()

	controllerList := controllers.NewControllers(
		ctxOp,
		op.Manager,
		op.Clock,
		op.GetClient(),
		op.EventRecorder,
		decCp,
		cp,
		clusterState,
		op.InstanceTypeStore,
		predictionStore,
	)

	ncController := nodeclass.Controller{
		Client: op.GetClient(),
	}
	if err := ncController.SetupWithManager(op.Manager); err != nil {
		return fmt.Errorf("setting up nodeclass controller: %w", err)
	}

	if v := os.Getenv("UPCLOUD_NODECLAIM_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("parsing UPCLOUD_NODECLAIM_TTL %q: %w", v, err)
		}
		ttlController := nodeclaimttl.Controller{
			Client: op.GetClient(),
			TTL:    d,
		}
		if err := ttlController.SetupWithManager(op.Manager); err != nil {
			return fmt.Errorf("setting up nodeclaim TTL controller: %w", err)
		}
		op.GetLogger().Info("nodeclaim TTL controller enabled", "TTL", d)
	}

	op.WithControllers(ctxOp, controllerList...).Start(ctxOp)
	return nil
}

func resolveClusterEndpoint(ctx context.Context, svc *service.Service, clusterUUID string) (string, error) {
	if endpoint := os.Getenv("CLUSTER_ENDPOINT"); endpoint != "" {
		return endpoint, nil
	}

	kubeconfig, err := svc.GetKubernetesKubeconfig(ctx, &request.GetKubernetesKubeconfigRequest{
		UUID: clusterUUID,
	})
	if err != nil {
		return "", fmt.Errorf("getting kubeconfig: %w", err)
	}

	var kc struct {
		Clusters []struct {
			Cluster struct {
				Server string `json:"server"`
			} `json:"cluster"`
		} `json:"clusters"`
	}
	if err := yaml.Unmarshal([]byte(kubeconfig), &kc); err != nil {
		return "", fmt.Errorf("parsing kubeconfig: %w", err)
	}

	if len(kc.Clusters) == 0 || kc.Clusters[0].Cluster.Server == "" {
		return "", fmt.Errorf("no server endpoint found in kubeconfig")
	}

	return kc.Clusters[0].Cluster.Server, nil
}

func requireEnv(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s environment variable is required", key)
	}
	return value, nil
}
