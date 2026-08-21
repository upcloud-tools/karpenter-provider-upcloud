# karpenter-upcloud

Karpenter provider for [UpCloud](https://upcloud.com), packaged as a Helm chart.
It installs the Karpenter controller with the UpCloud cloud provider together
with the `UpCloudNodeClass` CRD, giving your cluster just-in-time, cost-aware node
autoscaling on UpCloud infrastructure.

> [!IMPORTANT]
> **Beta** — in working state but not yet production-ready. Core provisioning,
> scale-from-zero, drift detection, node repair, GPU scheduling, and spot capacity
> all work; broader end-to-end coverage against live clusters is still expanding.

## Prerequisites

- A Kubernetes cluster running **Kubernetes >= 1.31**.
- An [UpCloud](https://upcloud.com) account with an API token that can manage the
  target cluster. This is usually a UpCloud Managed Kubernetes Service (UKS) cluster.
- The `UpCloudNodeClass` CRD (bundled with this chart) and Karpenter's standard APIs must be available in the cluster.
- Cluster-admin privileges in the target namespace to create the controller, its `ServiceAccount`, and the required RBAC.

## Installing

The chart is published to the UpCloud Tools OCI registry. Install it into a dedicated `karpenter` namespace,
supplying your UpCloud API token and the target cluster's UUID:

```sh
helm install karpenter-upcloud oci://ghcr.io/upcloud-tools/charts/karpenter-upcloud \
  --namespace kube-system \
  --set config.clusterUUID=<UPCLOUD_CLUSTER_UUID> \
  --set config.auth.token=<UPCLOUD_API_TOKEN>
```

To upgrade later:

```sh
helm upgrade karpenter-upcloud oci://ghcr.io/upcloud-tools/charts/karpenter-upcloud \
  --namespace kube-system \
  --set config.clusterUUID=<UPCLOUD_CLUSTER_UUID> \
  --set config.auth.token=<UPCLOUD_API_TOKEN>
```

> [!NOTE]
> Either `config.auth.token` **or** `config.auth.existingSecret` must be set. The chart creates a `Secret` from `config.auth.token` when present, or uses the referenced existing `Secret` otherwise.

## Configuration

The following table lists the most commonly overridden values. All values are also validated by the chart's `values.schema.json`.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `config.auth.token` | string | `""` | UpCloud API token. Mutually exclusive with `config.auth.existingSecret`. |
| `config.auth.existingSecret` | string | `""` | Name of an existing `Secret` holding the token. Mutually exclusive with `config.auth.token`. 
| `config.auth.tokenKey` | string | `token` | Key inside the `Secret` that holds the UpCloud API token. |
| `config.clusterUUID` | string | `""` | **Required.** UUID of the UpCloud Kubernetes cluster Karpenter manages. |
| `config.templateUUID` | string | `01000000-0000-4000-8000-000160150100` | OS template UUID (defaults to UKS [Debian] template). |
| `config.allowStarterPlans` | bool | `false` | Include UpCloud STARTER plans in instance type discovery. |
| `config.allowPremiumPlans` | bool | `false` | Include UpCloud PREMIUM plans in instance type discovery. |
| `config.repairToleration` | string | `30m` | How long an unhealthy node is tolerated before repair is triggered. |
| `config.nodeclaimTTL.enabled` | bool | `false` | Use absolute TTLs to expire node claims. **Alpha** |
| `config.nodeclaimTTL.ttl` | string | `50m` | TTL applied to node claims when enabled. |
| `image.repository` | string | `ghcr.io/upcloud-tools/karpenter-provider-upcloud` | Controller image repository. |
| `image.tag` | string | `""` | Controller image tag (defaults to the chart `appVersion` when empty). |
| `image.pullPolicy` | string | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | list | `[]` | Image pull secrets for registry authentication. |
| `resources` | object | `{}` | Standard `requests`/`limits` for the controller container. |
| `extraEnv` | list | `[]` | Extra environment variables for the controller container. |
| `podLabels` | object | `{}` | Extra labels added to the controller pod. |
| `podAnnotations` | object | `{}` | Extra annotations added to the controller pod. |
| `securityContext` | object | `allowPrivilegeEscalation: false`, `capabilities.drop: ["ALL"]`, `readOnlyRootFilesystem: true` | Container security context. |
| `podSecurityContext` | object | `runAsNonRoot: true`, `runAsUser: 65532`, `seccompProfile.type: RuntimeDefault` | Pod security context. |
| `priorityClassName` | string | `system-cluster-critical` | Priority class for the controller pod. |
| `nodeSelector` | object | `{}` | Node selector for scheduling the controller pod. |
| `tolerations` | list | `[]` | Tolerations for scheduling the controller pod. |
| `affinity` | object | schedules away from `karpenter.sh/nodepool` nodes | Pod affinity rules. |
| `topologySpreadConstraints` | list | `[]` | Topology spread constraints for the controller pod. |
| `terminationGracePeriodSeconds` | int | `30` | Termination grace period for the controller pod. |
| `revisionHistoryLimit` | int | `null` | Revision history limit for the Deployment. |
| `livenessProbe` | object | HTTP GET `/healthz` on port `8081` | Liveness probe. |
| `readinessProbe` | object | HTTP GET `/readyz` on port `8081` | Readiness probe. |
| `podDisruptionBudget.enabled` | bool | `false` | Enable a `PodDisruptionBudget` for the controller. |
| `podDisruptionBudget.maxUnavailable` | int/string | `null` | Max unavailable pods for the PDB. |
| `podDisruptionBudget.minAvailable` | int/string | `null` | Min available pods for the PDB. |
| `nameOverride` | string | `""` | Override the chart name. |
| `fullnameOverride` | string | `""` | Override the fully qualified resource name. |
| `commonLabels` | object | `{}` | Common labels added to all resources. |
| `extraObjects` | list | `[]` | Extra Kubernetes objects to deploy (supports Go template expressions). |

## Using the provider

Once installed, define an `UpCloudNodeClass` and a Karpenter `NodePool` to start provisioning UpCloud nodes.
For full provider documentation and examples, see the [project repository](https://github.com/upcloud-tools/karpenter-provider-upcloud).
