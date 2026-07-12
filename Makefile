TAG ?= $(shell git describe --tags)
COMMIT = $(shell git log --format="%h" -n 1)
TREE_STATE = $(shell git diff --quiet && echo 'clean' || echo 'dirty')

CONTAINER_REPO ?= ghcr.io/upcloud-tools/karpenter-upcloud-test
IMAGE_TAG ?= $(shell git rev-parse HEAD)

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
	go build -o bin/karpenter-upcloud ./cmd/karpenter-upcloud

.PHONY: cleanup
cleanup:
	upctl server list | awk '$$2 ~ /^karpenter/ {print $$1}' | xargs -r upctl server stop --type hard
	upctl server list | awk '$$5 == "stopped" {print $$1}' | xargs -r upctl server delete --delete-storages
