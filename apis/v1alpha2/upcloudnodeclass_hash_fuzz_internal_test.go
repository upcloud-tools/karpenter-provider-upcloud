package v1alpha2

import (
	"encoding/json"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// FuzzNodeClassHash decodes fuzzed JSON into a NodeClass spec and asserts that Hash always yields a well-formed 64-bit hex digest
// that is identical for a deep copy of the spec and invariant under reordering of list fields. Drift detection compares this hash
// across reconcile loops and releases, so determinism is a hard invariant and order-sensitivity a false-positive source.
func FuzzNodeClassHash(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"zone":"de-fra1","plan":"2xCPU-4GB"}`))
	f.Add([]byte(`{"zone":"fi-hel2","plan":"GPU-SPOT-8xCPU-64GB-1xL4","storage":{"size":40,"tier":"maxiops","encrypted":true},"sshKeys":["ssh-rsa AAAAB3"],"kubeletArgs":[{"key":"max-pods","value":"110"}],"labels":{"team":"ai","x.y/z":"w"},"taints":[{"key":"dedicated","value":"gpu","effect":"NoSchedule"}],"serverGroupUUID":"0b4c2e6a-7f3d-4b21-9c5e-8a1d2f3e4b5c","utilityNetworkAccess":true}`))
	f.Add([]byte(`{"labels":{"a\nb":"c\"d"},"taints":[{"key":"k","value":"v\n","effect":"bogus"}]}`))
	f.Add([]byte(`{"zone":"de-fra1"`))
	f.Add([]byte(`{"unknown":true}`))
	f.Add([]byte(`{"storage":{"size":-1,"tier":"\u0000"},"kubeletArgs":[]}`))
	f.Add([]byte(`{"sshKeys":["a","b","c"],"kubeletArgs":[{"key":"a","value":"1"},{"key":"b","value":"2"}],"taints":[{"key":"a","effect":"NoSchedule"},{"key":"b","effect":"NoExecute"}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var spec UpCloudNodeClassSpec
		if err := json.Unmarshal(data, &spec); err != nil {
			t.Skip("invalid spec JSON")
		}
		nc := &UpCloudNodeClass{Spec: spec}
		hash := nc.Hash()
		require.Regexp(t, hashPattern, hash, "hash must be a 16-char lowercase hex digest")
		require.Equal(t, hash, nc.DeepCopy().Hash(), "hash must be stable across deep copies")

		reversed := nc.DeepCopy()
		slices.Reverse(reversed.Spec.SSHKeys)
		slices.Reverse(reversed.Spec.KubeletArgs)
		slices.Reverse(reversed.Spec.Taints)
		require.Equal(t, hash, reversed.Hash(), "hash must be invariant under reordering of list fields")
	})
}
