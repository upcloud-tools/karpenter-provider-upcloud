package instance

import (
	"context"
	"testing"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/request"
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud/service"

	v1alpha2 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"
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
// and explicitly set storage tier and size are forwarded to the CreateServer request.
func TestCreateSetsManagedLabelAndStorage(t *testing.T) {
	srv := &captureServer{}
	p := NewProvider(srv, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")

	_, err := p.Create(context.Background(), "karpenter-abc", "4xCPU-8GB", "de-fra1", "#cloud-config", "", map[string]string{"team": "ai"}, &v1alpha2.StorageSpec{Size: 20, Tier: upcloud.StorageTierStandard})
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
		if l.Key == v1alpha2.ServerManagedLabelKey && l.Value == v1alpha2.ServerManagedLabelValue {
			foundManaged = true
		}
		if l.Key == "team" && l.Value == "ai" {
			foundTeam = true
		}
		if l.Key == v1alpha2.ServerClusterIDLabelKey && l.Value == "cluster-uuid" {
			foundClusterID = true
		}
		if l.Key == v1alpha2.ServerClusterNameLabelKey && l.Value == "cluster-name" {
			foundClusterName = true
		}
		if l.Key == v1alpha2.ServerGeneratedNameLabelKey && l.Value == "karpenter-abc" {
			foundGeneratedName = true
		}
	}
	if !foundManaged {
		t.Errorf("expected managed_by=%s label on created server", v1alpha2.ServerManagedLabelValue)
	}
	if !foundTeam {
		t.Errorf("expected caller-provided labels to be forwarded to the server")
	}
	if !foundClusterID {
		t.Errorf("expected %s label on created server", v1alpha2.ServerClusterIDLabelKey)
	}
	if !foundClusterName {
		t.Errorf("expected %s label on created server", v1alpha2.ServerClusterNameLabelKey)
	}
	if !foundGeneratedName {
		t.Errorf("expected %s label on created server", v1alpha2.ServerGeneratedNameLabelKey)
	}
	if len(srv.gotReq.StorageDevices) != 1 {
		t.Errorf("expected one storage device, got %d", len(srv.gotReq.StorageDevices))
	}
	if srv.gotReq.StorageDevices[0].Tier != string(upcloud.StorageTierStandard) {
		t.Errorf("expected standard storage tier, got %q", srv.gotReq.StorageDevices[0].Tier)
	}
	if srv.gotReq.StorageDevices[0].Size != 20 {
		t.Errorf("expected 20GB disk, got %d", srv.gotReq.StorageDevices[0].Size)
	}
}

// TestCreateWithNilStorage verifies that when storage is nil, no size/tier/encrypted are set on the device
// (letting UpCloud use plan-bundled storage for STARTER/PREMIUM plans).
func TestCreateWithNilStorage(t *testing.T) {
	srv := &captureServer{}
	p := NewProvider(srv, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")

	_, err := p.Create(context.Background(), "karpenter-abc", "STARTER-2xCPU-8GB", "de-fra1", "#cloud-config", "", nil, nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if srv.gotReq == nil {
		t.Fatal("CreateServer was not called")
	}
	if len(srv.gotReq.StorageDevices) != 1 {
		t.Errorf("expected one storage device, got %d", len(srv.gotReq.StorageDevices))
	}
	dev := srv.gotReq.StorageDevices[0]
	if dev.Tier != "" {
		t.Errorf("expected no tier when storage is nil, got %q", dev.Tier)
	}
	if dev.Size != 0 {
		t.Errorf("expected no size when storage is nil, got %d", dev.Size)
	}
	if dev.Action != "clone" {
		t.Errorf("expected clone action, got %q", dev.Action)
	}
	if dev.Storage != "template-uuid" {
		t.Errorf("expected template UUID, got %q", dev.Storage)
	}
}

// TestCreateWithPartialStorage verifies that only explicitly set storage fields are forwarded.
func TestCreateWithPartialStorage(t *testing.T) {
	srv := &captureServer{}
	p := NewProvider(srv, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")

	// Only tier set
	_, err := p.Create(context.Background(), "karpenter-abc", "CLOUDNATIVE-2xCPU-4GB", "de-fra1", "#cloud-config", "", nil, &v1alpha2.StorageSpec{Tier: upcloud.StorageTierMaxIOPS})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	dev := srv.gotReq.StorageDevices[0]
	if dev.Tier != string(upcloud.StorageTierMaxIOPS) {
		t.Errorf("expected maxiops tier, got %q", dev.Tier)
	}
	if dev.Size != 0 {
		t.Errorf("expected no size when not set, got %d", dev.Size)
	}

	// Only size set
	srv2 := &captureServer{}
	p2 := NewProvider(srv2, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")
	_, err = p2.Create(context.Background(), "karpenter-abc", "CLOUDNATIVE-2xCPU-4GB", "de-fra1", "#cloud-config", "", nil, &v1alpha2.StorageSpec{Size: 50})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	dev2 := srv2.gotReq.StorageDevices[0]
	if dev2.Tier != "" {
		t.Errorf("expected no tier when not set, got %q", dev2.Tier)
	}
	if dev2.Size != 50 {
		t.Errorf("expected 50GB size, got %d", dev2.Size)
	}
}

// TestCreateWithEncryptedStorage verifies that encrypted is only set when explicitly configured.
func TestCreateWithEncryptedStorage(t *testing.T) {
	srv := &captureServer{}
	p := NewProvider(srv, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")

	// Encrypted explicitly true
	encrypted := true
	_, err := p.Create(context.Background(), "karpenter-abc", "CLOUDNATIVE-2xCPU-4GB", "de-fra1", "#cloud-config", "", nil, &v1alpha2.StorageSpec{Tier: upcloud.StorageTierMaxIOPS, Encrypted: &encrypted})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	dev := srv.gotReq.StorageDevices[0]
	if dev.Encrypted != upcloud.True {
		t.Errorf("expected encrypted=true when explicitly set, got %v", dev.Encrypted)
	}

	// Encrypted explicitly false
	srv2 := &captureServer{}
	p2 := NewProvider(srv2, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")
	notEncrypted := false
	_, err = p2.Create(context.Background(), "karpenter-abc", "CLOUDNATIVE-2xCPU-4GB", "de-fra1", "#cloud-config", "", nil, &v1alpha2.StorageSpec{Tier: upcloud.StorageTierMaxIOPS, Encrypted: &notEncrypted})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	dev2 := srv2.gotReq.StorageDevices[0]
	if dev2.Encrypted != upcloud.False {
		t.Errorf("expected encrypted=false when explicitly set to false, got %v", dev2.Encrypted)
	}

	// Encrypted not set
	srv3 := &captureServer{}
	p3 := NewProvider(srv3, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")
	_, err = p3.Create(context.Background(), "karpenter-abc", "CLOUDNATIVE-2xCPU-4GB", "de-fra1", "#cloud-config", "", nil, &v1alpha2.StorageSpec{Tier: upcloud.StorageTierMaxIOPS})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	dev3 := srv3.gotReq.StorageDevices[0]
	if dev3.Encrypted != 0 {
		t.Errorf("expected encrypted not set (0) when nil, got %v", dev3.Encrypted)
	}
}

// TestCreateUsesCustomStorage verifies that custom storage tier and size are forwarded to the CreateServer request.
func TestCreateUsesCustomStorage(t *testing.T) {
	srv := &captureServer{}
	p := NewProvider(srv, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")

	if _, err := p.Create(context.Background(), "karpenter-xyz", "GPU-8xCPU-64GB-1xL4", "de-fra1", "#cloud-config", "", nil, &v1alpha2.StorageSpec{Size: 100, Tier: upcloud.StorageTierMaxIOPS}); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if srv.gotReq.StorageDevices[0].Tier != string(upcloud.StorageTierMaxIOPS) {
		t.Errorf("expected maxiops tier, got %q", srv.gotReq.StorageDevices[0].Tier)
	}
	if srv.gotReq.StorageDevices[0].Size != 100 {
		t.Errorf("expected 100GB disk, got %d", srv.gotReq.StorageDevices[0].Size)
	}
}

// TestCreateWithServerGroup verifies that the server group UUID is correctly passed to the CreateServer request.
func TestCreateWithServerGroup(t *testing.T) {
	srv := &captureServer{}
	p := NewProvider(srv, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")

	serverGroupUUID := "test-server-group-uuid-123"
	_, err := p.Create(context.Background(), "karpenter-abc", "4xCPU-8GB", "de-fra1", "#cloud-config", serverGroupUUID, nil, nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if srv.gotReq == nil {
		t.Fatal("CreateServer was not called")
	}
	if srv.gotReq.ServerGroup != serverGroupUUID {
		t.Errorf("expected server group UUID %q, got %q", serverGroupUUID, srv.gotReq.ServerGroup)
	}
}

// TestCreateWithEmptyServerGroup verifies that an empty server group UUID is handled correctly.
func TestCreateWithEmptyServerGroup(t *testing.T) {
	srv := &captureServer{}
	p := NewProvider(srv, "template-uuid", "network-uuid", "cluster-uuid", "cluster-name")

	_, err := p.Create(context.Background(), "karpenter-abc", "4xCPU-8GB", "de-fra1", "#cloud-config", "", nil, nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if srv.gotReq == nil {
		t.Fatal("CreateServer was not called")
	}
	if srv.gotReq.ServerGroup != "" {
		t.Errorf("expected empty server group UUID, got %q", srv.gotReq.ServerGroup)
	}
}

// TestIsManaged verifies that servers with the managed label are detected as managed and servers without it are not.
func TestIsManaged(t *testing.T) {
	managed := upcloud.ServerDetails{Labels: upcloud.LabelSlice{{Key: v1alpha2.ServerManagedLabelKey, Value: v1alpha2.ServerManagedLabelValue}}}
	if !isManaged(managed) {
		t.Errorf("expected managed server to be detected")
	}
	unmanaged := upcloud.ServerDetails{Labels: upcloud.LabelSlice{{Key: "other", Value: "x"}}}
	if isManaged(unmanaged) {
		t.Errorf("expected unmanaged server to be rejected")
	}
}
