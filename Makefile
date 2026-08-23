TAG ?= $(shell git describe --tags)
COMMIT = $(shell git log --format="%h" -n 1)
TREE_STATE = $(shell git diff --quiet && echo 'clean' || echo 'dirty')

GO_VERSION ?= 1.26.6
CONTAINER_REPO ?= ghcr.io/upcloud-tools/karpenter-provider-upcloud-test
IMAGE_TAG ?= $(shell git rev-parse HEAD)

HELM_CHART_DIR := deploy/helm

CGO_ENABLED ?= 0
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
LDFLAGS := -s -w \
	-X github.com/upcloud-tools/karpenter-provider-upcloud/internal/version.Version=$(TAG) \
	-X github.com/upcloud-tools/karpenter-provider-upcloud/internal/version.Commit=$(COMMIT) \
	-X github.com/upcloud-tools/karpenter-provider-upcloud/internal/version.TreeState=$(TREE_STATE)

# Build container image for linux/amd64 using buildah
# TAG: version string
# COMMIT: short commit hash
# TREE_STATE: "clean" or "dirty" based on git status
# CONTAINER_REPO: full image name including registry
# IMAGE_TAG: tag to apply to the built image
.PHONY: container-build
container-build:
	buildah build --platform linux/amd64 \
		--build-arg VERSION=$(TAG) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg TREE_STATE=$(TREE_STATE) \
		-t $(CONTAINER_REPO):$(IMAGE_TAG) \
		-f cmd/karpenter-upcloud/Containerfile .

# Push container image to registry, optionally capturing digest
# CONTAINER_REPO: full image name including registry
# IMAGE_TAG: tag of the image to push
# DIGEST_FILE: optional path where the image digest will be written
.PHONY: push-image
push-image:
	@echo "==> Pushing image $(CONTAINER_REPO):$(IMAGE_TAG)"
ifdef DIGEST_FILE
	buildah push --digestfile "$(DIGEST_FILE)" "$(CONTAINER_REPO):$(IMAGE_TAG)" "docker://$(CONTAINER_REPO):$(IMAGE_TAG)"
else
	buildah push "$(CONTAINER_REPO):$(IMAGE_TAG)"
endif

E2E_TIMEOUT ?= 20m
E2E_PLAN ?= CLOUDNATIVE-2xCPU-4GB
E2E_CAPACITY_TYPE ?= on-demand
E2E_TEST ?= TestLiveCloudProviderCreate

E2E_ENV := UPCLOUD_E2E_PROVISION=1 \
	UPCLOUD_E2E_PLAN=$(E2E_PLAN) \
	UPCLOUD_E2E_CAPACITY_TYPE=$(E2E_CAPACITY_TYPE)

# Run end-to-end tests against live UpCloud infrastructure
# E2E_TIMEOUT: maximum duration for test execution
# E2E_PLAN: UpCloud server plan to use for testing
# E2E_CAPACITY_TYPE: capacity type for the server, either "on-demand" or "spot"
# E2E_TEST: specific test function to run
.PHONY: test-e2e
test-e2e:
	$(E2E_ENV) go test -tags e2e ./test/e2e/ -run $(E2E_TEST) -v -timeout $(E2E_TIMEOUT)

DEPLOY_NAMESPACE ?= kube-system

# Deploy to test cluster via Helm
# Requires KUBECONFIG to be set and point to a valid cluster
# UPCLOUD_KUBERNETES_CLUSTER_ID: cluster UUID for the provider
# UPCLOUD_TOKEN: UpCloud API token for authentication
# CONTAINER_REPO: container image repository
# IMAGE_TAG: container image tag
# HELM_OPTS: additional helm options
.PHONY: deploy-test
deploy-test:
	@kubectl apply -f deploy/helm/crds/
	@helm upgrade --install karpenter-provider-upcloud $(HELM_CHART_DIR) --namespace $(DEPLOY_NAMESPACE) \
		--set config.clusterUUID=$(UPCLOUD_KUBERNETES_CLUSTER_ID) \
		--set config.auth.token=$(UPCLOUD_TOKEN) \
		--set image.repository=$(CONTAINER_REPO) \
		--set image.tag=$(IMAGE_TAG) \
		$(HELM_OPTS)
	@echo "Deployed karpenter-provider-upcloud to $(DEPLOY_NAMESPACE)"

