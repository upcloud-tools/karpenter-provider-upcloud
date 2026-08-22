package util

import "testing"

// TestInstanceFamily verifies the InstanceFamily helper extracts the correct prefix.
func TestInstanceFamily(t *testing.T) {
	tests := []struct {
		planName string
		expected string
	}{
		{"CLOUDNATIVE-2xCPU-4GB", "CLOUDNATIVE"},
		{"GPU-8xCPU-64GB-1xL4", "GPU"},
		{"GPU-SPOT-8xCPU-64GB-1xL4", "GPU"},
		{"STARTER-1xCPU-2GB", "STARTER"},
		{"PREMIUM-2xCPU-4GB", "PREMIUM"},
		{"UNKNOWN-PLAN", "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.planName, func(t *testing.T) {
			got := InstanceFamily(tt.planName)
			if got != tt.expected {
				t.Errorf("InstanceFamily(%q) = %q, want %q", tt.planName, got, tt.expected)
			}
		})
	}
}
