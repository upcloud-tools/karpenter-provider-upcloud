package v1alpha2

// Label domain for UpCloud-specific instance type metadata. NodePools can select on these labels
// to target specific instance characteristics without enumerating plan names.
const (
	LabelDomain = "karpenter.k8s.upcloud"

	// Instance family extracted from the plan name prefix (CLOUDNATIVE, GPU, STARTER, PREMIUM).
	LabelInstanceFamily = LabelDomain + "/instance-family"
	// CPU core count.
	LabelInstanceCPU = LabelDomain + "/instance-cpu"
	// Memory in MiB.
	LabelInstanceMemory = LabelDomain + "/instance-memory"
	// Storage size in GB.
	LabelInstanceStorageSize = LabelDomain + "/instance-storage-size"
	// GPU count (only present on GPU plans).
	LabelInstanceGPUCount = LabelDomain + "/instance-gpu-count"
	// GPU model name (only present on GPU plans, e.g. "L4").
	LabelInstanceGPUModel = LabelDomain + "/instance-gpu-model"
)

// UpCloud server labels applied to each provisioned server for management and identification.
// These labels are visible in the UpCloud API and can be used for filtering and automation.
const (
	// ServerManagedLabelKey marks servers managed by Karpenter.
	ServerManagedLabelKey = "managed_by"
	// ServerManagedLabelValue is the value set on the managed label.
	ServerManagedLabelValue = "karpenter"
	// ServerClusterIDLabelKey stores the cluster UUID for multi-cluster identification.
	ServerClusterIDLabelKey = "capu_cluster_id"
	// ServerClusterNameLabelKey stores the cluster name for human-readable identification.
	ServerClusterNameLabelKey = "capu_cluster_name"
	// ServerGeneratedNameLabelKey stores the Karpenter-generated hostname for traceability.
	ServerGeneratedNameLabelKey = "capu_generated_name"
)
