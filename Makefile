all: build

PACKAGE=github.com/openshift/cluster-ingress-operator
MAIN_PACKAGE=$(PACKAGE)/cmd/cluster-ingress-operator

BIN=$(lastword $(subst /, ,$(MAIN_PACKAGE)))
BINDATA=pkg/manifests/bindata.go

GOBINDATA_BIN=$(GOPATH)/bin/go-bindata

ENVVAR=GOOS=linux GOARCH=amd64 CGO_ENABLED=0
GOOS=linux
GO_BUILD_RECIPE=GOOS=$(GOOS) go build -o $(BIN) $(MAIN_PACKAGE)

build:
	$(GO_BUILD_RECIPE)

generate: $(GOBINDATA_BIN)
	# Using "-modtime 1" to make generate target deterministic. It sets all file time stamps to unix timestamp 1
	go-bindata -mode 420 -modtime 1 -pkg manifests -o $(BINDATA) assets/...

$(GOBINDATA_BIN):
	go get -u github.com/jteeuwen/go-bindata/...

test:
	go test ./...

.PHONY: all build generate test
