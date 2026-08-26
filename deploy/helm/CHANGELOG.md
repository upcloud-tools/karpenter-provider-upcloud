# Helm chart changelog

## [1.3.0] - 2026-08-26

### Fixed
- **Breaking**: `nodeClass.storage` now defaults to `null` instead of `{size: 20, tier: standard, encrypted: false}`. Plans with bundled storage (STARTER, PREMIUM, etc.) now work without custom storage configuration. Users of flexible-storage plans (CLOUDNATIVE, GPU) must explicitly set storage if they want custom disk size/tier.
- Plan-aware storage handling: `size` and `tier` are ignored for plans with bundled storage; `encrypted` is supported on all plans

### Changed
- `nodeClass.storage` schema now allows `null` or object with optional sub-keys
- NodeClass template conditionally renders storage block and individual keys only when set
- Updated documentation to clarify which storage fields apply to which plan types

## [1.2.0] - 2026-08-26

### Added
- Core Karpenter CRDs are now bundled with the chart, making it self-contained:
  - `NodePool` (`karpenter.sh/v1`)
  - `NodeClaim` (`karpenter.sh/v1`)
  - `NodeOverlay` (`karpenter.sh/v1`)
  - `CapacityBuffer` (`autoscaling.x-k8s.io/v1beta1`, feature-gated)
- CRDs sourced from the exact Karpenter dependency the controller is built against, so schemas always match the binary
- `artifacthub.io/crds` annotation extended to list all bundled CRDs

### Notes
- Installing this chart alongside upstream Karpenter charts is safe — identical CRDs apply idempotently

## [1.1.2] - 2026-08-23

### Changed
- Maintenance release to add sigstore asset

## [1.1.1] - 2026-08-23

### Added
- `artifacthub.io/crds` annotation to display CRD information on Artifact Hub

### Fixed
- Chart signing now visible on Artifact Hub (signatures stored in OCI registry)

## [1.1.0] - 2026-08-23

### Changed
- **Breaking**: `UpCloudNodeClass` storage configuration refactored to nested `storage` struct:
  - `storageGB` → `storage.size`
  - `storageTier` → `storage.tier`
- **Breaking**: `appVersion` in `Chart.yaml` now includes the `v` prefix (e.g., `v1.0.0` instead of `1.0.0`)
- `serverGroupUUID` replaces `antiAffinity` boolean in `UpCloudNodeClass`
- Updated `NOTES.txt` with current `v1alpha2` API references and guidance for new options

### Added
- Optional `UpCloudNodeClass` creation via `nodeClass.enabled` (default `false`)
- Optional `NodePool` creation via `nodePool.enabled` (default `false`)
- `nodeClass.zone` validation. Required when `nodeClass.enabled=true`
- `nodeclass-nodepool_test.yaml` with 8 unit tests covering enabled/disabled, custom values, labels, and serverGroupUUID omission
- `storage.encrypted` (boolean, optional)
- `serverGroupUUID` field in `UpCloudNodeClass` for anti-affinity placement via server groups

## [1.0.2] - 2026-08-22

### Added
- Dedicated chart `README.md`

## [1.0.1] - 2026-08-21

### Changed
- Removed unsupported artifacthub category

## [1.0.0] - 2026-08-19

### Breaking changes
- Provider-specific values moved under `config` subsection:
  - `clusterUUID` → `config.clusterUUID`
  - `templateUUID` → `config.templateUUID`
  - `nodeclaimTTL` → `config.nodeclaimTTL`
  - `repairToleration` → `config.repairToleration`
  - `allowStarterPlans` → `config.allowStarterPlans`
  - `allowPremiumPlans` → `config.allowPremiumPlans`
- Credential values moved under `config.auth` subsection:
  - `existingSecret` → `config.auth.existingSecret`
  - `existingSecretTokenKey` → `config.auth.tokenKey`
  - `credentials.token` → `config.auth.token`

### Added
- `config.auth.tokenKey` value (default `token`) — key within the secret that holds the UpCloud API token
- `config.repairToleration` value (default `30m`) — controls how long to wait before replacing unhealthy nodes
- `config.allowStarterPlans` value (default `false`) — opt-in to include STARTER plans in instance type discovery
- `config.allowPremiumPlans` value (default `false`) — opt-in to include PREMIUM plans in instance type discovery
- `@schema` annotations in `values.yaml` for inline type documentation alongside `values.schema.json` validation

### Changed
- `templateUUID` default set to `01000000-0000-4000-8000-000160150100` (UKS Debian 13)

## [0.1.7] - 2026-08-19

### Added
- `values.schema.json` for Helm values validation
- `commonLabels` applied to all resource metadata
- `imagePullSecrets` for private registry authentication
- `securityContext` and `podSecurityContext` with secure defaults
- `priorityClassName`, `tolerations`, `nodeSelector`, `topologySpreadConstraints`
- `terminationGracePeriodSeconds`, `revisionHistoryLimit`
- `podDisruptionBudget` (opt-in)
- `extraObjects` for deploying arbitrary Kubernetes resources
- `livenessProbe` and `readinessProbe` (port 8081, Karpenter operator health endpoints)
- `NOTES.txt` with post-install instructions
- `.helmignore` to exclude tests from packaged chart
- `.kube-linter.yaml` for lint configuration
- Helm unit tests for all templates
- Artifact Hub metadata annotations

### Changed
- `image.pullPolicy` default changed from `Always` to `IfNotPresent`
- `image.tag` defaults to `appVersion` when empty
- `_helpers.tpl` adds `helm.sh/chart` label, `selectorLabels`, `commonLabels` support, `image` helper
- Deployment uses `selectorLabels` for matchLabels (separate from metadata labels)
- All `toYaml` renders of user-supplied values wrapped with `tpl()` for template expressions
