# Changelog

All notable changes to this project will be documented in this file.

The project is still in **Beta**, so expect breaking changes in future releases.

## [1.0.3] - 2026-08-27

### Fixed
- Storage size bug: bundled-storage plans (STARTER, PREMIUM) now correctly use the plan's bundled disk size instead of falling back to the template size.

### Added
- E2E test now verifies that the server's root disk size matches the plan's bundled storage size for bundled-storage plans.

## [1.0.2] - 2026-08-26

### Added
- E2E test for bundled-storage plans validates that STARTER/PREMIUM plans work correctly with `Storage: nil`.
- Comprehensive logging across all e2e tests for better observability during test runs.

### Fixed
- NodeClass storage is now truly optional. The provider no longer invents defaults (20GB/standard) when storage is unset, allowing plans with bundled storage (STARTER, PREMIUM, etc.) to work without custom storage configuration.
- Removed unused `karpenter.sh/created-at` label that contained colons (RFC3339 format like `2026-08-26T15:56:26Z`), causing Kubernetes to reject NodeClaim creation with an error.
- Plan-aware storage handling: when the selected plan has bundled storage (`StorageSize > 0`), the provider strips `size` and `tier` from the storage spec and logs a warning, letting UpCloud use the plan's bundled disk. The `encrypted` field is preserved and supported on all plans.

### Changed
- Instance provider no longer substitutes default storage size/tier/encrypted values. Only explicitly configured fields are sent to the UpCloud API.
- CloudProvider now consults the cached plan metadata to detect bundled-storage plans and strip incompatible storage overrides before passing to the instance provider.
- InstanceTypes provider now caches raw UpCloud plans alongside instance types, exposing a `Get(name)` accessor for plan metadata lookups.
- Updated Makefile to run all e2e tests by default (`E2E_TEST ?= .`), previously only ran `TestLiveCloudProviderCreate`.

## [1.0.1] - 2026-08-23

### Added
- Sigstore asset for chart signing verification

## [1.0.0] - 2026-08-23

### Added
- `karpenter.sh/created-at` label stamped on NodeClaims at creation time. Enables garbage collection to distinguish just-launched servers from orphans.
- `storage.encrypted` field in `UpCloudNodeClass` spec. Enables encryption at rest for the node's root disk.
- `CONTRIBUTING.md` with UpCloud ↔ Karpenter concept map documenting load-bearing design decisions (provider ID format, zone/region parity, two-call deletion, label separation).
- Rich instance type labels for NodePool selection: `karpenter.k8s.upcloud/instance-family`, `instance-cpu`, `instance-memory`, `instance-storage-size`, `instance-gpu-count`, `instance-gpu-model`. NodePools can now select on instance characteristics (e.g. "any GPU instance" or "≥4 CPU") without enumerating plan names.

### Changed
- **Breaking**: API version bumped from `v1alpha1` to `v1alpha2` to reflect breaking changes in the API group rename and storage configuration. All manifests must be updated to use `apiVersion: karpenter.k8s.upcloud/v1alpha2`.
- **Breaking**: API group renamed from `karpenter.upcloud.com` to `karpenter.k8s.upcloud` to align with upstream Karpenter conventions. This affects:
  - CRD API group: `upcloudnodeclasses.karpenter.k8s.upcloud`
  - Annotations: `karpenter.k8s.upcloud/nodeclass-hash`, `karpenter.k8s.upcloud/ttl-reset-at`
  - Taints: `karpenter.k8s.upcloud/decommissioning`
  - NodeClassRef group in NodePool specs
  - RBAC apiGroups
- **Breaking**: Storage configuration refactored from flat fields to nested `storage` struct:
  - `storageGB` → `storage.size`
  - `storageTier` → `storage.tier`
  - `storageEncrypted` → `storage.encrypted` (new)
- **Breaking**: Removed `UPCLOUD_ALLOW_STARTER_PLANS` and `UPCLOUD_ALLOW_PREMIUM_PLANS` environment variables. All plan families are now discovered by default. Use NodePool requirements with instance type labels to select specific families.
- Label separation: Kubernetes-internal labels (topology, instance-type, capacity-type, created-at) are no longer sent to the UpCloud server. Only user-defined labels from `nodeClass.Spec.Labels` plus managed markers (`managed_by`, `capu_*`) reach the server.
- User labels with slashes and dots (e.g. `node.kubernetes.io/test`) are now passed through to the UpCloud server.

## [0.9.6] - 2026-07-11

### Added
- TTL-based disruption controller (`nodeclaimttl`) that replaces Karpenter's built-in `consolidateAfter`. At expiry the controller follows a three-way decision tree: (1) non-DS pods on the node → reset TTL, (2) node empty but a pending/unschedulable pod matches the node's instance type → reset TTL and reuse the node, (3) no match → add a `karpenter.upcloud.com/decommissioning:NoSchedule` taint then delete the NodeClaim. Configurable via `UPCLOUD_NODECLAIM_TTL` (default `50m`). This feature has been implemented to make maximum use of UpCloud's one hour billing cycle.
- HTTP/2 connection retry in the UpCloud API service wrapper: transient connection drops are retried with exponential backoff via `wait.PollUntilContextTimeout`.
- UKS-compatible server labels: provisioned servers now carry `capu_cluster_id`, `capu_cluster_name`, and `capu_generated_name` labels matching UKS node conventions.
- VPA prediction store integration for Karpenter v1.14 compatibility.

