package userdata

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestGenerateIncludesBootstrapSecrets(t *testing.T) {
	opts := &Options{
		ClusterEndpoint:   "https://10.0.0.1:6443",
		CACertPEM:         "CA-CERT-BUNDLE",
		KubeletClientCert: "KUBELET-CLIENT-CERT",
		KubeletClientKey:  "KUBELET-CLIENT-KEY",
		Labels:            map[string]string{"foo": "bar"},
	}

	out, err := NewProvider().Generate(opts)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if !strings.Contains(out, "cloud-config") {
		t.Errorf("expected cloud-config header in output")
	}
	if !strings.Contains(out, "https://10.0.0.1:6443") {
		t.Errorf("expected cluster endpoint in kubelet.conf")
	}
	// CA / cert / key must be base64 encoded (never raw) in the write_files section.
	if strings.Contains(out, "CA-CERT-BUNDLE") || strings.Contains(out, "KUBELET-CLIENT-CERT") || strings.Contains(out, "KUBELET-CLIENT-KEY") {
		t.Errorf("raw secrets leaked into userdata; expected base64-encoded content only")
	}
	if !strings.Contains(out, "Q0EtQ0VSVC1CVU5ETEU=") { // base64("CA-CERT-BUNDLE")
		t.Errorf("expected base64-encoded CA cert in write_files")
	}
	if !strings.Contains(out, "S1VCRUxFVC1DTElFTlQtQ0VSVA==") { // base64("KUBELET-CLIENT-CERT")
		t.Errorf("expected base64-encoded kubelet client cert in write_files")
	}
}

func TestGenerateNodeLabels(t *testing.T) {
	t.Run("with labels", func(t *testing.T) {
		out, err := NewProvider().Generate(&Options{
			Labels: map[string]string{"topology.kubernetes.io/zone": "de-fra1", "custom": "yes"},
		})
		if err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		if !strings.Contains(out, `NODE_LABELS="topology.kubernetes.io/zone=de-fra1,custom=yes"`) {
			t.Errorf("expected NODE_LABELS assignment with comma-joined labels, got:\n%s", out)
		}
		if !strings.Contains(out, "--node-labels=$NODE_LABELS") {
			t.Errorf("expected --node-labels flag referencing NODE_LABELS, got:\n%s", out)
		}
	})

	t.Run("without labels", func(t *testing.T) {
		out, err := NewProvider().Generate(&Options{})
		if err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		if !strings.Contains(out, `NODE_LABELS=""`) {
			t.Errorf("expected empty NODE_LABELS assignment when labels are empty")
		}
		if strings.Contains(out, "topology.kubernetes.io/zone=de-fra1") {
			t.Errorf("expected no label values when labels are empty")
		}
	})
}

func TestGenerateTaints(t *testing.T) {
	taints := []corev1.Taint{
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
	}
	out, err := NewProvider().Generate(&Options{Taints: taints})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if !strings.Contains(out, "registerWithTaints") {
		t.Errorf("expected registerWithTaints in kubelet config when taints present")
	}
	if !strings.Contains(out, "dedicated") || !strings.Contains(out, "NoSchedule") {
		t.Errorf("expected taint key/effect serialized into kubelet config")
	}

	outEmpty, err := NewProvider().Generate(&Options{})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if strings.Contains(outEmpty, "registerWithTaints") {
		t.Errorf("expected no registerWithTaints when taints are empty")
	}
}

func TestSerializeLabels(t *testing.T) {
	if got := serializeLabels(nil); got != "" {
		t.Errorf("expected empty string for nil labels, got %q", got)
	}
	if got := serializeLabels(map[string]string{}); got != "" {
		t.Errorf("expected empty string for empty labels, got %q", got)
	}
	got := serializeLabels(map[string]string{"a": "1", "b": "2"})
	if got != "a=1,b=2" && got != "b=2,a=1" {
		t.Errorf("expected comma-joined labels, got %q", got)
	}
}
