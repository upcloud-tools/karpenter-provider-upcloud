package util

import "testing"

// FuzzIsSpotPlan fuzzes plan-name spot detection to ensure it never panics on arbitrary input.
func FuzzIsSpotPlan(f *testing.F) {
	f.Add("1xCPU-2GB")
	f.Add("GPU-SPOT-8xCPU-64GB-1xL4")
	f.Add("SPOT")
	f.Add("spot")
	f.Add("")

	f.Fuzz(func(t *testing.T, name string) {
		_ = IsSpotPlan(name)
	})
}

// FuzzInstanceFamily fuzzes plan-family extraction, asserting the result always stays within the known set.
func FuzzInstanceFamily(f *testing.F) {
	f.Add("CLOUDNATIVE-2xCPU-4GB")
	f.Add("GPU-SPOT-8xCPU-64GB-1xL4")
	f.Add("STARTER-1xCPU-2GB")
	f.Add("PREMIUM-1xCPU-2GB")
	f.Add("GPUX-1xCPU-2GB")
	f.Add("")

	f.Fuzz(func(t *testing.T, name string) {
		switch InstanceFamily(name) {
		case "CLOUDNATIVE", "GPU", "STARTER", "PREMIUM", "UNKNOWN":
		default:
			t.Fatalf("InstanceFamily(%q) returned an unexpected family", name)
		}
	})
}
