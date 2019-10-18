#!/bin/bash
set -euo pipefail

OUTDIR="${OUTDIR:-$PWD}"

# Using "-modtime 1" to make generate target deterministic. It sets all file
# time stamps to unix timestamp 1
GO111MODULE=on GOFLAGS=-mod=vendor go run github.com/kevinburke/go-bindata/go-bindata -mode 420 -modtime 1 -pkg manifests -o ${OUTDIR}/pkg/manifests/bindata.go assets/... manifests/...

gofmt -s -w ${OUTDIR}/pkg/manifests/bindata.go
