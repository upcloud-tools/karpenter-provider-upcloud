# Contributing to Karpenter Provider for UpCloud

This document explains the load-bearing conceptual choices and UpCloud-specific quirks that every contributor should understand before modifying the code. These are not arbitrary decisions, they match how UpCloud's APIs and the UpCloud Cloud Controller Manager (CCM) work.

## UpCloud ↔ Karpenter Concept Map

### Provider ID: Four Slashes Are Correct

```
upcloud:////<server-uuid>
```

The provider ID has **four slashes**, not three. This is not a typo. The UpCloud CCM writes provider IDs in this format, and Karpenter must match exactly what the CCM writes when it observes Nodes joining the cluster. If you "fix" the format to `upcloud:///<uuid>`, Karpenter will fail to match NodeClaims to Nodes, and your nodes will appear orphaned.

**Where this matters:** `pkg/cloudprovider/cloudprovider.go` — `providerPrefix` constant.

### Zone and Region Are the Same Value

```go
"topology.kubernetes.io/region": p.zone,
"topology.kubernetes.io/zone":   p.zone,
```

UpCloud doesn't distinguish between regions and zones the way AWS or GCP do. A "zone" like `de-fra1` is both the region and the zone. The UpCloud CCM writes the same value to both `topology.kubernetes.io/region` and `topology.kubernetes.io/zone` labels on Nodes. This provider must do the same, or Karpenter will fail to match NodeClaims to Nodes.

**Where this matters:** `pkg/cloudprovider/cloudprovider.go` — label assignment in `Create()`.

### Server Deletion Requires Two Calls

UpCloud's API requires servers to be **stopped** before they can be deleted. You cannot delete a running server. The deletion flow is:

1. `StopServer()` with hard stop
2. `WaitForServerState()` until state is `stopped`
3. `DeleteServerAndStorages()` to remove the server and its disks

If you try to delete without stopping first, you'll get a `SERVER_STATE_ILLEGAL` error. The `Delete()` method handles this automatically, but if you're modifying the deletion logic, remember: **stop first, wait, then delete**.

**Where this matters:** `pkg/cloudprovider/cloudprovider.go` — `Delete()` method, `pkg/providers/instance/instance.go` — `Stop()`, `WaitForStop()`.

### UpCloud Server Labels Are Separated from Node Labels

The provider sends two distinct label sets:

- **Node labels** — Kubernetes-internal labels (`topology.kubernetes.io/*`, `node.kubernetes.io/instance-type`, `karpenter.sh/*`, etc.) merged with user labels. These go into cloud-init (`kubelet --node-labels`) and onto the NodeClaim. They are **not** sent to the UpCloud server.
- **Server labels** — only user-defined labels from `nodeClass.Spec.Labels`, plus managed markers (`managed_by=karpenter`, `capu_cluster_id`, `capu_cluster_name`, `capu_generated_name`). These go to the UpCloud server API.

The UpCloud API accepts any characters in label keys, including slashes and dots. No character-based filtering is applied.

**Where this matters:** `pkg/cloudprovider/cloudprovider.go` — `Create()`, `nodeLabels` vs `serverLabels`. `pkg/providers/instance/instance.go` — `Create()`.

### NodeClass Maps to UKS Node Group Configuration

An `UpCloudNodeClass` is roughly equivalent to a UKS node group's configuration. It defines:
- **Zone** — which data center (e.g., `de-fra1`, `fi-hel2`)
- **Plan** — the server plan (e.g., `CLOUDNATIVE-2xCPU-4GB`, `GPU-4xCPU-32GB-1xL4`)
- **Storage** — root disk config: `size` (GB, default 20), `tier` (`standard`, `maxiops`, or `hdd`), `encrypted` (bool)
- **Labels/Taints** — applied to each node in the group
- **AntiAffinity** — whether nodes should avoid being placed on the same physical host

