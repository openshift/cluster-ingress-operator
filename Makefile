all: build

APP_NAME=cluster-ingress-operator
BIN=cluster-ingress-operator
MAIN_PKG=github.com/openshift/$(APP_NAME)/cmd/cluster-ingress-operator
ENVVAR=GOOS=linux GOARCH=amd64 CGO_ENABLED=0
GOOS=linux
GO_BUILD_RECIPE=GOOS=$(GOOS) go build -o $(BIN) $(MAIN_PKG)

build: $(BIN)

$(BIN): $(SRC)
	$(GO_BUILD_RECIPE)


.PHONY: all build
