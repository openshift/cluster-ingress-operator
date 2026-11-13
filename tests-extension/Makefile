GO_PKG_NAME := github.com/openshift-eng/openshift-tests-extension

GIT_COMMIT    := $(shell git rev-parse --short HEAD)
BUILD_DATE    := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
GIT_TREE_STATE := $(shell if git diff --quiet; then echo clean; else echo dirty; fi)

LDFLAGS := -X '$(GO_PKG_NAME)/pkg/version.CommitFromGit=$(GIT_COMMIT)' \
           -X '$(GO_PKG_NAME)/pkg/version.BuildDate=$(BUILD_DATE)' \
           -X '$(GO_PKG_NAME)/pkg/version.GitTreeState=$(GIT_TREE_STATE)'

TOOLS_BIN_DIR := $(CURDIR)/bin
BINARY        := $(TOOLS_BIN_DIR)/cluster-ingress-operator-tests-ext

# GO_COMPLIANCE_POLICY="exempt_all" must only be used for test related binaries.
# It prevents various FIPS compliance policies from being applied to this compilation.
.PHONY: build
build: ## Build the extended tests binary
	@mkdir -p $(TOOLS_BIN_DIR)
	GO_COMPLIANCE_POLICY="exempt_all" go build -ldflags "$(LDFLAGS)" -mod=vendor \
		-o $(BINARY) ./cmd/...

.PHONY: list-test-names
list-test-names: build ## List all registered test names
	@$(BINARY) list -o names

.PHONY: verify
verify: tidy fmt vet ## Run all verification checks

.PHONY: tidy
tidy: ## Run go mod tidy
	go mod tidy

.PHONY: fmt
fmt: ## Check formatting
	@files=$$(find . -name '*.go' -not -path './vendor/*' -exec gofmt -l {} \;); \
	if [ -n "$$files" ]; then \
		echo "Files that need formatting:"; \
		echo "$$files"; \
		echo "Run: find . -name '*.go' -not -path './vendor/*' -exec gofmt -w {} \\;"; \
		exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...
