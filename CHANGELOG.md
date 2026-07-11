# Changelog

All notable changes to this project will be documented in this file.

## [0.9.6] - 2026-07-11

### Added
- TTL-based disruption controller (`nodeclaimttl`) that replaces Karpenter's built-in `consolidateAfter`. At expiry the controller follows a three-way decision tree: (1) non-DS pods on the node → reset TTL, (2) node empty but a pending/unschedulable pod matches the node's instance type → reset TTL and reuse the node, (3) no match → add a `karpenter.upcloud.com/decommissioning:NoSchedule` taint then delete the NodeClaim. Configurable via `UPCLOUD_NODECLAIM_TTL` (default `50m`). This feature has been implemented to make maximum use of UpCloud's one hour billing cycle.
- HTTP/2 connection retry in the UpCloud API service wrapper: transient connection drops are retried with exponential backoff via `wait.PollUntilContextTimeout`.

### Changed
- The `consolidateAfter` NodePool field is superseded by the TTL controller; example `nodepool.yaml` now sets `consolidationPolicy: Never`. Node lifetime from creation is at most the TTL duration (default 50m) unless the node is actively hosting non-DaemonSet pods or a matching pending pod can reuse it.
- E2e GPU provisioning test now tries up to 4 spot GPU plans in price order when the primary plan reports `SERVER_RESOURCES_UNAVAILABLE`; if all are exhausted the test skips rather than fails.

### Fixed
- CSR approval: `helpers.go` now checks for existing `Approved`/`Denied` conditions before appending, avoiding `"duplicate Approved"` errors when `kube-controller-manager` auto-approves the CSR before the provider can.
- Labels with `/` in the key are now filtered out when sent to the UpCloud API, except for `karpenter.upcloud.com/*` labels. Node capacity type is derived from the plan name (`isSpotPlan`) instead of server labels, fixing `KEY_INVALID` API errors on labels like `node.kubernetes.io/instance-type`.
- Unchecked errors: `serializeTaintsYAML` returns `(string, error)`, kubeletconfig template `Execute` error is captured, `encoder.Encode`/`encoder.Close` errors are checked, `register.go` panics appropriately on `AddToScheme` failure.
- All Go doc comments added/updated across instancetypes, instance, and TTL controller packages.

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
- Server creation now applies the `karpenter.upcloud.com/managed=true` label to provisioned servers, so `List()` correctly discovers Karpenter-managed nodes (previously the label was computed but never sent to the API, causing `List()` to return nothing).

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
