package userdata

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"text/template"

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

func (p *Provider) Generate(opts *Options) (string, error) {
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
		"NodeLabels":      serializeLabels(opts.Labels),
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
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
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
    NODE_LABELS="{{ .NodeLabels }}"

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
