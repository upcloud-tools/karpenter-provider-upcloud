package userdata

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestValidateSingleLine(t *testing.T) {
	t.Run("accepts printable and valid UTF-8 values", func(t *testing.T) {
		cases := map[string]string{
			"empty":                "",
			"tab":                  "\t",
			"printable ASCII":      "k=v",
			"double-byte UTF-8":    "é",
			"astral UTF-8":         "🚀",
			"NBSP":                 "\u00a0",
			"shell metacharacters": `x'$(id)"y`,
		}
		for name, v := range cases {
			t.Run(name, func(t *testing.T) {
				if err := validateSingleLine("field", v); err != nil {
					t.Errorf("expected %q to be accepted, got %v", v, err)
				}
			})
		}
	})

	t.Run("rejects line breaks and disallowed controls", func(t *testing.T) {
		cases := map[string]string{
			"LF":                         "a\nb",
			"CR":                         "a\rb",
			"NUL":                        "a\x00b",
			"ESC":                        "a\x1bb",
			"DEL":                        "a\x7fb",
			"C1 U+0080":                  "a\u0080b",
			"C1 NEL U+0085":              "a\u0085b",
			"C1 U+009F":                  "a\u009fb",
			"line separator U+2028":      "a\u2028b",
			"paragraph separator U+2029": "a\u2029b",
			"non-character U+FFFE":       "a\uFFFEb",
			"non-character U+FFFF":       "a\uFFFFb",
		}
		for name, v := range cases {
			t.Run(name, func(t *testing.T) {
				if err := validateSingleLine("field", v); err == nil {
					t.Errorf("expected %q to be rejected", v)
				}
			})
		}
	})

	t.Run("rejects invalid UTF-8 and names the cause", func(t *testing.T) {
		cases := map[string]string{
			"lone lead byte":       "\xd6",
			"invalid continuation": "a\xffb",
		}
		for name, v := range cases {
			t.Run(name, func(t *testing.T) {
				err := validateSingleLine("node labels", v)
				if err == nil {
					t.Fatalf("expected %q to be rejected", v)
				}
				if !strings.Contains(err.Error(), "node labels") || !strings.Contains(err.Error(), "invalid UTF-8") {
					t.Errorf("expected error to name the field and the UTF-8 cause, got %v", err)
				}
			})
		}
	})

	t.Run("error names the offending field", func(t *testing.T) {
		if err := validateSingleLine("taint key", "x\ny"); err == nil || !strings.Contains(err.Error(), "taint key") {
			t.Errorf("expected error to mention the field, got %v", err)
		}
	})
}

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
		if !strings.Contains(out, `NODE_LABELS='custom=yes,topology.kubernetes.io/zone=de-fra1'`) {
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
		if !strings.Contains(out, `NODE_LABELS=''`) {
			t.Errorf("expected empty NODE_LABELS assignment when labels are empty")
		}
		if strings.Contains(out, "topology.kubernetes.io/zone=de-fra1") {
			t.Errorf("expected no label values when labels are empty")
		}
	})

	t.Run("label values are shell-quoted", func(t *testing.T) {
		out, err := NewProvider().Generate(&Options{
			Labels: map[string]string{"evil": `x'y`},
		})
		if err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		if !strings.Contains(out, `NODE_LABELS='evil=x'\''y'`) {
			t.Errorf("expected single-quoted NODE_LABELS with escaped quote, got:\n%s", out)
		}
	})

	t.Run("rejects values that would break the document", func(t *testing.T) {
		cases := map[string]*Options{
			"label value with newline": {Labels: map[string]string{"k": "x\ny"}},
			"label key with newline":   {Labels: map[string]string{"k\nx": "y"}},
			"endpoint with newline":    {ClusterEndpoint: "https://a:6443\nextra: true"},
			"endpoint with NUL":        {ClusterEndpoint: "https://a:6443\x00"},
			"label with invalid UTF-8": {Labels: map[string]string{"\xd6": "0"}},
			"taint effect with DEL":    {Taints: []corev1.Taint{{Key: "k", Value: "v", Effect: "\x7f"}}},
			"label with U+FFFE":        {Labels: map[string]string{"k": "v\uFFFE"}},
			"taint key with U+FFFF":    {Taints: []corev1.Taint{{Key: "k\uFFFF", Value: "v", Effect: "NoSchedule"}}},
			"endpoint with U+FFFE":     {ClusterEndpoint: "https://a:6443\uFFFE"},
			"label with double quote":  {Labels: map[string]string{"k": `a"b`}},
			"label key with dollar":    {Labels: map[string]string{"$key": "v"}},
			"label with backtick":      {Labels: map[string]string{"k": "a`id`b"}},
			"label with backslash":     {Labels: map[string]string{"k": `a\b`}},
		}
		for name, opts := range cases {
			t.Run(name, func(t *testing.T) {
				if out, err := NewProvider().Generate(opts); err == nil || out != "" {
					t.Errorf("expected rejection with empty output, got err=%v out=%q", err, out)
				}
			})
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
