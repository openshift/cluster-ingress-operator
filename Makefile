all: build

APP_NAME=cluster-ingress-operator
OS_OUTPUT_BINPATH=_output/bin
BIN=$(OS_OUTPUT_BINPATH)/$(APP_NAME)
MAIN_PKG=github.com/openshift/$(APP_NAME)/cmd/$(APP_NAME)
ENVVAR=GOOS=linux GOARCH=amd64 CGO_ENABLED=0
GOOS=linux

build:
	mkdir -p $(OS_OUTPUT_BINPATH)
	GOOS=$(GOOS) go build -o $(BIN) $(MAIN_PKG)

clean:
	rm -rf $(OS_OUTPUT_BINPATH)

.PHONY: all build
