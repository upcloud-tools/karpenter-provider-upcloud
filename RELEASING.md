# Release Process

This document describes how to create a new release of the Karpenter Provider for UpCloud.

## Automated Releases

Releases are automated via GitHub Actions. Two workflows trigger when `charts/karpenter-upcloud/Chart.yaml` is pushed to `main`:

### Application Release (`release-app.yaml`)

Triggered when `appVersion` in Chart.yaml is bumped to a version that doesn't have an existing GitHub release.

1. Builds container image with `buildah`
2. Runs E2E provisioning tests
3. Tags image with appVersion + `latest`
4. Signs image with cosign
5. Creates draft GitHub release
6. Creates git tag

### Helm Chart Release (`release-helm.yaml`)

Triggered when `version` in Chart.yaml is bumped to a version that doesn't have an existing `helm-v*` release.

1. Runs E2E tests (skipped if appVersion was also bumped — release-app handles it)
2. Copies LICENSE + README into chart directory
3. Packages chart with `helm package`
4. Signs chart with cosign
5. Pushes chart to `oci://ghcr.io/upcloud-tools/charts`
6. Creates draft GitHub release with chart tarball + signature

## Creating a Release

### 1. Update CHANGELOG.md

Add a new section for the release:

```markdown
## [1.0.0] - 2026-08-19

### Added
- New feature description

### Changed
- Changed behavior description
```

### 2. Update Chart.yaml

Bump `version` and `appVersion` as needed:

```yaml
version: "1.0.0"
appVersion: "1.0.0"
```

Also update the `artifacthub.io/changes` annotation with the latest changelog entry.

### 3. Push to main

```bash
git add CHANGELOG.md charts/karpenter-upcloud/Chart.yaml
git commit -m "release: v1.0.0"
git push origin main
```

The workflows will detect the version bumps and create the releases automatically.

### 4. Publish the draft releases

Go to GitHub Releases, review the draft releases, and publish them when ready.

## Manual Release (fallback)

If the automated pipeline fails:

```bash
# Build and push container image
make container-build CONTAINER_REPO=ghcr.io/upcloud-tools/karpenter-upcloud IMAGE_TAG=v1.0.0
make push-image CONTAINER_REPO=ghcr.io/upcloud-tools/karpenter-upcloud IMAGE_TAG=v1.0.0

# Package and push Helm chart
cp LICENSE README.md charts/karpenter-upcloud/
helm package charts/karpenter-upcloud --destination dist
helm push dist/*.tgz oci://ghcr.io/upcloud-tools/charts
```
