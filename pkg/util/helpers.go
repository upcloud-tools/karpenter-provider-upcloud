package util

import "strings"

// IsSpotPlan reports whether a plan name denotes a spot variant.
// UpCloud encodes spot in the plan name (e.g. "GPU-SPOT-8xCPU-64GB-1xL4").
func IsSpotPlan(name string) bool {
	return strings.Contains(strings.ToUpper(name), "SPOT")
}

// InstanceFamily extracts the family prefix from a plan name (e.g. "CLOUDNATIVE-2xCPU-4GB" → "CLOUDNATIVE",
// "GPU-SPOT-8xCPU-64GB-1xL4" → "GPU"). Returns "UNKNOWN" if no known prefix matches.
func InstanceFamily(name string) string {
	for _, prefix := range []string{"CLOUDNATIVE", "GPU", "STARTER", "PREMIUM"} {
		if strings.HasPrefix(name, prefix+"-") || strings.HasPrefix(name, prefix) {
			return prefix
		}
	}
	return "UNKNOWN"
}
