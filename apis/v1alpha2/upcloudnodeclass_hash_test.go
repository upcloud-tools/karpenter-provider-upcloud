package v1alpha2

import (
	"testing"

	upcloud "github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
)

func fullSpec() UpCloudNodeClassSpec {
	return UpCloudNodeClassSpec{
		Zone: "de-fra1",
		Plan: "2xCPU-4GB",
		Storage: &StorageSpec{
			Size: 40,
			Tier: "maxiops",
		},
		SSHKeys:     []string{"ssh-rsa AAAAkey1", "ssh-ed25519 AAAAkey2"},
		KubeletArgs: []upcloud.KubernetesKubeletArg{{Key: "max-pods", Value: "110"}, {Key: "eviction-hard", Value: "memory.available<100Mi"}},
		Labels:      map[string]string{"team": "ai", "env": "prod"},
		Taints:      []upcloud.KubernetesTaint{{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"}, {Key: "batch", Effect: "NoExecute"}},
	}
}

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
	changedStorage.Spec.Storage = &StorageSpec{Size: 40}
	if changedStorage.Hash() == base.Hash() {
		t.Errorf("expected storage change to alter hash")
	}

	changedTaints := base.DeepCopy()
	changedTaints.Spec.Taints = []upcloud.KubernetesTaint{{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"}}
	if changedTaints.Hash() == base.Hash() {
		t.Errorf("expected taint change to alter hash")
	}

	changedServerGroup := base.DeepCopy()
	changedServerGroup.Spec.ServerGroupUUID = "0b4c2e6a-7f3d-4b21-9c5e-8a1d2f3e4b5c"
	if changedServerGroup.Hash() == base.Hash() {
		t.Errorf("expected serverGroupUUID change to alter hash")
	}

	changedUtilityNet := base.DeepCopy()
	utilityNet := true
	changedUtilityNet.Spec.UtilityNetworkAccess = &utilityNet
	if changedUtilityNet.Hash() == base.Hash() {
		t.Errorf("expected utilityNetworkAccess change to alter hash")
	}
}

// TestHashIgnoresSliceOrder pins the v2 hashing property: reordering semantically-equivalent list fields
// must not produce a different digest, so list reordering never triggers false-positive drift.
func TestHashIgnoresSliceOrder(t *testing.T) {
	base := &UpCloudNodeClass{Spec: fullSpec()}

	shuffled := base.DeepCopy()
	shuffled.Spec.SSHKeys[0], shuffled.Spec.SSHKeys[1] = shuffled.Spec.SSHKeys[1], shuffled.Spec.SSHKeys[0]
	if shuffled.Hash() != base.Hash() {
		t.Errorf("expected reordered sshKeys to hash identically")
	}

	shuffled = base.DeepCopy()
	shuffled.Spec.KubeletArgs[0], shuffled.Spec.KubeletArgs[1] = shuffled.Spec.KubeletArgs[1], shuffled.Spec.KubeletArgs[0]
	if shuffled.Hash() != base.Hash() {
		t.Errorf("expected reordered kubeletArgs to hash identically")
	}

	shuffled = base.DeepCopy()
	shuffled.Spec.Taints[0], shuffled.Spec.Taints[1] = shuffled.Spec.Taints[1], shuffled.Spec.Taints[0]
	if shuffled.Hash() != base.Hash() {
		t.Errorf("expected reordered taints to hash identically")
	}
}

// TestHashIgnoresZeroValues pins that omitted and explicitly-zero list/map fields are equivalent.
func TestHashIgnoresZeroValues(t *testing.T) {
	omit := &UpCloudNodeClass{Spec: UpCloudNodeClassSpec{Zone: "de-fra1", Plan: "2xCPU-4GB"}}
	empty := &UpCloudNodeClass{Spec: UpCloudNodeClassSpec{
		Zone:        "de-fra1",
		Plan:        "2xCPU-4GB",
		SSHKeys:     []string{},
		KubeletArgs: []upcloud.KubernetesKubeletArg{},
		Labels:      map[string]string{},
		Taints:      []upcloud.KubernetesTaint{},
	}}
	if omit.Hash() != empty.Hash() {
		t.Errorf("expected empty slices/maps to hash like omitted fields")
	}
}

func TestNodeClassHashAnnotationKey(t *testing.T) {
	if NodeClassHashAnnotationKey != "karpenter.k8s.upcloud/nodeclass-hash" {
		t.Errorf("unexpected annotation key %q", NodeClassHashAnnotationKey)
	}
	if NodeClassHashVersionAnnotationKey != "karpenter.k8s.upcloud/nodeclass-hash-version" {
		t.Errorf("unexpected annotation key %q", NodeClassHashVersionAnnotationKey)
	}
}
