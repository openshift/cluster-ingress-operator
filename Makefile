all: generate build

PACKAGE=github.com/openshift/cluster-ingress-operator
MAIN_PACKAGE=$(PACKAGE)/cmd/cluster-ingress-operator

BIN=$(lastword $(subst /, ,$(MAIN_PACKAGE)))

GO_BUILD_RECIPE=CGO_ENABLED=0 go build -o $(BIN) $(MAIN_PACKAGE)

TEST ?= .*

.PHONY: build
build:
	$(GO_BUILD_RECIPE)

.PHONY: generate
generate:
	hack/update-generated-bindata.sh
	hack/update-codegen.sh

.PHONY: test
test: verify
	go test ./...

.PHONY: release-local
release-local:
	MANIFESTS=$(shell mktemp -d) hack/release-local.sh

.PHONY: test-e2e
test-e2e:
	KUBERNETES_CONFIG="$(KUBECONFIG)" WATCH_NAMESPACE=openshift-ingress-operator go test -count 1 -v -tags e2e -run "$(TEST)" ./...

.PHONY: clean
clean:
	go clean
	rm -f $(BIN)

.PHONY: verify
verify:
	hack/verify-gofmt.sh
	hack/verify-generated-bindata.sh
	hack/verify-codegen.sh

.PHONY: uninstall
uninstall:
	hack/uninstall.sh