# Remove deployment from test cluster
.PHONY: undeploy-test
undeploy-test:
	@helm uninstall karpenter-provider-upcloud --namespace $(DEPLOY_NAMESPACE) || true
	@echo "Removed karpenter-provider-upcloud from $(DEPLOY_NAMESPACE)"

# Run unit tests with race detection
.PHONY: test
test:
	go vet ./...
	go test -race ./...

# Build the karpenter-upcloud binary
# CGO_ENABLED: enable/disable CGO compilation
# GOOS: target operating system
# GOARCH: target architecture
# TAG: version string embedded in binary via ldflags
# COMMIT: commit hash embedded in binary via ldflags
# TREE_STATE: tree state embedded in binary via ldflags
.PHONY: build
build:
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
	go build -ldflags '$(LDFLAGS)' -o bin/karpenter-upcloud ./cmd/karpenter-upcloud

# Run go vet static analysis
.PHONY: vet
vet:
	go vet ./...

# Tidy and verify go modules
.PHONY: tidy
tidy:
	go mod tidy
	go mod verify

# Stop and delete all karpenter-related servers in UpCloud
.PHONY: cleanup
cleanup:
	upctl server list | awk '$$2 ~ /^karpenter/ {print $$1}' | xargs -r upctl server stop --type hard || true
	upctl server list | awk '$$5 == "stopped" {print $$1}' | xargs -r upctl server delete --delete-storages || true

HELM_CHART_VERSION ?= $(shell yq .version $(HELM_CHART_DIR)/Chart.yaml)

# Lint the Helm chart
.PHONY: helm-lint
helm-lint:
	helm lint $(HELM_CHART_DIR)

# Run Helm unit tests (installs unittest plugin if needed)
.PHONY: helm-unittest
helm-unittest:
	@if ! helm plugin list | grep -q unittest > /dev/null 2>&1; then \
		helm plugin install https://github.com/helm-unittest/helm-unittest.git; \
	fi
	helm unittest $(HELM_CHART_DIR)

# Run all Helm tests (lint + unit tests)
.PHONY: helm-test
helm-test: helm-lint helm-unittest

# Package the Helm chart into dist/
.PHONY: helm-package
helm-package:
	mkdir -p dist
	helm package $(HELM_CHART_DIR) --destination dist

# Extract release notes for the current chart version from CHANGELOG.md
# HELM_CHART_VERSION: auto-detected from Chart.yaml version field
.PHONY: helm-release-notes
helm-release-notes:
	@awk \
		'/^## \['$(HELM_CHART_VERSION)'\]/ { flag = 1; next } \
		/^## \[/ { if ( flag ) { exit; } } \
		flag { if ( n ) { print prev; } n++; prev = $$0 }' \
		$(HELM_CHART_DIR)/CHANGELOG.md

# Extract release notes for a given version from CHANGELOG.md
# TAG: version to extract notes for (default: git describe --tags)
.PHONY: release-notes
release-notes:
	@awk -v ver="$(TAG)" ' \
		/^## \[/ { if (found) exit } \
		$$0 ~ "\\[" ver "\\]" { found=1; next } \
		found { if (n) print prev; n++; prev=$$0 }' \
		CHANGELOG.md

# Lint Kubernetes manifests with kube-linter
.PHONY: kube-lint
kube-lint:
	kube-linter lint --config $(HELM_CHART_DIR)/.kube-linter.yaml $(HELM_CHART_DIR)

# Validate Kubernetes manifests with kubeconform
.PHONY: k8s-lint
k8s-lint:
	helm template test-release $(HELM_CHART_DIR) > /tmp/karpenter-upcloud-rendered.yaml
	@if command -v kubeconform > /dev/null 2>&1; then \
		kubeconform --ignore-missing-schemas /tmp/karpenter-upcloud-rendered.yaml; \
	else \
		echo "kubeconform not installed. Install from https://github.com/yannh/kubeconform"; \
		exit 1; \
	fi
