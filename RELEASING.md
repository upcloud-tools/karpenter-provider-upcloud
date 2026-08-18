# Release Process

This document describes how to create a new release of the Karpenter Provider for UpCloud.

## Current Process (Manual)

### 1. Update CHANGELOG.md

Add a new section for the release with all changes since the last version:

```markdown
## [1.0.0] - 2026-08-19

### Added
- New feature description

### Changed
- Changed behavior description

### Fixed
- Bug fix description
```

### 2. Create and push git tag

```bash
git tag v1.0.0 && git push origin v1.0.0
```

### 3. Build and push container image

```bash
# Build the image
make container-build \
  CONTAINER_REPO=ghcr.io/upcloud-tools/karpenter-upcloud \
  IMAGE_TAG=v1.0.0

# Push the image
make push-image \
  CONTAINER_REPO=ghcr.io/upcloud-tools/karpenter-upcloud \
  IMAGE_TAG=v1.0.0
```

### 4. Update Helm chart version

Update `charts/karpenter-upcloud/Chart.yaml`:

```yaml
version: "1.0.0"
appVersion: "1.0.0"
```

### 5. Create GitHub Release (optional)

Create a GitHub release with the tag and copy the CHANGELOG entry as release notes.
