package instance

import (
	"context"
	"fmt"
	"time"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
)

const managedLabel = "karpenter.upcloud.com/managed"

type Provider struct {
	svc          service.Server
	templateUUID string
	networkUUID  string
}

func NewProvider(svc service.Server, templateUUID, networkUUID string) *Provider {
	return &Provider{
		svc:          svc,
		templateUUID: templateUUID,
		networkUUID:  networkUUID,
	}
}

func (p *Provider) Create(ctx context.Context, hostname, plan, zone, userData string, labels map[string]string, storageGB int, storageTier string) (*upcloud.ServerDetails, error) {
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[managedLabel] = "true"

	labelSlice := &upcloud.LabelSlice{}
	for k, v := range labels {
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

func (p *Provider) Delete(ctx context.Context, serverUUID string) error {
	return p.svc.DeleteServerAndStorages(ctx, &request.DeleteServerAndStoragesRequest{
		UUID: serverUUID,
	})
}

func (p *Provider) Get(ctx context.Context, serverUUID string) (*upcloud.ServerDetails, error) {
	return p.svc.GetServerDetails(ctx, &request.GetServerDetailsRequest{
		UUID: serverUUID,
	})
}

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

func (p *Provider) Stop(ctx context.Context, serverUUID string) error {
	_, err := p.svc.StopServer(ctx, &request.StopServerRequest{
		UUID:     serverUUID,
		StopType: request.ServerStopTypeHard,
		Timeout:  30 * time.Second,
	})
	return err
}

func (p *Provider) WaitForStop(ctx context.Context, serverUUID string) error {
	_, err := p.svc.WaitForServerState(ctx, &request.WaitForServerStateRequest{
		UUID:         serverUUID,
		DesiredState: upcloud.ServerStateStopped,
	})
	return err
}

func isManaged(s upcloud.ServerDetails) bool {
	for _, l := range s.Labels {
		if l.Key == managedLabel && l.Value == "true" {
			return true
		}
	}
	return false
}
