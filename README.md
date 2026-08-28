# Karpenter Provider for UpCloud

[![Beta](https://img.shields.io/badge/Status-Beta-yellow)](https://github.com/upcloud-tools/karpenter-provider-upcloud)
[![Build](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/test.yaml/badge.svg)](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/test.yaml)
[![Build](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/test-e2e.yaml/badge.svg)](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/test-e2e.yaml)
[![Go Lint](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/lint-golang.yaml/badge.svg)](https://github.com/upcloud-tools/karpenter-provider-upcloud/actions/workflows/lint-golang.yaml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/upcloud-tools/karpenter-provider-upcloud/badge)](https://scorecard.dev/viewer/?uri=github.com/upcloud-tools/karpenter-provider-upcloud)

Karpenter provider implementation for [UpCloud](https://upcloud.com), enabling efficient, just-in-time node autoscaling.

> [!IMPORTANT]
> **Beta** — in working state but not yet considered production-ready. Core provisioning, scale-from-zero,
> drift detection, node repair, GPU (spot) scheduling all work. Expect breaking changes in early stages.

**Note**: the n+1 VM issue ([kubernetes-sigs/karpenter#3121](https://github.com/kubernetes-sigs/karpenter/issues/3121)) is fixed in this provider via a [fork of Karpenter](https://github.com/aardbol/karpenter/tree/fix/3121) with a [fix PR (#3243)](https://github.com/kubernetes-sigs/karpenter/pull/3243) pending upstream merge. Until the PR is merged, this provider uses the forked dependency via a `replace` directive in `go.mod`.

## Features

This provider contains these features:

- Scale-from-zero provisioning
- Full UpCloud plan catalog as instance types (CLOUDNATIVE, STARTER, PREMIUM, GPU)
- GPU spot instance support
- Drift detection (recycles nodes when the `UpCloudNodeClass` changes)
- Node repair (replaces unhealthy or unjoined nodes)
- Configurable node storage (CLOUDNATIVE, GPU)
- Support for storage-bundled plans (STARTER, PREMIUM)
- Absolute NodeClaim lifetime TTL (**alpha version - expect unstability**)

For full feature details, see [FEATURES.md](FEATURES.md).

## Required environment variables

| Variable | Description |
|----------|-------------|
| `UPCLOUD_TOKEN` | UpCloud API token |
| `UPCLOUD_KUBERNETES_CLUSTER_ID` | UKS cluster UUID |
| `UPCLOUD_TEMPLATE_UUID` | OS template UUID for node boot disk |
| `UPCLOUD_REPAIR_TOLERATION` | How long a `NotReady`/`Unknown` node is tolerated before Karpenter recycles it |
| `UPCLOUD_NODECLAIM_TTL` | Enable NodeClaim TTL controller by setting a duration. Defines the absolute lifetime for NodeClaims (e.g. `50m`, `1h`). Disabled by default. |

> [!NOTE]
> `UPCLOUD_TEMPLATE_UUID` and `UPCLOUD_REPAIR_TOLERATION` are considered required unless deploying via the [Helm chart](deploy/helm/), which provides their defaults.

## Required UpCloud API permissions

The credentials need the following permissions in the UpCloud API:

| Resource | Reason |
|----------|--------|
| Kubernetes cluster | Auto-detect zone, plan, and API server endpoint of the target cluster |
| Server | Provision and terminate Karpenter-managed nodes |
| Storage | Create/clean up cloud-init and OS disk storage |
| Private network | Attach nodes to the correct K8s network |

Use a dedicated token or sub-account with the above permissions. `UPCLOUD_TOKEN` is used with bearer auth.

## Project structure

```
├── cmd/karpenter-upcloud/     ← entry point + Containerfile
├── internal/version/          ← build-time version info
├── apis/v1alpha2/             ← UpCloudNodeClass CRD types
├── pkg/
│   ├── cloudprovider/         ← core provider implementation
│   │   └── helpers.go          ← bootstrap token + CA bundle
│   ├── controllers/           ← Kubernetes controllers
│   │   ├── nodeclaimttl/      ← alpha TTL controller
│   │   └── nodeclass/         ← nodeclass reconciliation
│   ├── providers/
│   │   ├── options.go          ← env var parsing
│   │   ├── instance/           ← server lifecycle (Create/Delete/Get/List)
│   │   ├── instancetypes/      ← plan discovery + cached pricing
│   │   └── userdata/           ← cloud-init generation
│   └── util/                   ← shared utility helpers
├── deploy/helm/               ← Helm chart
├── examples/                  ← sample CRDs
├── Makefile
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
