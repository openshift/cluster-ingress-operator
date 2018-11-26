#!/bin/bash
set -e
set -u
set -o pipefail

# Setup temporary GOPATH so we can install go-bindata from vendor
TMP_GOPATH=$(mktemp -d)
export GOPATH="${TMP_GOPATH}"

OUTDIR="${OUTDIR:-$PWD}"

function cleanup() {
    return_code=$?
    rm -rf "${TMP_GOPATH}"
    exit "${return_code}"
}
trap "cleanup" EXIT

# Install go-bindata
ln -s $(pwd)/vendor "${GOPATH}/src"
pushd "${GOPATH}" >/dev/null
go install github.com/kevinburke/go-bindata/...
popd >/dev/null

# Using "-modtime 1" to make generate target deterministic. It sets all file
# time stamps to unix timestamp 1
${GOPATH}/bin/go-bindata -mode 420 -modtime 1 -pkg manifests -o ${OUTDIR}/pkg/manifests/bindata.go assets/...

gofmt -s -w ${OUTDIR}/pkg/manifests/bindata.go
