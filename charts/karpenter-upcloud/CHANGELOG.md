# Helm chart changelog

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