### Changed
- The `consolidateAfter` NodePool field is superseded by the TTL controller; example `nodepool.yaml` now sets `consolidationPolicy: Never`. Node lifetime from creation is at most the TTL duration (default 50m) unless the node is actively hosting non-DaemonSet pods or a matching pending pod can reuse it.
- E2e GPU provisioning test now tries up to 4 spot GPU plans in price order when the primary plan reports `SERVER_RESOURCES_UNAVAILABLE`; if all are exhausted the test skips rather than fails.
- Managed label changed from `karpenter-upcloud-com-managed=true` to `managed_by=karpenter`.
- Updated to use [forked Karpenter v1.14](https://github.com/aardbol/karpenter/tree/fix/3121) with fix for [n+1 VM provisioning issue](https://github.com/kubernetes-sigs/karpenter/issues/3121) until [upstream PR #3243](https://github.com/kubernetes-sigs/karpenter/pull/3243) is merged.

### Fixed
- CSR approval: `helpers.go` now checks for existing `Approved`/`Denied` conditions before appending, avoiding `"duplicate Approved"` errors when `kube-controller-manager` auto-approves the CSR before the provider can.
- Labels with `/` in the key are now filtered out when sent to the UpCloud API, except for `karpenter.upcloud.com/*` labels. Node capacity type is derived from the plan name (`isSpotPlan`) instead of server labels, fixing `KEY_INVALID` API errors on labels like `node.kubernetes.io/instance-type`.
- Unchecked errors: `serializeTaintsYAML` returns `(string, error)`, kubeletconfig template `Execute` error is captured, `encoder.Encode`/`encoder.Close` errors are checked, `register.go` panics appropriately on `AddToScheme` failure.
- All Go doc comments added/updated across instancetypes, instance, and TTL controller packages.
- Flaky userdata test: label serialization now uses sorted keys for deterministic output.

## [0.9.5] - 2026-07-08

### Added
- Test foundation: unit tests for cloud-init userdata generation, instance type discovery/pricing, and `UpCloudNodeClass` validation.
- Cloudprovider integration harness exercising `Create`/`Get`/`List`/`Delete`/`GetInstanceTypes`/`IsDrifted` against a fake UpCloud API (no external binaries required).
- Instance provider smoke tests verifying managed-label tagging and storage configuration on server creation.
- Drift detection: `IsDrifted` now recycles NodeClaims when the `UpCloudNodeClass` spec changes (zone, plan, storageGB, storageTier, sshKeys, kubeletArgs, labels, taints). The NodeClass hash is stamped on each NodeClaim at creation (annotation `karpenter.upcloud.com/nodeclass-hash`) and surfaced on `UpCloudNodeClass.status.hash`; legacy nodes without the annotation are left untouched.
- Instance type scope: `GetInstanceTypes()` now uses a CloudNative-first default — `CLOUDNATIVE-*` and GPU plans are included, while `STARTER` and `PREMIUM` plans are excluded unless opted in via `UPCLOUD_ALLOW_STARTER_PLANS` or `UPCLOUD_ALLOW_PREMIUM_PLANS`.
- Node repair: `RepairPolicies()` reports the `Ready` condition (`False` and `Unknown`) so Karpenter's `node.health` controller force-terminates and replaces unhealthy nodes. Toleration defaults to `30m` and is configurable via `UPCLOUD_REPAIR_TOLERATION`.
- GPU support: GPU plans (`GPU-*`, `GPU-SPOT-*`) now advertise `nvidia.com/gpu` capacity on their instance types, so pods requesting GPU resources can be scheduled onto them.
- Configurable node storage: the root disk size (`storageGB`, default 20GB) and tier (`storageTier`, default `standard`) from `UpCloudNodeClass` are now applied to provisioned servers. The disk size is also advertised as the node's `ephemeral-storage` capacity.
- Spot capacity: UpCloud spot plans (e.g. `GPU-SPOT-*`) are surfaced as their own instance types with `karpenter.sh/capacity-type: spot`. When a NodePool requests it, Karpenter's scheduler selects only spot instance types and the provider launches the chosen spot plan.
- End-to-end tests: `test/e2e` exercises instance-type discovery against the real UpCloud API (skipped without `UPCLOUD_TOKEN` / `UPCLOUD_KUBERNETES_CLUSTER_ID`). A gated provisioning test (`TestLiveCloudProviderCreate`) drives the full `cloudprovider.Create` path — kubelet cert (self-approved CSR), userdata, real `CreateServer` — against a live cluster, and cleans the server up afterwards. It only runs when `UPCLOUD_E2E_PROVISION=1` is set.

### Fixed
- Server creation now applies the `karpenter.k8s.upcloud/managed=true` label to provisioned servers, so `List()` correctly discovers Karpenter-managed nodes (previously the label was computed but never sent to the API, causing `List()` to return nothing).

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
