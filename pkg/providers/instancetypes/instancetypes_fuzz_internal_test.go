package instancetypes

import (
	"math"
	"testing"

	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"github.com/stretchr/testify/require"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	v1alpha2 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"
)

// memoryBytesLimit is the largest memory amount (in MB) for which MemoryAmount*1024*1024 fits in an int64 without overflowing.
const memoryBytesLimit = math.MaxInt64 / (1024 * 1024)

// FuzzBuildInstanceType fuzzes the UpCloud-plan to InstanceType conversion, asserting the
// built type always has non-negative capacity quantities and a single offering with a valid capacity type.
// Extreme memory values probe the int64 overflow in the MiB→bytes conversion.
func FuzzBuildInstanceType(f *testing.F) {
	f.Add("2xCPU-4GB", 2, 4096, 50, 0, "")
	f.Add("GPU-SPOT-8xCPU-64GB-1xL4", 8, 65536, 100, 1, "NVIDIA L4")
	f.Add("CLOUDNATIVE-2xCPU-4GB", 2, 4096, 0, 0, "")
	f.Add("1xCPU-2GB", 0, 0, 0, 0, "")
	f.Add("overflow", 1, memoryBytesLimit+1, 0, 0, "")
	f.Add("negative", 1, -4096, 0, 0, "")

	f.Fuzz(func(t *testing.T, name string, cores, memoryMB, storageGB, gpuCount int, gpuModel string) {
		plan := upcloud.Plan{
			Name:         name,
			CoreNumber:   cores,
			MemoryAmount: memoryMB,
			StorageSize:  storageGB,
			GPUAmount:    gpuCount,
			GPUModel:     gpuModel,
		}
		p := NewProvider(nil, "fi-hel2")
		it := p.buildInstanceTypeWithPrices(plan, map[string]float64{name: 0.05})
		require.NotNil(t, it)

		require.GreaterOrEqual(t, it.Capacity.Cpu().Sign(), 0, "CPU capacity must never be negative")
		if memoryMB >= 0 && int64(memoryMB) <= memoryBytesLimit {
			require.Equal(t, int64(memoryMB)*1024*1024, it.Capacity.Memory().Value())
		}
		require.GreaterOrEqual(t, it.Capacity.Memory().Sign(), 0, "memory capacity must never be negative")

		gpu, hasGPU := it.Capacity[v1alpha2.ResourceNvidiaGPU]
		require.Equal(t, gpuCount > 0, hasGPU, "nvidia.com/gpu capacity must be present iff the plan has GPUs")
		if hasGPU {
			require.GreaterOrEqual(t, gpu.Sign(), 0, "GPU capacity must never be negative")
		}

		require.Len(t, it.Offerings, 1)
		ct := it.Requirements.Get(karpv1.CapacityTypeLabelKey)
		require.NotNil(t, ct)
		require.Subset(t, []string{karpv1.CapacityTypeOnDemand, karpv1.CapacityTypeSpot}, ct.Values())
	})
}
