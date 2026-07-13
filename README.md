# Karpenter Provider for UpCloud — Beta

[![Build](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/test.yaml/badge.svg)](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/test.yaml)
[![Go Lint](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/lint-golang.yaml/badge.svg)](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/lint-golang.yaml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/upcloud-tools/karpenter-provider-upcloud/badge)](https://scorecard.dev/viewer/?uri=github.com/upcloud-tools/karpenter-provider-upcloud)

Karpenter provider implementation for [UpCloud](https://upcloud.com), enabling efficient, just-in-time node autoscaling.

> **Beta** — in working state but not yet production-ready. Core provisioning, scale-from-zero,
> drift detection, node repair, GPU scheduling, and spot capacity all work; broader end-to-end
> coverage against live clusters is still being expanded.

**Note** that the provider suffers from this upstream bug: https://github.com/kubernetes-sigs/karpenter/issues/3121. This results in n + 1 VM being started, so for one pod, two VMs will be started, for 2 pods, 3 VMs will be started, and so on. The extra VM runs for its TTL duration (default 50m) before being cleaned up. As soon as a fix is available, I'll update this provider ASAP.

## How Karpenter Works

Karpenter is a Kubernetes node autoscaler that replaces the traditional
[Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler).
Instead of managing node groups and scaling them up/down, Karpenter works at the
individual node level:

1. **Watch** — Karpenter watches for unschedulable pods (pods that the Kubernetes
   scheduler couldn't place due to resource constraints, taints, affinities, etc.)
2. **Evaluate** — It evaluates the pod's scheduling requirements (resource requests,
   node selectors, affinities, tolerations, topology spread) against known instance types
3. **Provision** — It picks the cheapest instance type that fits and provisions a node
   directly via a cloud provider
4. **Remove** — When nodes are underutilized or expired, Karpenter cordons, drains,
   and terminates them

Unlike Cluster Autoscaler which is node-group-aware, Karpenter is not. This means it
can bin-pack pods across different instance types without being constrained by
pre-defined group boundaries. The result is better utilization, lower cost, and
faster scaling.

The key enabler for Karpenter's scheduling is the `GetInstanceTypes()` method on the
cloud provider interface. This returns every available instance type with its CPU,
memory, GPU, and pricing — even when zero nodes exist in the cluster. Karpenter uses
this to simulate pod placement and choose the optimal instance type. This is called
**scale-from-zero**.

## How This Implementation Works

This provider integrates Karpenter with UpCloud's compute API to provision
individual servers directly — the same approach every other Karpenter provider uses.

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
5. `List()` / `Get()` use `GetServers()` / `GetServerDetails()` filtered by the
    `karpenter.upcloud.com/managed` label

### Drift detection

When an `UpCloudNodeClass` is updated, the provider detects the change and recycles the affected nodes. At `Create()` time the provider stamps the NodeClaim with the hash of the `UpCloudNodeClass` spec (annotation `karpenter.upcloud.com/nodeclass-hash`).
On every reconciliation `IsDrifted()` compares that stored hash against the live `UpCloudNodeClass`. If they differ, the NodeClaim is marked drifted and Karpenter cordons, drains, and terminates it so a replacement is launched with the new config.

The following fields trigger drift when changed: `zone`, `plan`, `storageGB`, `storageTier`, `sshKeys`, `kubeletArgs`, `labels`, and `taints`.

Nodes created before drift detection existed carry no hash annotation and are left untouched to avoid disrupting running workloads.

### Instance type scope

`GetInstanceTypes()` discovers plans via `GetPlans()` and surfaces them as Karpenter instance types. The default scope is **CloudNative-first**:

- **Included by default:** `CLOUDNATIVE-*` plans and GPU plans (`gpu_amount > 0`, including `GPU-SPOT-*`).
- **Excluded by default:** `STARTER` and `PREMIUM` plans — opt in per family below.

Opt in to additional families with environment variables on the provider:

| Variable | Effect |
|----------|--------|
| `UPCLOUD_ALLOW_STARTER_PLANS` | Also include `STARTER-*` plans (e.g. `true`). |
| `UPCLOUD_ALLOW_PREMIUM_PLANS` | Also include `PREMIUM-*` plans (e.g. `true`). |

> GPU plans (`GPU-*` / `GPU-SPOT-*`) are included by default and advertise `nvidia.com/gpu` capacity, so pods requesting GPU resources schedule onto them. The GPU device plugin must be installed in the cluster for the resource to be consumable on the node.
> `CLOUDNATIVE-*` / GPU plans report `storage_size: 0`; the node boot disk is still provisioned from the configurable `storageGB` (default 20GB).

### Node storage

Each node gets a single root disk cloned from the OS template. The size and tier are configured per `UpCloudNodeClass`:

- `storageGB` — disk size in GB. Defaults to **20** when unset.
- `storageTier` — `standard` (default), `maxiops`, or `hdd`.

Karpenter does not size disks from pod storage requests; the disk is a fixed, configurable value and is advertised as the node's `ephemeral-storage` capacity. PersistentVolumeClaims are provisioned by the CSI driver and do not affect node selection.

### Spot instances

UpCloud exposes spot capacity as dedicated plan names (e.g. `GPU-SPOT-8xCPU-64GB-1xL4`). The provider
surfaces each plan as its own instance type: on-demand plans carry `karpenter.sh/capacity-type: on-demand`
and spot plans carry `karpenter.sh/capacity-type: spot`. A NodePool requesting
`karpenter.sh/capacity-type: spot` is matched by Karpenter's scheduler to spot plans only; when no
spot plan matches the requested shape, no instance type is found and the pod stays unscheduled (no
silent fallback to on-demand). Spot pricing is taken from the live catalog and used for cost-aware
scheduling.

> To run a NodePool on spot, set `spec.template.spec.requirements: - key: karpenter.sh/capacity-type, operator: In, values: ["spot"]`.

### NodeClaim TTL (alpha)

This provider ships an optional absolute-lifetime TTL controller for NodeClaims as an alternative to Karpenter's built-in consolidation, to make maximum and optimal use of UpCloud's hourtly billing cycle. The controller is an **alpha** release. The core logic and unit tests are solid, but e2e coverage against live clusters still needs more testing.

The controller is disabled by default. Enable it by setting `UPCLOUD_NODECLAIM_TTL_ENABLED=true` on the operator.
The TTL defaults to 50minutes and is configurable via `UPCLOUD_NODECLAIM_TTL` (any Go duration, e.g. `30m`, `1h`).

When the TTL controller is active, set your NodePool's `disruption.consolidationPolicy` to `Never` to prevent Karpenter's built-in
disruption from fighting with the TTL eviction.

> **Alpha** — opt-in only. Help test it by running the e2e suite with
> `UPCLOUD_E2E_PROVISION=1 go test -tags e2e ./test/e2e/ -run TestLiveNodeClaimTTL -v -timeout 20m`.
> Bug reports and fixes are very welcome.

### Node repair

Karpenter's built-in `node.health` controller calls the provider's `RepairPolicies()` and force-terminates (then replaces) any node that stays in an unhealthy state past its toleration window. This provider watches the standard Kubernetes `Ready` condition:

- `Ready = False` (NotReady — kubelet down or the node never joined) for longer than the toleration, or
- `Ready = Unknown` (kubelet stopped reporting) for longer than the toleration.

The toleration defaults to **30 minutes** and is configurable via `UPCLOUD_REPAIR_TOLERATION` (any Go duration string, e.g. `15m`, `1h`). Node *termination* (a node with a deletion timestamp that won't go away) is handled separately by Karpenter's built-in `node.termination` controller, so it is not part of `RepairPolicies()`.


### Scale-from-zero

`GetInstanceTypes()` calls `GetPlans()` which returns all server plans with
CPU, RAM, and GPU specs. Karpenter can bin-pack pods onto any plan without
any servers running. Pricing is fetched from `GetPricesByZone()` and cached
with a 30-minute TTL.

### Required environment variables

| Variable | Description |
|-------------|
| `UPCLOUD_TOKEN` | UpCloud API token |
| `UPCLOUD_KUBERNETES_CLUSTER_ID` | UKS cluster UUID — zone and API server endpoint auto-detected |
| `UPCLOUD_TEMPLATE_UUID` | OS template UUID for node boot disk (optional, default: Debian 13) |
| `UPCLOUD_REPAIR_TOLERATION` | How long a `NotReady`/`Unknown` node is tolerated before Karpenter recycles it (optional, default: `30m`) |
| `UPCLOUD_NODECLAIM_TTL_ENABLED` | Enable the alpha NodeClaim TTL controller (optional, default: unset — disabled) |
| `UPCLOUD_NODECLAIM_TTL` | TTL for idle NodeClaims (optional, default: `50m`) |

#### Required UpCloud API permissions

The credentials need the following permissions in the UpCloud API:

| Resource | Permission | Reason |
|----------|------------|
| Kubernetes clusters | `read` | Auto-detect zone, plan, and API server endpoint of the target cluster |
| Server | `read`, `create`, `delete` | Provision and terminate Karpenter-managed nodes |
| Storage | `read`, `create`, `delete` | Create/clean up cloud-init and OS disk storage |
| Network | `read` | Attach nodes to the correct network |
| Labels | `write` | Tag servers with `karpenter.upcloud.com/managed=true` |

Use a dedicated token or sub-account with the above permissions. `UPCLOUD_TOKEN` is used with bearer auth.

## Project structure

```
├── cmd/karpenter-upcloud/     ← entry point + Containerfile
│   ├── main.go
│   └── Containerfile
├── internal/version/          ← build-time version info
│   └── version.go
├── apis/v1alpha1/             ← UpCloudNodeClass CRD types
│   ├── groupversion_info.go
│   ├── upcloudnodeclass_types.go
│   ├── upcloudnodeclass_status.go
│   └── zz_generated.deepcopy.go
├── pkg/
│   ├── cloudprovider/         ← core provider implementation
│   │   ├── cloudprovider.go
│   │   └── helpers.go          ← bootstrap token + CA bundle
│   └── providers/
│       ├── options.go          ← env var parsing
│       ├── instance/           ← server lifecycle (Create/Delete/Get/List)
│       │   └── instance.go
│   ├── instancetypes/      ← plan discovery + cached pricing
│   │   └── instancetypes.go
│       └── userdata/           ← cloud-init generation
│           └── userdata.go
├── examples/                  ← sample CRDs
│   ├── upcloudnodeclass.yaml
│   └── nodepool.yaml
├── Makefile
├── LICENSE
└── README.md
```

## Developing

```shell
make test      # vet + test
make build     # compile binary
make container-build  # build OCI image via buildah
```

## Credits

- **UpCloud Ltd** — Sponsors the test infrastructure used for integration and e2e testing.
- **Zed Industries** — Provides a free version of their editor.

## License

EUPL 1.2 — see [LICENSE](LICENSE).

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for version history.
