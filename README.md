# Karpenter Provider for UpCloud — Beta

[![Build](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/test.yaml/badge.svg)](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/test.yaml)
[![Go Lint](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/lint-golang.yaml/badge.svg)](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/lint-golang.yaml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/upcloud-tools/karpenter-provider-upcloud/badge)](https://scorecard.dev/viewer/?uri=github.com/upcloud-tools/karpenter-provider-upcloud)

Karpenter provider implementation for [UpCloud](https://upcloud.com), enabling efficient, just-in-time node autoscaling.

> **Beta** — in working state but not yet production-ready. Core provisioning and scale-from-zero work,
> but drift detection, node repair, and comprehensive e2e tests are still in progress.

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
                                     │                           │
                                     │  ┌─────────────────────┐  │
                                     │  │      Karpenter      │  │
                                     │  │   (this operator)   │  │
                                     │  │                     │  │
                                     │  │  ┌───────────────┐  │  │
                                     │  │  │ Bootstrap     │  │  │
                                     │  │  │ Token (Secret)│  │  │
                                     │  │  └───────┬───────┘  │  │
                                     │  └──────────┼──────────┘  │
                                     └─────────────┼────────────┘
                                                   │
                              CreateServer/Delete  │  cloud-init
                              Server/ListServers   │  with kubeadm join
                                                   │
                                     ┌─────────────▼──────────────┐
                                     │      UpCloud API           │
                                     │                            │
                                     │  ┌──────────────────────┐  │
                                     │  │  Server A            │  │
                                     │  │  (kp-<uuid>)         │  │
                                     │  │  plan: 2xCPU-4GB     │  │
                                     │  │  zone: de-fra1       │  │
                                     │  ├──────────────────────┤  │
                                     │  │  Server B            │  │
                                     │  │  (kp-<uuid>)         │  │
                                     │  │  plan: 4xCPU-8GB     │  │
                                     │  │  zone: fi-hel2       │  │
                                     │  └──────────────────────┘  │
                                     └────────────────────────────┘
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

#### Required UpCloud API permissions

The credentials need the following permissions in the UpCloud API:

| Resource | Permission | Reason |
|----------|------------|
| Kubernetes clusters | `read` | Auto-detect zone, plan, and API server endpoint of the target cluster |
| Kubernetes kubeconfig | `read` | Resolve cluster API server endpoint for `kubeadm join` |
| Server | `read`, `create`, `delete` | Provision and terminate Karpenter-managed nodes |
| Storage | `read`, `create`, `delete` | Create/clean up cloud-init and OS disk storage |
| Network | `read` | Attach nodes to the correct network |
| Pricing | `read` | Get instance type pricing for cost-aware scheduling |
| Labels | `write` | Tag servers with `karpenter.upcloud.com/managed=true` |

On UKS, the built-in `upcloud` secret in `kube-system` (used by the CSI driver) needs to include a `token` key with your API token. Use a dedicated token or sub-account with the above permissions.

#### Authentication

`UPCLOUD_TOKEN` is used with bearer auth.

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

## License

EUPL 1.2 — see [LICENSE](LICENSE).

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for version history.
