## Features

### Architecture

```
                                     ┌───────────────────────────┐
                                     │     Kubernetes Cluster    │
                                     │      zone: de-fra1        │
                                     │  ┌─────────────────────┐  │
                                     │  │      Karpenter      │  │
                                     │  │   (this operator)   │  │
                                     │  │                     │  │
                                     │  │  ┌───────────────┐  │  │
                                     │  │  │ Bootstrap     │  │  │
                                     │  │  │ Token (Secret)│  │  │
                                     │  │  └───────┬───────┘  │  │
                                     │  └──────────┼──────────┘  │
                                     └─────────────┼─────────────┘
                                                   │
                              CreateServer/Delete  │  cloud-init
                              Server/ListServers   │  with kubeadm join
                                                   │
                                      ┌──────────────────────▼───────────────────────┐
                                      │                 UpCloud API                  │
                                      │                                              │
                                      │  ┌────────────────────────────────────────┐  │
                                      │  │  Server A                              │  │
                                      │  │  (kp-<uuid>)                           │  │
                                      │  │  plan: CLOUDNATIVE-2xCPU-4GB           │  │
                                      │  │  zone: de-fra1                         │  │
                                      │  ├────────────────────────────────────────┤  │
                                      │  │  Server B                              │  │
                                      │  │  (kp-<uuid>)                           │  │
                                      │  │  plan: CLOUDNATIVE-4xCPU-8GB           │  │
                                      │  │  zone: de-fra1                         │  │
                                      │  └────────────────────────────────────────┘  │
                                      └──────────────────────────────────────────────┘
```

### Core flow

1. Karpenter detects unschedulable pods
2. It evaluates pod requirements against known instance types (from `GetPlans()`)
3. `Create()` is called:
   - A bootstrap token Secret is created in `kube-system`
   - Cloud-init userdata is generated with the token + CA cert hash + kubelet args
   - `CreateServer()` provisions a bare UpCloud server with the chosen plan
   - The server boots, runs cloud-init, and joins the cluster via `kubeadm join`
4. `Delete()` calls `DeleteServerAndStorages()` to terminate the server
5. `List()` / `Get()` use `GetServers()` / `GetServerDetails()` filtered by the `managed_by=karpenter` label

### Scale-from-zero provisioning

Karpenter is a Kubernetes node autoscaler that replaces the traditional [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler). Instead of managing node groups and scaling them up/down, Karpenter works at the individual node level:

1. **Watch** — Karpenter watches for unschedulable pods
2. **Evaluate** — It evaluates the pod's scheduling requirements against known instance types
3. **Provision** — It picks the cheapest instance type that fits and provisions a node directly via a cloud provider
4. **Remove** — When nodes are underutilized or expired, Karpenter cordons, drains, and terminates them

Unlike Cluster Autoscaler which is node-group-aware, Karpenter is not. This means it can efficiently pack pods across different instance types without being constrained by pre-defined group boundaries. The result is better utilization, lower cost, and faster scaling.

The key enabler for Karpenter's scheduling is the `GetInstanceTypes()` method on the cloud provider interface. This returns every available instance type with its CPU, memory, GPU, and pricing. Karpenter uses this to simulate pod placement and choose the optimal instance type. This is called **scale-from-zero**.

In this provider, `GetInstanceTypes()` returns cached instance types discovered at startup via `Refresh()`, which calls `GetPlans()` to fetch all server plans with CPU, RAM, and GPU specs, then `GetPricesByZone()` for pricing. Karpenter can optimally place pods onto any plan without any servers running. The cache is refreshed periodically (pricing cached with a 30-minute TTL).

### Full plan catalog as instance types

`GetInstanceTypes()` discovers all plans via `GetPlans()` and surfaces them as Karpenter instance types. All plan families are included: `CLOUDNATIVE-*`, `GPU-*` (including `GPU-SPOT-*`), `STARTER-*`, and `PREMIUM-*`. Use NodePool requirements with the instance type labels to select specific families or characteristics.

Each instance type carries UpCloud-specific labels that NodePools can use for selection without enumerating plan names:

| Label | Example | Description |
|-------|---------|-------------|
| `karpenter.k8s.upcloud/instance-family` | `CLOUDNATIVE`, `GPU`, `STARTER`, `PREMIUM` | Plan family extracted from the name prefix |
| `karpenter.k8s.upcloud/instance-cpu` | `2`, `4`, `8` | CPU core count |
| `karpenter.k8s.upcloud/instance-memory` | `4096`, `8192` | Memory in MiB |
| `karpenter.k8s.upcloud/instance-storage-size` | `0`, `50`, `100` | Storage size in GB |
| `karpenter.k8s.upcloud/instance-gpu-count` | `1`, `2` | GPU count (GPU plans only) |
| `karpenter.k8s.upcloud/instance-gpu-model` | `NVIDIA L4` | GPU model (GPU plans only) |

