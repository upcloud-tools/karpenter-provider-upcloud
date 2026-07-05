# Changelog

All notable changes to this project will be documented in this file.

## [0.9.0] - 2026-07-03

- Initial beta release of the UpCloud Karpenter provider
- `UpCloudNodeClass` CRD for specifying zone, plan, labels, taints, and kubelet configuration
- Individual server provisioning via UpCloud compute API (`CreateServer`, `DeleteServerAndStorages`)
- Instance type discovery via `GetPlans()` for scale-from-zero scheduling
- Dynamic pricing via `GetPricesByZone()` with in-memory caching
- Bootstrap token-based cluster join using `kubeadm join` with cloud-init userdata
- UKS Debian 13 as the default node OS template
- Removed `WaitForState()` from server creation — `Create()` returns immediately after API response, reducing per-node provisioning time by ~70s
- Migrated `--provider-id`, `--address`, and `--register-with-taints` from kubelet CLI flags to `KubeletConfiguration` file with runtime `sed` substitution for boot-time values
- Replaced manual taint string serialization with proper `yaml.Marshal` via `gopkg.in/yaml.v3`
- Added `--node-labels` and `--cloud-provider=external` kubelet flags for proper external IP assignment
- Added `nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution` with `karpenter.sh/nodepool DoesNotExist` to prevent karpenter from scheduling on its own managed nodes
