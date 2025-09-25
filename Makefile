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

.PHONY: build
build: generate
	$(GO_BUILD_RECIPE)

.PHONY: tests-ext-build
tests-ext-build:
	# GO_COMPLIANCE_POLICY="exempt_all" must only be used for test related binaries.
	# It prevents various FIPS compliance policies from being applied to this compilation.
	# Do not set globally.
	GO_COMPLIANCE_POLICY="exempt_all" go build ./cmd/ingress-operator-ext-tests/...

.PHONY: tests-ext-update
tests-ext-update: tests-ext-build
	./ingress-operator-ext-tests update
	for f in .openshift-tests-extension/*.json; do \
		jq 'map(del(.codeLocations))' "$$f" > tmpp && mv tmpp "$$f"; \
	done

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
