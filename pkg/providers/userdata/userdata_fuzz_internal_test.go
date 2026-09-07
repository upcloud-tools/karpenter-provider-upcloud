package userdata

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
)

// Cert inputs are held at fixed constants: they pass through base64 encoding on the way into
// the template, so they are YAML-safe by construction and add no fuzzing signal.
const (
	fuzzCACertPEM      = "-----BEGIN CERTIFICATE-----\nZnV6eg==\n-----END CERTIFICATE-----\n"
	fuzzKubeletCertPEM = "-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"
	fuzzKubeletKeyPEM  = "-----BEGIN RSA PRIVATE KEY-----\nYmFy\n-----END RSA PRIVATE KEY-----\n"
)

// FuzzUserdataGenerate fuzzes the user-controllable inputs that are interpolated into the generated cloud-init document and asserts
// the contract: either generation is cleanly rejected, or the result is valid YAML with the expected top-level structure.
// Label, taint, and endpoint values are the interesting surface: they land in YAML block scalars and a shell runcmd script,
// so escaping bugs show up here as parse failures.
func FuzzUserdataGenerate(f *testing.F) {
	f.Add("karpenter.sh/nodepool", "default", "dedicated", "gpu", string(corev1.TaintEffectNoSchedule), "https://10.0.0.1:6443")
	f.Add("", "", "", "", "", "")
	f.Add(`a"b`, `x'y`, "t`t", `v$HOME`, "NoExecute", "https://example.com")
	f.Add("label", "x\ny", "taint", "`id`", "ScheduleDont", "not a url")
	f.Add("label", "$(rm -rf /)", "key", "#comment", string(corev1.TaintEffectPreferNoSchedule), "https://example.com\nextra: true")
	f.Add("very-long-key", "v", "k", "very-long-value", "NoSchedule", "https://a:443")
	// YAML non-characters: valid UTF-8 but rejected by the go-yaml reader (F1 regression seeds).
	f.Add("k", "v\uFFFE", "k2", "v2", "NoSchedule", "https://a:443")
	f.Add("k", "v", "k\uFFFF", "v2", "NoSchedule", "https://a:443")
	f.Add("k", "v", "k2", "v2", "No\x7fSchedule", "https://a:443\uFFFE")
	// Shell-significant characters in labels: rejected so /etc/default/kubelet cannot be corrupted (regression seeds).
	f.Add("k\"1", "v$2", "k3", "v4`x", "NoSchedule", "https://a:443")
	f.Add("k5", `v6\x`, "k7", "v8", "NoSchedule", "https://a:443")

	f.Fuzz(func(t *testing.T, labelKey, labelValue, taintKey, taintValue, taintEffect, endpoint string) {
		p := NewProvider()
		out, err := p.Generate(&Options{
			ClusterEndpoint:   endpoint,
			CACertPEM:         fuzzCACertPEM,
			KubeletClientCert: fuzzKubeletCertPEM,
			KubeletClientKey:  fuzzKubeletKeyPEM,
			Labels:            map[string]string{labelKey: labelValue},
			Taints:            []corev1.Taint{{Key: taintKey, Value: taintValue, Effect: corev1.TaintEffect(taintEffect)}},
		})
		if err != nil {
			require.Empty(t, out, "on rejection the provider must return no document")
			return
		}

		var doc map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(out), &doc), "generated cloud-init must be valid YAML")
		require.Contains(t, doc, "write_files")
		require.Contains(t, doc, "runcmd")
	})
}
