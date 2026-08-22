package instance

import (
	"context"
	"testing"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"

	v1alpha1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
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
	p := NewProvider(srv, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")

	_, err := p.Create(context.Background(), "karpenter-abc", "4xCPU-8GB", "de-fra1", "#cloud-config", map[string]string{"team": "ai"}, &v1alpha1.StorageSpec{Size: 20, Tier: upcloud.StorageTierStandard})
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
	foundClusterID := false
	foundClusterName := false
	foundGeneratedName := false
	for _, l := range *srv.gotReq.Labels {
		if l.Key == managedLabelKey && l.Value == managedLabelValue {
			foundManaged = true
		}
		if l.Key == "team" && l.Value == "ai" {
			foundTeam = true
		}
		if l.Key == "capu_cluster_id" && l.Value == "cluster-uuid" {
			foundClusterID = true
		}
		if l.Key == "capu_cluster_name" && l.Value == "cluster-name" {
			foundClusterName = true
		}
		if l.Key == "capu_generated_name" && l.Value == "karpenter-abc" {
			foundGeneratedName = true
		}
	}
	if !foundManaged {
		t.Errorf("expected managed_by=%s label on created server", managedLabelValue)
	}
	if !foundTeam {
		t.Errorf("expected caller-provided labels to be forwarded to the server")
	}
	if !foundClusterID {
		t.Errorf("expected capu_cluster_id label on created server")
	}
	if !foundClusterName {
		t.Errorf("expected capu_cluster_name label on created server")
	}
	if !foundGeneratedName {
		t.Errorf("expected capu_generated_name label on created server")
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
	p := NewProvider(srv, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")

	if _, err := p.Create(context.Background(), "karpenter-xyz", "GPU-8xCPU-64GB-1xL4", "de-fra1", "#cloud-config", nil, &v1alpha1.StorageSpec{Size: 100, Tier: upcloud.StorageTierMaxIOPS}); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if srv.gotReq.StorageDevices[0].Tier != string(upcloud.StorageTierMaxIOPS) {
		t.Errorf("expected maxiops tier, got %q", srv.gotReq.StorageDevices[0].Tier)
	}
	if srv.gotReq.StorageDevices[0].Size != 100 {
		t.Errorf("expected 100GB disk, got %d", srv.gotReq.StorageDevices[0].Size)
	}
}

// TestIsManaged verifies that servers with the managed label are detected as managed and servers without it are not.
func TestIsManaged(t *testing.T) {
	managed := upcloud.ServerDetails{Labels: upcloud.LabelSlice{{Key: managedLabelKey, Value: managedLabelValue}}}
	if !isManaged(managed) {
		t.Errorf("expected managed server to be detected")
	}
	unmanaged := upcloud.ServerDetails{Labels: upcloud.LabelSlice{{Key: "other", Value: "x"}}}
	if isManaged(unmanaged) {
		t.Errorf("expected unmanaged server to be rejected")
	}
}