Example NodePool selecting any GPU instance with at least 4 CPUs:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
spec:
  template:
    spec:
      requirements:
        - key: karpenter.k8s.upcloud/instance-family
          operator: In
          values: ["GPU"]
        - key: karpenter.k8s.upcloud/instance-cpu
          operator: Gt
          values: ["3"]
```

### GPU scheduling

GPU plans (`GPU-*` / `GPU-SPOT-*`) advertise `nvidia.com/gpu` capacity, so pods requesting GPU resources schedule onto them. The GPU device plugin must be installed in the cluster for the resource to be consumable on the node.

> `CLOUDNATIVE-*` / GPU plans report `storage_size: 0`; the node boot disk is still provisioned from the configurable `storage.size` (default 20GB).

### Spot instance support

UpCloud exposes spot capacity as dedicated plan names (e.g. `GPU-SPOT-8xCPU-64GB-1xL4`). The provider
surfaces each plan as its own instance type: on-demand plans carry `karpenter.sh/capacity-type: on-demand`
and spot plans carry `karpenter.sh/capacity-type: spot`. A NodePool requesting `karpenter.sh/capacity-type: spot` is
matched by Karpenter's scheduler to spot plans only; when no spot plan matches the requested shape, no
instance type is found and the pod stays unscheduled. Spot pricing is taken from the live catalog and used for cost-aware scheduling.

> To run a NodePool on spot, set:
>
> ```yaml
> spec:
>   template:
>     spec:
>       requirements:
>         - key: karpenter.sh/capacity-type
>           operator: In
>           values:
>             - spot
> ```

### Node storage configuration

Each node gets a single root disk cloned from the OS template. The storage configuration is set per `UpCloudNodeClass`:

```yaml
storage:
  size: 20        # disk size in GB, defaults to 20
  tier: standard    # standard (default), maxiops, or hdd
  encrypted: true   # optional, enables encryption at rest
```

Karpenter does not size disks from pod storage requests; the disk is a fixed, configurable value and is advertised as the node's `ephemeral-storage` capacity. PersistentVolumeClaims are provisioned by the CSI driver and do not affect node selection.

### Drift detection

When an `UpCloudNodeClass` is updated, the provider detects the change and recycles the affected nodes. At `Create()` time the provider stamps the NodeClaim with the hash of the `UpCloudNodeClass` spec (annotation `karpenter.k8s.upcloud/nodeclass-hash`).
On every reconciliation `IsDrifted()` compares that stored hash against the live `UpCloudNodeClass`. If they differ, the NodeClaim is marked drifted and Karpenter cordons, drains, and terminates it so a replacement is launched with the new config.

The following fields trigger drift when changed: `zone`, `plan`, `storage`, `sshKeys`, `kubeletArgs`, `labels`, and `taints`.

Nodes created before drift detection existed carry no hash annotation and are left untouched to avoid disrupting running workloads.

### Node repair

Karpenter's built-in `node.health` controller calls the provider's `RepairPolicies()` and force-terminates (then replaces) any node that stays in an unhealthy state past its toleration window. This provider watches the standard Kubernetes `Ready` condition:

- `Ready = False` (NotReady — kubelet down or the node never joined) for longer than the toleration, or
- `Ready = Unknown` (kubelet stopped reporting) for longer than the toleration.

The toleration defaults to **30 minutes** and is configurable via `UPCLOUD_REPAIR_TOLERATION` (any Go duration string, e.g. `15m`, `1h`). Node *termination* (a node with a deletion timestamp that won't go away) is handled separately by Karpenter's built-in `node.termination` controller, so it is not part of `RepairPolicies()`.

### NodeClaim TTL (alpha)

This provider ships an optional absolute-lifetime TTL controller for NodeClaims as an alternative to Karpenter's built-in consolidation, to make maximum and optimal use of UpCloud's hourly billing cycle. The controller is an **alpha** release. The core logic and unit tests are solid, but e2e coverage against live clusters still needs more testing.

The controller is disabled by default. Enable it by setting `UPCLOUD_NODECLAIM_TTL` to a duration value (e.g. `50m`, `1h`) on the operator.

When the TTL controller is active, set your NodePool's `disruption.consolidationPolicy` to `Never` to prevent Karpenter's built-in
disruption from fighting with the TTL eviction.

> [!NOTE]
> **Alpha** — opt-in only. Help test it by running the e2e suite with
> `UPCLOUD_E2E_PROVISION=1 go test -tags e2e ./test/e2e/ -run TestLiveNodeClaimTTL -v -timeout 20m`.
> Bug reports and fixes are very welcome.