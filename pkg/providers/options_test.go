package providers

import (
	"testing"
	"time"
)

func TestNewOptionsFromEnvironmentDefaults(t *testing.T) {
	t.Setenv("UPCLOUD_TOKEN", "tok")
	t.Setenv("UPCLOUD_KUBERNETES_CLUSTER_ID", "cluster")
	t.Setenv("UPCLOUD_REPAIR_TOLERATION", "")

	opts, err := NewOptionsFromEnvironment()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Token != "tok" || opts.ClusterUUID != "cluster" {
		t.Errorf("unexpected base options: %+v", opts)
	}
	if opts.RepairToleration != 30*time.Minute {
		t.Errorf("expected default 30m toleration, got %v", opts.RepairToleration)
	}
}

func TestNewOptionsFromEnvironmentCustomToleration(t *testing.T) {
	t.Setenv("UPCLOUD_TOKEN", "tok")
	t.Setenv("UPCLOUD_KUBERNETES_CLUSTER_ID", "cluster")
	t.Setenv("UPCLOUD_REPAIR_TOLERATION", "15m")

	opts, err := NewOptionsFromEnvironment()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.RepairToleration != 15*time.Minute {
		t.Errorf("expected 15m toleration, got %v", opts.RepairToleration)
	}
}

func TestNewOptionsFromEnvironmentInvalidToleration(t *testing.T) {
	t.Setenv("UPCLOUD_TOKEN", "tok")
	t.Setenv("UPCLOUD_KUBERNETES_CLUSTER_ID", "cluster")
	t.Setenv("UPCLOUD_REPAIR_TOLERATION", "not-a-duration")

	if _, err := NewOptionsFromEnvironment(); err == nil {
		t.Fatal("expected error for invalid UPCLOUD_REPAIR_TOLERATION")
	}
}

func TestNewOptionsFromEnvironmentMissingRequired(t *testing.T) {
	t.Setenv("UPCLOUD_TOKEN", "")
	t.Setenv("UPCLOUD_KUBERNETES_CLUSTER_ID", "cluster")

	if _, err := NewOptionsFromEnvironment(); err == nil {
		t.Fatal("expected error for missing UPCLOUD_TOKEN")
	}
}
