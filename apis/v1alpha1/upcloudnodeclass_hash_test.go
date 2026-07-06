package v1alpha1

import (
	"testing"

	upcloud "github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
)

func TestHashDeterministic(t *testing.T) {
	a := &UpCloudNodeClass{Spec: UpCloudNodeClassSpec{Zone: "de-fra1", Plan: "2xCPU-4GB"}}
	b := &UpCloudNodeClass{Spec: UpCloudNodeClassSpec{Zone: "de-fra1", Plan: "2xCPU-4GB"}}
	if a.Hash() != b.Hash() {
		t.Errorf("expected identical specs to produce identical hashes")
	}
	if a.Hash() == "" {
		t.Errorf("expected non-empty hash")
	}
}

func TestHashChangesWithSpec(t *testing.T) {
	base := &UpCloudNodeClass{Spec: UpCloudNodeClassSpec{Zone: "de-fra1", Plan: "2xCPU-4GB"}}

	changedZone := base.DeepCopy()
	changedZone.Spec.Zone = "fi-hel2"
	if changedZone.Hash() == base.Hash() {
		t.Errorf("expected zone change to alter hash")
	}

	changedPlan := base.DeepCopy()
	changedPlan.Spec.Plan = "4xCPU-8GB"
	if changedPlan.Hash() == base.Hash() {
		t.Errorf("expected plan change to alter hash")
	}

	changedLabels := base.DeepCopy()
	changedLabels.Spec.Labels = map[string]string{"team": "ai"}
	if changedLabels.Hash() == base.Hash() {
		t.Errorf("expected label change to alter hash")
	}

	changedStorage := base.DeepCopy()
	changedStorage.Spec.StorageGB = 40
	if changedStorage.Hash() == base.Hash() {
		t.Errorf("expected storage change to alter hash")
	}

	changedTaints := base.DeepCopy()
	changedTaints.Spec.Taints = []upcloud.KubernetesTaint{{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"}}
	if changedTaints.Hash() == base.Hash() {
		t.Errorf("expected taint change to alter hash")
	}
}

func TestNodeClassHashAnnotationKey(t *testing.T) {
	if NodeClassHashAnnotationKey != "karpenter.upcloud.com/nodeclass-hash" {
		t.Errorf("unexpected annotation key %q", NodeClassHashAnnotationKey)
	}
}
