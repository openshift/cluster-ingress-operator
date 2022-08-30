all: generate build

PACKAGE=github.com/openshift/cluster-ingress-operator
MAIN_PACKAGE=$(PACKAGE)/cmd/ingress-operator

BIN=$(lastword $(subst /, ,$(MAIN_PACKAGE)))

ifneq ($(DELVE),)
GO_GCFLAGS ?= -gcflags=all="-N -l"
endif

GO=GO111MODULE=on GOFLAGS=-mod=vendor go
GO_BUILD_RECIPE=CGO_ENABLED=0 $(GO) build -o $(BIN) $(GO_GCFLAGS) $(MAIN_PACKAGE)

TEST ?= TestAll

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
generate: update

.PHONY: bindata
bindata:
	hack/update-generated-bindata.sh

.PHONY: update
update: crd bindata

# Generate CRDs from vendored and internal API specs.
.PHONY: crd
crd:
	hack/update-generated-crd.sh
	hack/update-profile-manifests.sh

.PHONY: test
test:
	CGO_ENABLED=0 $(GO) test ./...

.PHONY: release-local
release-local:
	MANIFESTS=$(shell mktemp -d) hack/release-local.sh

.PHONY: test-e2e
test-e2e:
	CGO_ENABLED=0 $(GO) test -timeout 1h -count 1 -v -tags e2e -run "$(TEST)" ./test/e2e

.PHONY: test-e2e-list
test-e2e-list:
	@(cd ./test/e2e; E2E_TEST_MAIN_SKIP_SETUP=1 CGO_ENABLED=0 $(GO) test -list . -tags e2e | grep ^Test | sort)

.PHONY: clean
clean:
	$(GO) clean
	rm -f $(BIN)

.PHONY: verify
verify:
	hack/verify-gofmt.sh
	hack/verify-generated-crd.sh
	hack/verify-profile-manifests.sh
	hack/verify-generated-bindata.sh
	hack/verify-deps.sh
	hack/verify-e2e-test-all-presence.sh

.PHONY: uninstall
uninstall:
	hack/uninstall.sh

.PHONY: run-local
run-local: build
	hack/run-local.sh
