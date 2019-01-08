#!/bin/bash
set -euo pipefail

# Setup temporary GOPATH so we can install code-generator binaries from vendor
TMP_GOPATH=$(mktemp -d)

OP_MSG="Generating"
verify=
if [[ -n "${VERIFY:-}" ]]; then
    OP_MSG="Verifying"
    verify="--verify-only"
fi

function cleanup() {
    return_code=$?
    rm -rf "${TMP_GOPATH}"
    exit "${return_code}"
}
trap "cleanup" EXIT

# Install code-generator binaries
ln -s $(pwd)/vendor "${TMP_GOPATH}/src"
pushd "${TMP_GOPATH}" >/dev/null
GOPATH=${TMP_GOPATH} go install k8s.io/code-generator/cmd/{deepcopy-gen,conversion-gen,defaulter-gen,openapi-gen}
popd >/dev/null

function codegen::join() { local IFS="$1"; shift; echo "$*"; }

INGRESS_PKG="github.com/openshift/cluster-ingress-operator"

# enumerate group versions
ALL_FQ_APIS=(
    github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1
)

# Needed for empty header
touch /tmp/boilerplate

echo "${OP_MSG} deepcopy funcs"
${TMP_GOPATH}/bin/deepcopy-gen \
    -i $(codegen::join , "${ALL_FQ_APIS[@]}") \
    -h /tmp/boilerplate \
    -O zz_generated.deepcopy \
    --bounding-dirs $(codegen::join , "${ALL_FQ_APIS[@]}") \
    ${verify} "$@"

echo "${OP_MSG} conversion funcs"
${TMP_GOPATH}/bin/conversion-gen \
    -i $(codegen::join , "${ALL_FQ_APIS[@]}") \
    -h /tmp/boilerplate \
    -O zz_generated.conversion \
    ${verify} "$@"

echo "${OP_MSG} defaulter funcs"
${TMP_GOPATH}/bin/defaulter-gen \
    -i $(codegen::join , "${ALL_FQ_APIS[@]}") \
    -h /tmp/boilerplate \
    -O zz_generated.defaults \
    ${verify} "$@"

: '
# TODO: Enable this once we fix openapi verify (Error: unable to read file "-" for comparison)
echo "${OP_MSG} OpenAPI definitions"
${TMP_GOPATH}/bin/openapi-gen \
    -i $(codegen::join , "${ALL_FQ_APIS[@]}") \
    -h /tmp/boilerplate \
    -O zz_generated.openapi \
    -p "${INGRESS_PKG}/pkg/openapi" \
    ${verify} "$@"
'
