all: generate build

PACKAGE=github.com/openshift/cluster-ingress-operator
MAIN_PACKAGE=$(PACKAGE)/cmd/ingress-operator

BIN=$(lastword $(subst /, ,$(MAIN_PACKAGE)))

ifneq ($(DELVE),)
GO_GCFLAGS ?= -gcflags=all="-N -l"
endif

GO=GO111MODULE=on GOFLAGS=-mod=vendor go
GO_BUILD_RECIPE=CGO_ENABLED=0 $(GO) build -o $(BIN) $(GO_GCFLAGS) $(MAIN_PACKAGE)

TEST ?= .*

.PHONY: build
build:
	$(GO_BUILD_RECIPE)

.PHONY: buildconfig
buildconfig:
	hack/create-buildconfig.sh

.PHONY: cluster-build
cluster-build:
	hack/start-build.sh

# TODO: Add deepcopy generation script/target
.PHONY: generate
generate: crd bindata

.PHONY: bindata
bindata:
	hack/update-generated-bindata.sh

# Generate CRDs from vendored and internal API specs.
.PHONY: crd
crd:
	hack/update-generated-crd.sh

.PHONY: verify-bindata
verify-bindata:
	hack/verify-generated-bindata.sh

# Do not write the CRDs, only compare and return (code 1 if dirty).
.PHONY: verify-crd
verify-crd:
	hack/verify-generated-crd.sh

.PHONY: test
test: verify
	$(GO) test ./...

.PHONY: release-local
release-local:
	MANIFESTS=$(shell mktemp -d) hack/release-local.sh

.PHONY: test-e2e
test-e2e:
	$(GO) test -count 1 -v -tags e2e -run "$(TEST)" ./test/e2e

.PHONY: clean
clean:
	$(GO) clean
	rm -f $(BIN)

.PHONY: verify
verify: verify-crd verify-bindata
	hack/verify-gofmt.sh

.PHONY: uninstall
uninstall:
	hack/uninstall.sh

.PHONY: run-local
run-local:
	hack/run-local.sh
