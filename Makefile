all: generate build

PACKAGE=github.com/openshift/cluster-ingress-operator
MAIN_PACKAGE=$(PACKAGE)/cmd/ingress-operator

BIN=$(lastword $(subst /, ,$(MAIN_PACKAGE)))

GO=GO111MODULE=on GOFLAGS=-mod=vendor go
GO_BUILD_RECIPE=CGO_ENABLED=0 $(GO) build -o $(BIN) $(MAIN_PACKAGE)

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
generate: bindata crd

.PHONY: bindata
bindata:
	hack/update-generated-bindata.sh

# Generate IngressController CRD from vendored API spec.
.PHONY: crd
crd:
	hack/update-generated-crd.sh

.PHONY: verify-bindata
verify-bindata:
	hack/verify-generated-bindata.sh

# Do not write the IngressController CRD, only compare and return (code 1 if dirty).
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
	$(GO) test -count 1 -v -tags e2e -run "$(TEST)" ./...

.PHONY: clean
clean:
	$(GO) clean
	rm -f $(BIN)

.PHONY: verify
verify: verify-bindata verify-crd
	hack/verify-gofmt.sh

.PHONY: uninstall
uninstall:
	hack/uninstall.sh
