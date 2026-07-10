package instance

import (
	"context"
	"testing"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"
)

// captureServer captures the CreateServerRequest for test assertions.
type captureServer struct {
	service.Server
	gotReq *request.CreateServerRequest
}

func (c *captureServer) CreateServer(_ context.Context, r *request.CreateServerRequest) (*upcloud.ServerDetails, error) {
	c.gotReq = r
	return &upcloud.ServerDetails{
		Server: upcloud.Server{UUID: "x", Hostname: r.Hostname, Plan: r.Plan, Zone: r.Zone},
	}, nil
}

// TestCreateSetsManagedLabelAndStorage verifies that the managed label is added, caller labels are forwarded,
// and the default storage tier (standard) and size (20 GB) are applied.
func TestCreateSetsManagedLabelAndStorage(t *testing.T) {
	srv := &captureServer{}
	p := NewProvider(srv, "template-uuid", "network-uuid")

	_, err := p.Create(context.Background(), "karpenter-abc", "4xCPU-8GB", "de-fra1", "#cloud-config", map[string]string{"team": "ai"}, 20, string(upcloud.StorageTierStandard))
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if srv.gotReq == nil {
		t.Fatal("CreateServer was not called")
	}
	if srv.gotReq.Labels == nil {
		t.Fatal("expected labels on CreateServerRequest")
	}
	foundManaged := false
	foundTeam := false
	for _, l := range *srv.gotReq.Labels {
		if l.Key == managedLabel && l.Value == "true" {
			foundManaged = true
		}
		if l.Key == "team" && l.Value == "ai" {
			foundTeam = true
		}
	}
	if !foundManaged {
		t.Errorf("expected managed=%s label on created server", "true")
	}
	if !foundTeam {
		t.Errorf("expected caller-provided labels to be forwarded to the server")
	}
	if len(srv.gotReq.StorageDevices) != 1 {
		t.Errorf("expected one storage device, got %d", len(srv.gotReq.StorageDevices))
	}
	if srv.gotReq.StorageDevices[0].Tier != string(upcloud.StorageTierStandard) {
		t.Errorf("expected standard storage tier default, got %q", srv.gotReq.StorageDevices[0].Tier)
	}
	if srv.gotReq.StorageDevices[0].Size != 20 {
		t.Errorf("expected default 20GB disk, got %d", srv.gotReq.StorageDevices[0].Size)
	}
}

// TestCreateUsesCustomStorage verifies that custom storage tier and size are forwarded to the CreateServer request.
func TestCreateUsesCustomStorage(t *testing.T) {
	srv := &captureServer{}
	p := NewProvider(srv, "template-uuid", "network-uuid")

	if _, err := p.Create(context.Background(), "karpenter-xyz", "GPU-8xCPU-64GB-1xL4", "de-fra1", "#cloud-config", nil, 100, string(upcloud.StorageTierMaxIOPS)); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if srv.gotReq.StorageDevices[0].Tier != string(upcloud.StorageTierMaxIOPS) {
		t.Errorf("expected maxiops tier, got %q", srv.gotReq.StorageDevices[0].Tier)
	}
	if srv.gotReq.StorageDevices[0].Size != 100 {
		t.Errorf("expected 100GB disk, got %d", srv.gotReq.StorageDevices[0].Size)
	}
}

// TestIsManaged verifies that servers with karpenter.upcloud.com/managed=true are detected as managed and servers without it are not.
func TestIsManaged(t *testing.T) {
	managed := upcloud.ServerDetails{Labels: upcloud.LabelSlice{{Key: managedLabel, Value: "true"}}}
	if !isManaged(managed) {
		t.Errorf("expected managed server to be detected")
	}
	unmanaged := upcloud.ServerDetails{Labels: upcloud.LabelSlice{{Key: "other", Value: "x"}}}
	if isManaged(unmanaged) {
		t.Errorf("expected unmanaged server to be rejected")
	}
}
