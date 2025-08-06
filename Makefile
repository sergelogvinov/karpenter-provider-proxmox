# Copyright 2025 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

REGISTRY ?= ghcr.io
USERNAME ?= sergelogvinov
PROJECT ?= karpenter-provider-proxmox
IMAGE ?= $(REGISTRY)/$(USERNAME)/$(PROJECT)
HELMREPO ?= $(REGISTRY)/$(USERNAME)/charts
PLATFORM ?= linux/arm64,linux/amd64
PUSH ?= false

VERSION ?= $(shell git describe --dirty --tag --match='v*' 2> /dev/null)
SHA ?= $(shell git describe --match=none --always --abbrev=7 --dirty)
TAG ?= $(VERSION)

GO_LDFLAGS := -s -w
GO_LDFLAGS += -X sigs.k8s.io/karpenter/pkg/operator.Version=$(VERSION)

OS ?= $(shell go env GOOS)
ARCH ?= $(shell go env GOARCH)
ARCHS = amd64 arm64

TESTARGS ?= "-v"

BUILD_ARGS := --platform=$(PLATFORM)
ifeq ($(PUSH),true)
BUILD_ARGS += --push=$(PUSH)
BUILD_ARGS += --output type=image,annotation-index.org.opencontainers.image.source="https://github.com/$(USERNAME)/$(PROJECT)",annotation-index.org.opencontainers.image.description="Karpenter Proxmox Provider"
else
BUILD_ARGS += --output type=docker
endif

CONTROLLER_GEN ?= controller-gen

COSING_ARGS ?=

############

# Help Menu

define HELP_MENU_HEADER
# Getting Started

To build this project, you must have the following installed:

- git
- make
- golang 1.20+
- golangci-lint

endef

export HELP_MENU_HEADER

help: ## This help menu.
	@echo "$$HELP_MENU_HEADER"
	@grep -E '^[a-zA-Z0-9%_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

############
#
# Build Abstractions
#

build-all-archs:
	@for arch in $(ARCHS); do $(MAKE) ARCH=$${arch} build ; done

.PHONY: clean
clean: ## Clean
	rm -rf bin

.PHONY: tools
tools:
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

.PHONY: vendor
vendor: ## update modules and populate local vendor directory
	go mod tidy
	go mod vendor
	go mod verify

.PHONY: generate
generate: gen-objects manifests ## generate all controller-gen files

.PHONY: gen-objects
gen-objects: ## generate the controller-gen related objects
	$(CONTROLLER_GEN) object paths="./..."

.PHONY: manifests
manifests: ## generate the controller-gen kubernetes manifests
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd object:headerFile="hack/boilerplate.go.txt" paths="./..." output:crd:artifacts:config=pkg/apis/crds
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd paths="./vendor/sigs.k8s.io/karpenter/..." output:crd:artifacts:config=pkg/apis/crds
	@echo "Copying generated CRDs to Helm chart..."
	@mkdir -p charts/karpenter-provider-proxmox/crds
	@cp pkg/apis/crds/*.yaml charts/karpenter-provider-proxmox/crds/

.PHONY: install
install: ## Install
	kubectl replace -f charts/karpenter-provider-proxmox/crds/

.PHONY: build
build: ## Build
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) go build -ldflags "$(GO_LDFLAGS)" \
		-o bin/karpenter-provider-proxmox-$(ARCH) ./cmd/controller

.PHONY: run
run: ## Run
	go run ./cmd/controller -disable-leader-election -log-level=debug

.PHONY: lint
lint: ## Lint Code
	golangci-lint run --config .golangci.yml

.PHONY: unit
unit: ## Unit Tests
	go test -tags=unit $(shell go list ./...) $(TESTARGS)

.PHONY: conformance
conformance:
	docker run --rm -it -v $(PWD):/src -w /src ghcr.io/siderolabs/conform:latest enforce

############

.PHONY: helm-unit
helm-unit: ## Helm Unit Tests
	@helm lint charts/karpenter-provider-proxmox
	@helm template --include-crds -f charts/karpenter-provider-proxmox/ci/values.yaml \
		karpenter-provider-proxmox charts/karpenter-provider-proxmox >/dev/null

.PHONY: helm-login
helm-login: ## Helm Login
	@echo "${HELM_TOKEN}" | helm registry login $(REGISTRY) --username $(USERNAME) --password-stdin

.PHONY: helm-release
helm-release: ## Helm Release
	@rm -rf dist/
	@helm package charts/karpenter-provider-proxmox -d dist
	@helm push dist/karpenter-provider-proxmox-*.tgz oci://$(HELMREPO) 2>&1 | tee dist/.digest
	@cosign sign --yes $(COSING_ARGS) $(HELMREPO)/karpenter-provider-proxmox@$$(cat dist/.digest | awk -F "[, ]+" '/Digest/{print $$NF}')

############

.PHONY: docs
docs:
	yq -i '.appVersion = "$(TAG)"' charts/karpenter-provider-proxmox/Chart.yaml
	helm template -n kube-system --include-crds karpenter-provider-proxmox \
		-f charts/karpenter-provider-proxmox/values.edge.yaml \
		--set-string image.tag=$(TAG) \
		charts/karpenter-provider-proxmox > docs/deploy/karpenter-provider-proxmox.yml
	helm template -n kube-system --include-crds karpenter-provider-proxmox \
		-f charts/karpenter-provider-proxmox/values.edge.yaml \
		--set-string image.tag=edge \
		charts/karpenter-provider-proxmox > docs/deploy/karpenter-provider-proxmox-edge.yml
	helm-docs --sort-values-order=file charts/karpenter-provider-proxmox

release-update:
	git-chglog --config hack/chglog-config.yml -o CHANGELOG.md

############
#
# Docker Abstractions
#

docker-init:
	@docker run --rm --privileged multiarch/qemu-user-static -p yes ||:

	@docker context create multiarch ||:
	@docker buildx create --name multiarch --driver docker-container --use ||:
	@docker context use multiarch
	@docker buildx inspect --bootstrap multiarch

.PHONY: images
images: ## Build images
	docker buildx build $(BUILD_ARGS) \
		--build-arg VERSION="$(VERSION)" \
		--build-arg TAG="$(TAG)" \
		--build-arg SHA="$(SHA)" \
		-t $(IMAGE):$(TAG) \
		--target karpenter-provider-proxmox \
		-f Dockerfile .

.PHONY: images-checks
images-checks: images
	trivy image --exit-code 1 --ignore-unfixed --severity HIGH,CRITICAL --no-progress $(IMAGE):$(TAG)

.PHONY: images-cosign
images-cosign:
	@cosign sign --yes $(COSING_ARGS) --recursive $(IMAGE):$(TAG)