A NodePool references an `UpCloudNodeClass` to say "provision nodes with this configuration." The NodePool's `requirements` field selects which instance types (plans) are acceptable; the NodeClass provides the rest of the config.

**Where this matters:** `apis/v1alpha2/upcloudnodeclass_types.go`, `pkg/cloudprovider/cloudprovider.go` — `resolveNodeClass()`.

### Instance Types Are UpCloud Plans

Karpenter's `GetInstanceTypes()` returns cached UpCloud plans, each surfaced as a separate instance type. The provider calls `GetPlans()` to fetch all available plans with CPU, RAM, and GPU specs, then `GetPricesByZone()` for pricing. All plan families are included (CLOUDNATIVE, GPU, STARTER, PREMIUM). Use NodePool requirements with instance type labels to select specific families or characteristics.

Each plan is its own instance type. Spot plans (names containing `SPOT`) get a `karpenter.sh/capacity-type: spot` offering; all others get `on-demand`. Karpenter selects between them via the capacity-type requirement.

**Where this matters:** `pkg/providers/instancetypes/instancetypes.go` — `Refresh()`, `buildInstanceTypeWithPrices()`.

### The n+1 VM Problem (and the Fork Fix)

Karpenter has a known issue ([kubernetes-sigs/karpenter#3121](https://github.com/kubernetes-sigs/karpenter/issues/3121)) where it provisions n+1 VMs when scaling from zero because it doesn't wait for the first VM to register as a Node before provisioning the next one. This is fixed in a [fork of Karpenter](https://github.com/aardbol/karpenter/tree/fix/3121) with a [PR pending upstream merge](https://github.com/kubernetes-sigs/karpenter/pull/3243).

This provider uses the forked dependency via a `replace` directive in `go.mod`. Until the upstream PR is merged, any changes to Karpenter core dependencies should be tested against the fork to ensure the fix isn't broken.

**Where this matters:** `go.mod` — `replace` directive for `sigs.k8s.io/karpenter`.

## Common Pitfalls

### Labels Are Split Between Kubernetes and UpCloud

Kubernetes-internal labels (topology, instance-type, capacity-type, created-at) are set on the NodeClaim and passed to cloud-init for kubelet `--node-labels`, but are **not** sent to the UpCloud server. Only user-defined labels from `nodeClass.Spec.Labels` reach the UpCloud server. The UpCloud API accepts any characters in label keys. If you need to debug which labels are on the server, check via `upctl server show <uuid>`.

### Don't Assume the Server Has a Creation Timestamp

UpCloud's `GetServerDetails()` API doesn't return a creation timestamp. If you need to know when a server was created (e.g., for garbage collection), you must stamp it yourself. This provider adds `karpenter.sh/created-at` to the NodeClaim's labels at creation time.

### Don't Forget to Wait After Stopping

After calling `StopServer()`, you must call `WaitForServerState()` to wait for the server to reach the `stopped` state before calling `DeleteServerAndStorages()`. If you skip the wait, deletion will fail with `SERVER_STATE_ILLEGAL`.

### Test Against Live Infrastructure

Unit tests are essential, but UpCloud's API has quirks that only show up in e2e tests. Before submitting a PR that modifies server lifecycle logic (create, stop, delete), run the e2e suite:

```bash
make test-e2e
```

This provisions real infrastructure, so it costs money. Use it sparingly but don't skip it for critical changes.

## Getting Started

1. **Read the README** — understand the architecture and core flow.
2. **Run the unit tests** — `make test` should pass before you start.
3. **Make your changes** — follow the existing code style.
4. **Run the tests again** — ensure nothing broke.
5. **Run e2e tests if relevant** — see above.
6. **Update the README** — if your change affects user-facing behavior.
7. **Submit a PR** — include a clear description of what you changed and why.

## Questions?

If you're unsure whether a design decision matches UpCloud's behavior, check the [UpCloud API documentation](https://developers.upcloud.com/) or ask in the PR.
