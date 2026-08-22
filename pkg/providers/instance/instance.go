package instance

import (
	"context"
	"fmt"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"

	v1alpha2 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"
)

const managedLabelKey = "managed_by"
const managedLabelValue = "karpenter"

// Provider manages the lifecycle of UpCloud servers (create, delete, get, list, stop).
// Each server is cloned from a template storage device and attached to the cluster network.
type Provider struct {
	svc          service.Server
	templateUUID string
	networkUUID  string
	clusterID    string
	clusterName  string
}

// NewProvider creates a Provider that clones the given template storage onto each server and attaches it to the given cluster network.
func NewProvider(svc service.Server, templateUUID, networkUUID, clusterID, clusterName string) *Provider {
	return &Provider{
		svc:          svc,
		templateUUID: templateUUID,
		networkUUID:  networkUUID,
		clusterID:    clusterID,
		clusterName:  clusterName,
	}
}

// Create provisions a new UpCloud server: clones the template storage, attaches private/utility/public networking, injects
// userdata (containing kubelet config and TLS certs), and applies the managed marker label.
func (p *Provider) Create(ctx context.Context, hostname, plan, zone, userData string, labels map[string]string, storage *v1alpha2.StorageSpec) (*upcloud.ServerDetails, error) {
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[managedLabelKey] = managedLabelValue
	labels["capu_cluster_id"] = p.clusterID
	labels["capu_cluster_name"] = p.clusterName
	labels["capu_generated_name"] = hostname

	labelSlice := &upcloud.LabelSlice{}
	for k, v := range labels {
		*labelSlice = append(*labelSlice, upcloud.Label{Key: k, Value: v})
	}

	storageSizeGB := 20
	storageTier := string(upcloud.StorageTierStandard)
	storageEncrypted := false
	if storage != nil {
		if storage.Size > 0 {
			storageSizeGB = storage.Size
		}
		if storage.Tier != "" {
			storageTier = string(storage.Tier)
		}
		storageEncrypted = storage.Encrypted != nil && *storage.Encrypted
	}

	createReq := &request.CreateServerRequest{
		Labels:   labelSlice,
		Zone:     zone,
		Hostname: hostname,
		Title:    hostname,
		Plan:     plan,
		StorageDevices: request.CreateServerStorageDeviceSlice{
			{
				Action:    "clone",
				Storage:   p.templateUUID,
				Title:     hostname + "-root",
				Tier:      storageTier,
				Size:      storageSizeGB,
				Encrypted: upcloud.FromBool(storageEncrypted),
			},
		},
		Networking: &request.CreateServerNetworking{
			Interfaces: request.CreateServerInterfaceSlice{
				{
					Type: "private",
					Network: p.networkUUID,
					IPAddresses: request.CreateServerIPAddressSlice{
						{Family: "IPv4"},
					},
				},
				{
					Type: "utility",
					IPAddresses: request.CreateServerIPAddressSlice{
						{Family: "IPv4"},
					},
				},
				{
					Type: "public",
					IPAddresses: request.CreateServerIPAddressSlice{
						{Family: "IPv4"},
					},
				},
			},
		},
		UserData: userData,
		Metadata: upcloud.True,
	}

	server, err := p.svc.CreateServer(ctx, createReq)
	return server, err
}

// Delete removes a server and all its attached storage volumes by UUID.
func (p *Provider) Delete(ctx context.Context, serverUUID string) error {
	return p.svc.DeleteServerAndStorages(ctx, &request.DeleteServerAndStoragesRequest{
		UUID: serverUUID,
	})
}

// Get returns server details by UUID.
func (p *Provider) Get(ctx context.Context, serverUUID string) (*upcloud.ServerDetails, error) {
	return p.svc.GetServerDetails(ctx, &request.GetServerDetailsRequest{
		UUID: serverUUID,
	})
}

// List returns all managed servers (those carrying our managed label).
func (p *Provider) List(ctx context.Context) ([]upcloud.ServerDetails, error) {
	servers, err := p.svc.GetServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}

	var result []upcloud.ServerDetails
	for _, s := range servers.Servers {
		details, err := p.svc.GetServerDetails(ctx, &request.GetServerDetailsRequest{
			UUID: s.UUID,
		})
		if err != nil {
			continue
		}
		if isManaged(*details) {
			result = append(result, *details)
		}
	}
	return result, nil
}

// Stop performs a hard power-off of a server by UUID.
func (p *Provider) Stop(ctx context.Context, serverUUID string) error {
	_, err := p.svc.StopServer(ctx, &request.StopServerRequest{
		UUID:     serverUUID,
		StopType: request.ServerStopTypeHard,
		Timeout:  30 * time.Second,
	})
	return err
}

// WaitForStart blocks until the server reaches the started state.
func (p *Provider) WaitForStart(ctx context.Context, serverUUID string) error {
	_, err := p.svc.WaitForServerState(ctx, &request.WaitForServerStateRequest{
		UUID:         serverUUID,
		DesiredState: upcloud.ServerStateStarted,
	})
	return err
}

// WaitForStop blocks until the server reaches the stopped state.
func (p *Provider) WaitForStop(ctx context.Context, serverUUID string) error {
	_, err := p.svc.WaitForServerState(ctx, &request.WaitForServerStateRequest{
		UUID:         serverUUID,
		DesiredState: upcloud.ServerStateStopped,
	})
	return err
}

// isManaged checks whether a server carries the managed label.
func isManaged(s upcloud.ServerDetails) bool {
	for _, l := range s.Labels {
		if l.Key == managedLabelKey && l.Value == managedLabelValue {
			return true
		}
	}
	return false
}
