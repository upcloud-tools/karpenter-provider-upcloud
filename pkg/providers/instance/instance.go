package instance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
)

const managedLabel = "karpenter.upcloud.com/managed"

// upcloudLabelPrefix is the allowed label namespace for UpCloud server labels.
// Labels with keys outside this namespace that contain a slash are skipped because the UpCloud API rejects special characters in label keys.
const upcloudLabelPrefix = "karpenter.upcloud.com/"

// Provider manages the lifecycle of UpCloud servers (create, delete, get, list, stop).
// Each server is cloned from a template storage device and attached to the cluster network.
type Provider struct {
	svc          service.Server
	templateUUID string
	networkUUID  string
}

// NewProvider creates a Provider that clones the given template storage onto each server and attaches it to the given cluster network.
func NewProvider(svc service.Server, templateUUID, networkUUID string) *Provider {
	return &Provider{
		svc:          svc,
		templateUUID: templateUUID,
		networkUUID:  networkUUID,
	}
}

// Create provisions a new UpCloud server: clones the template storage, attaches private/utility/public networking, injects 
// userdata (containing kubelet config and TLS certs), and applies labels (e.g. karpenter.upcloud.com/managed=true). 
// Labels whose keys contain a slash outside the karpenter.upcloud.com/ namespace are silently dropped because the UpCloud API rejects them.
func (p *Provider) Create(ctx context.Context, hostname, plan, zone, userData string, labels map[string]string, storageGB int, storageTier string) (*upcloud.ServerDetails, error) {
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[managedLabel] = "true"

	labelSlice := &upcloud.LabelSlice{}
	for k, v := range labels {
		// UpCloud label keys cannot contain slashes; skip Kubernetes-internal labels (e.g. node.kubernetes.io/instance-type, 
		// karpenter.sh/capacity-type) but keep the UpCloud provider's own labels (karpenter.upcloud.com/*).
		if strings.Contains(k, "/") && !strings.HasPrefix(k, upcloudLabelPrefix) {
			continue
		}
		*labelSlice = append(*labelSlice, upcloud.Label{Key: k, Value: v})
	}

	createReq := &request.CreateServerRequest{
		Labels:   labelSlice,
		Zone:     zone,
		Hostname: hostname,
		Title:    hostname,
		Plan:     plan,
		StorageDevices: request.CreateServerStorageDeviceSlice{
			{
				Action:  "clone",
				Storage: p.templateUUID,
				Title:   hostname + "-root",
				Tier:    storageTier,
				Size:    storageGB,
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

	return p.svc.CreateServer(ctx, createReq)
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

// List returns all managed servers (those carrying the karpenter.upcloud.com/managed label).
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
		if l.Key == managedLabel && l.Value == "true" {
			return true
		}
	}
	return false
}
