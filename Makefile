TAG ?= $(shell git describe --tags)
COMMIT = $(shell git log --format="%h" -n 1)
TREE_STATE = $(shell git diff --quiet && echo 'clean' || echo 'dirty')

GO_VERSION ?= 1.26.6
CONTAINER_REPO ?= ghcr.io/upcloud-tools/karpenter-upcloud-test
IMAGE_TAG ?= $(shell git rev-parse HEAD)

HELM_CHART_DIR := deploy/helm

CGO_ENABLED ?= 0
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
LDFLAGS := -s -w \
	-X github.com/upcloud-tools/karpenter-provider-upcloud/internal/version.Version=$(TAG) \
	-X github.com/upcloud-tools/karpenter-provider-upcloud/internal/version.Commit=$(COMMIT) \
	-X github.com/upcloud-tools/karpenter-provider-upcloud/internal/version.TreeState=$(TREE_STATE)

.PHONY: container-build
container-build:
	buildah build --platform linux/amd64 \
		--build-arg VERSION=$(TAG) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg TREE_STATE=$(TREE_STATE) \
		-t $(CONTAINER_REPO):$(IMAGE_TAG) \
		-f cmd/karpenter-upcloud/Containerfile .

.PHONY: push-image
push-image: container-build
	@echo "==> Pushing image $(CONTAINER_REPO):$(IMAGE_TAG)"
	buildah push $(CONTAINER_REPO):$(IMAGE_TAG)

.PHONY: test
test:
	go vet ./...
	go test -race ./...

.PHONY: build
build:
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
	go build -ldflags '$(LDFLAGS)' -o bin/karpenter-upcloud ./cmd/karpenter-upcloud

.PHONY: vet
vet:
	go vet ./...

.PHONY: tidy
tidy:
	go mod tidy
	go mod verify
	
.PHONY: cleanup
cleanup:
	upctl server list | awk '$$2 ~ /^karpenter/ {print $$1}' | xargs -r upctl server stop --type hard || true
	upctl server list | awk '$$5 == "stopped" {print $$1}' | xargs -r upctl server delete --delete-storages || true

HELM_CHART_VERSION ?= $(shell yq .version $(HELM_CHART_DIR)/Chart.yaml)

.PHONY: helm-lint
helm-lint:
	helm lint $(HELM_CHART_DIR)

.PHONY: helm-unittest
helm-unittest:
	@if ! helm plugin list | grep -q unittest > /dev/null 2>&1; then \
		helm plugin install https://github.com/helm-unittest/helm-unittest.git; \
	fi
	helm unittest $(HELM_CHART_DIR)

.PHONY: helm-test
helm-test: helm-lint helm-unittest

.PHONY: helm-package
helm-package:
	mkdir -p dist
	helm package $(HELM_CHART_DIR) --destination dist

.PHONY: helm-release-notes
helm-release-notes:
	@awk \
		'/^## \['$(HELM_CHART_VERSION)'\]/ { flag = 1; next } \
		/^## \[/ { if ( flag ) { exit; } } \
		flag { if ( n ) { print prev; } n++; prev = $$0 }' \
		$(HELM_CHART_DIR)/CHANGELOG.md

.PHONY: kube-lint
kube-lint:
	kube-linter lint --config $(HELM_CHART_DIR)/.kube-linter.yaml $(HELM_CHART_DIR)

.PHONY: k8s-lint
k8s-lint:
	helm template test-release $(HELM_CHART_DIR) > /tmp/karpenter-upcloud-rendered.yaml
	@if command -v kubeconform > /dev/null 2>&1; then \
		kubeconform --ignore-missing-schemas /tmp/karpenter-upcloud-rendered.yaml; \
	else \
		echo "kubeconform not installed. Install from https://github.com/yannh/kubeconform"; \
		exit 1; \
	fi
