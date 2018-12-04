all: generate build

PACKAGE=github.com/openshift/cluster-ingress-operator
MAIN_PACKAGE=$(PACKAGE)/cmd/cluster-ingress-operator

BIN=$(lastword $(subst /, ,$(MAIN_PACKAGE)))

ENVVAR=GOOS=linux CGO_ENABLED=0
GOOS=linux
GO_BUILD_RECIPE=GOOS=$(GOOS) go build -o $(BIN) $(MAIN_PACKAGE)

.PHONY: build
build:
	$(GO_BUILD_RECIPE)

.PHONY: generate
generate:
	hack/update-generated-bindata.sh

.PHONY: test
test: verify
	go test ./...

.PHONY: release-local
release-local:
	MANIFESTS=$(shell mktemp -d) hack/release-local.sh

.PHONY: test-e2e
test-e2e:
	KUBERNETES_CONFIG="$(KUBECONFIG)" go test -v -tags e2e ./...

.PHONY: clean
clean:
	go clean
	rm -f $(BIN)

.PHONY: verify
verify:
	hack/verify-gofmt.sh
	hack/verify-generated-bindata.sh
