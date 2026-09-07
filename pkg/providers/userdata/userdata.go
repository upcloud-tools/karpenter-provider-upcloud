package userdata

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
)

type Options struct {
	ClusterEndpoint   string
	CACertPEM         string
	KubeletClientCert string
	KubeletClientKey  string
	Labels            map[string]string
	Taints            []corev1.Taint
}

type Provider struct{}

func NewProvider() *Provider {
	return &Provider{}
}

// validateSingleLine rejects values containing line breaks, disallowed control chars, or byte sequences that are not valid UTF-8.
// Such values would split the lines they are interpolated into inside the generated cloud-init document,
// corrupting the YAML structure (or injecting keys into embedded configs) instead of staying inside a single string.
// Tabs are allowed; the rejection set covers everything the go-yaml reader refuses plus line breaks:
// C0 controls other than tab, DEL (0x7F), C1 controls (including U+0085, a line break for libyaml scanners),
// the U+2028/U+2029 line separators, and the U+FFFE/U+FFFF non-characters.
func validateSingleLine(what, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s contains invalid UTF-8", what)
	}
	for _, r := range s {
		if (r < 0x20 && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 || r == 0xFFFE || r == 0xFFFF {
			return fmt.Errorf("%s contains a disallowed control character %q", what, r)
		}
	}
	return nil
}

// shellSingleQuote wraps s in single quotes, escaping embedded single quotes using the standard backslash-quote shell idiom,
// so the value stays one literal token in the generated runcmd script. Labels are additionally checked by validateShellSafe,
// which removes the characters that would be reinterpreted downstream.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// validateShellSafe rejects characters that the shell or systemd reinterpret after runcmd runs:
// the script echoes KUBELET_EXTRA_ARGS (containing the node labels) as a double-quoted value into /etc/default/kubelet,
// which is parsed a second time when kubelet starts, where a double quote flips quoting, a dollar sign or backtick triggers expansion,
// and a backslash escapes. Kubernetes label syntax admits none of them, so valid input is never rejected.
func validateShellSafe(what, s string) error {
	if i := strings.IndexAny(s, "\"$`\\"); i >= 0 {
		return fmt.Errorf("%s contains a shell-significant character %q", what, s[i])
	}
	return nil
}

func (p *Provider) Generate(opts *Options) (string, error) {
	if err := validateSingleLine("cluster endpoint", opts.ClusterEndpoint); err != nil {
		return "", err
	}
	nodeLabels := serializeLabels(opts.Labels)
	if err := validateSingleLine("node labels", nodeLabels); err != nil {
		return "", err
	}
	if err := validateShellSafe("node labels", nodeLabels); err != nil {
		return "", err
	}
	// The YAML encoder escapes most of these values safely, but the encoder and parser disagree
	// on some control characters (e.g. DEL is emitted verbatim yet rejected when reading), so
	// taint inputs are held to the same single-line contract as everything else we interpolate.
	for _, taint := range opts.Taints {
		for _, field := range []struct{ what, value string }{
			{"taint key", taint.Key},
			{"taint value", taint.Value},
			{"taint effect", string(taint.Effect)},
		} {
			if err := validateSingleLine(field.what, field.value); err != nil {
				return "", err
			}
		}
	}

	caCertB64 := base64.StdEncoding.EncodeToString([]byte(opts.CACertPEM))
	certB64 := base64.StdEncoding.EncodeToString([]byte(opts.KubeletClientCert))
	keyB64 := base64.StdEncoding.EncodeToString([]byte(opts.KubeletClientKey))

	kubeletConfigBuf := &bytes.Buffer{}
	taintsYAML, terr := serializeTaintsYAML(opts.Taints)
	if terr != nil {
		return "", fmt.Errorf("serializing taints: %w", terr)
	}
	if err := template.Must(template.New("kubeletconfig").Parse(kubeletConfigTemplate)).Execute(kubeletConfigBuf, map[string]string{
		"TaintsYAML": taintsYAML,
	}); err != nil {
		return "", fmt.Errorf("executing kubeletconfig template: %w", err)
	}
	kubeletConfigIndented := indentLines(kubeletConfigBuf.String(), 6)

	var buf bytes.Buffer
	err := template.Must(template.New("userdata").Parse(cloudInitTemplate)).Execute(&buf, map[string]string{
		"CACertB64":       caCertB64,
		"KubeletCertB64":  certB64,
		"KubeletKeyB64":   keyB64,
		"ClusterEndpoint": opts.ClusterEndpoint,
		"KubeletConfig":   kubeletConfigIndented,
		"NodeLabels":      shellSingleQuote(nodeLabels),
	})
	if err != nil {
		return "", fmt.Errorf("executing userdata template: %w", err)
	}
	return buf.String(), nil
}

func serializeLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(labels))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

type taintEntry struct {
	Key    string `yaml:"key"`
	Value  string `yaml:"value,omitempty"`
	Effect string `yaml:"effect"`
}

type taintsConfig struct {
	RegisterWithTaints []taintEntry `yaml:"registerWithTaints"`
}

func serializeTaintsYAML(taints []corev1.Taint) (string, error) {
	if len(taints) == 0 {
		return "", nil
	}
	entries := make([]taintEntry, len(taints))
	for i, t := range taints {
		entries[i] = taintEntry{Key: t.Key, Value: t.Value, Effect: string(t.Effect)}
	}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(taintsConfig{RegisterWithTaints: entries}); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func indentLines(s string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

const kubeletConfigTemplate = `apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
address: ADDRESS_PLACEHOLDER
providerID: PROVIDER_ID_PLACEHOLDER
authentication:
  anonymous:
    enabled: false
  webhook:
    cacheTTL: 0s
    enabled: true
  x509:
    clientCAFile: /etc/kubernetes/pki/ca.crt
authorization:
  mode: Webhook
  webhook:
    cacheAuthorizedTTL: 0s
    cacheUnauthorizedTTL: 0s
cgroupDriver: systemd
clusterDNS:
- 10.96.0.10
clusterDomain: cluster.local
containerRuntimeEndpoint: unix:///var/run/containerd/containerd.sock
healthzBindAddress: 127.0.0.1
healthzPort: 10248
imageGCHighThresholdPercent: 85
logging:
  verbosity: 0
resolvConf: /run/systemd/resolve/resolv.conf
rotateCertificates: true
staticPodPath: /etc/kubernetes/manifests
{{ .TaintsYAML }}
`

const cloudInitTemplate = `#cloud-config

manage_etc_hosts: false

write_files:
  - path: /etc/kubernetes/pki/ca.crt
    encoding: b64
    content: {{ .CACertB64 }}
  - path: /var/lib/kubelet/pki/kubelet-client.crt
    encoding: b64
    content: {{ .KubeletCertB64 }}
  - path: /var/lib/kubelet/pki/kubelet-client.key
    encoding: b64
    content: {{ .KubeletKeyB64 }}
    permissions: "0600"
  - path: /etc/kubernetes/kubelet.conf
    content: |
      apiVersion: v1
      kind: Config
      clusters:
      - cluster:
          certificate-authority: /etc/kubernetes/pki/ca.crt
          server: {{ .ClusterEndpoint }}
        name: default-cluster
      contexts:
      - context:
          cluster: default-cluster
          namespace: default
          user: default-auth
        name: default-context
      current-context: default-context
      users:
      - name: default-auth
        user:
          client-certificate: /var/lib/kubelet/pki/kubelet-client-current.pem
          client-key: /var/lib/kubelet/pki/kubelet-client-current.pem
  - path: /var/lib/kubelet/config.yaml
    content: |
{{ .KubeletConfig }}
runcmd:
  - |
    # Combine cert + key (UKS convention)
    cat /var/lib/kubelet/pki/kubelet-client.crt /var/lib/kubelet/pki/kubelet-client.key \
      > /var/lib/kubelet/pki/kubelet-client-current.pem
    chmod 600 /var/lib/kubelet/pki/kubelet-client-current.pem

    # Discover private IP via metadata service
    for i in $(curl -s http://169.254.169.254/metadata/v1/network/interfaces/); do
      if [ "$(curl -s http://169.254.169.254/metadata/v1/network/interfaces/$i/type)" = "private" ]; then
        PRIVATE_IP=$(curl -s http://169.254.169.254/metadata/v1/network/interfaces/$i/ip_addresses/1/address)
        break
      fi
    done

    PROVIDER_ID="upcloud:////$(curl -s http://169.254.169.254/metadata/v1/instance_id)"
    NODE_LABELS={{ .NodeLabels }}

    if ! grep -q "$(hostname)" /etc/hosts; then
      echo "$PRIVATE_IP  $(hostname)" >> /etc/hosts
    fi

    sed -i "s|PROVIDER_ID_PLACEHOLDER|$PROVIDER_ID|" /var/lib/kubelet/config.yaml
    sed -i "s|ADDRESS_PLACEHOLDER|$PRIVATE_IP|" /var/lib/kubelet/config.yaml

    KUBELET_EXTRA="--cloud-provider=external"
    if [ -n "$NODE_LABELS" ]; then
      KUBELET_EXTRA="$KUBELET_EXTRA --node-labels=$NODE_LABELS"
    fi

    echo "KUBELET_EXTRA_ARGS=\"$KUBELET_EXTRA\"" > /etc/default/kubelet

    systemctl daemon-reload
    systemctl restart kubelet
`
