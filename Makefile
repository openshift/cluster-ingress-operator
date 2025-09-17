all: generate build

PACKAGE=github.com/openshift/cluster-ingress-operator
MAIN_PACKAGE=$(PACKAGE)/cmd/ingress-operator

BIN=$(lastword $(subst /, ,$(MAIN_PACKAGE)))

ifneq ($(DELVE),)
GO_GCFLAGS ?= -gcflags=all="-N -l"
endif

GO=GO111MODULE=on GOFLAGS=-mod=vendor go
GO_BUILD_RECIPE=CGO_ENABLED=1 $(GO) build -o $(BIN) $(GO_GCFLAGS) $(MAIN_PACKAGE)

ifdef TEST
TEST_UNIT ?= $(TEST)
TEST_E2E ?= $(TEST)
else
TEST_UNIT ?= ^.*$
TEST_E2E ?= ^TestAll$
endif

CATALOG_VERSION ?= latest
CATALOG_IMG ?= quay.io/alebedev/ossm-operator-catalog:$(CATALOG_VERSION)
CATALOG_DIR := catalog
PACKAGE_DIR := $(CATALOG_DIR)/servicemeshoperator3
OPM = ./opm
OPM_VERSION = v1.58.0
CONTAINER_ENGINE ?= podman

.PHONY: build
build: generate
	$(GO_BUILD_RECIPE)

.PHONY: buildconfig
buildconfig:
	hack/create-buildconfig.sh

.PHONY: cluster-build
cluster-build:
	hack/start-build.sh

# TODO: Add deepcopy generation script/target
.PHONY: generate
generate: manifests

.PHONY: update
update: crd

# Generate CRDs from vendored and internal API specs.
.PHONY: crd
crd:
	hack/update-generated-crd.sh
	hack/update-profile-manifests.sh

.PHONY: manifests
manifests:
	go generate ./pkg/manifests

.PHONY: test
test: generate
	CGO_ENABLED=1 $(GO) test -run "$(TEST_UNIT)" ./...

.PHONY: release-local
release-local:
	MANIFESTS=$(shell mktemp -d) hack/release-local.sh

.PHONY: test-e2e
test-e2e: generate
	CGO_ENABLED=1 $(GO) test -timeout 1.5h -count 1 -v -tags e2e -run "$(TEST_E2E)" ./test/e2e

.PHONY: test-e2e-list
test-e2e-list: generate
	@(cd ./test/e2e; E2E_TEST_MAIN_SKIP_SETUP=1 $(GO) test -list . -tags e2e | grep ^Test | sort)

.PHONY: gatewayapi-conformance
gatewayapi-conformance:
	hack/gatewayapi-conformance.sh

.PHONY: clean
clean:
	$(GO) clean
	rm -f $(BIN)
	rm -rf pkg/manifests/manifests

.PHONY: verify
verify: generate
	hack/verify-gofmt.sh
	hack/verify-generated-crd.sh
	hack/verify-profile-manifests.sh
	hack/verify-deps.sh
	hack/verify-e2e-test-all-presence.sh

.PHONY: uninstall
uninstall:
	hack/uninstall.sh

.PHONY: run-local
run-local: build
	hack/run-local.sh

.PHONY: opm
opm: ## Download opm locally if necessary.
ifeq (,$(wildcard $(OPM)))
ifeq (,$(shell which opm 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p $(dir $(OPM)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPM) https://github.com/operator-framework/operator-registry/releases/download/$(OPM_VERSION)/$${OS}-$${ARCH}-opm ;\
	chmod +x $(OPM) ;\
	}
else
OPM = $(shell which opm)
endif
endif

.PHONY: catalog
catalog: opm
	$(OPM) render $(BUNDLE_IMG) -o yaml > $(PACKAGE_DIR)/bundle.yaml
	$(OPM) validate $(CATALOG_DIR)

.PHONY: catalog-build
catalog-build: catalog
	$(CONTAINER_ENGINE) build -t $(CATALOG_IMG) -f Dockerfile.catalog .

.PHONY: catalog-push
catalog-push:
	$(CONTAINER_ENGINE) push $(CATALOG_IMG)
