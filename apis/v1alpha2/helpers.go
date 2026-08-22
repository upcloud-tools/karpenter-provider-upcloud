package v1alpha2

import "strings"

// IsSpotPlan reports whether a plan name denotes a spot variant.
// UpCloud encodes spot in the plan name (e.g. "GPU-SPOT-8xCPU-64GB-1xL4").
func IsSpotPlan(name string) bool {
	return strings.Contains(strings.ToUpper(name), "SPOT")
}
