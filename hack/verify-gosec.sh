#!/bin/bash
set -euo pipefail

GO111MODULE=on GOFLAGS=-mod=vendor go run github.com/securego/gosec/cmd/gosec -severity medium -confidence medium -quiet ./...
