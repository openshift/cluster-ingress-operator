#!/bin/bash
set -euo pipefail

OUTDIR="${OUTDIR:-}"
if [[ -z "$OUTDIR" ]]; then
  OUTDIR="$(mktemp -d)"
fi

# Generating against the github.com/openshift/api/operator/v1 will currently
# generate some failures until other types' markers and structures are updated
# to be compatible with the latest metadata expectations. For now, generate the
# minimal set of type files we know are needed for the ingress types.
set -x
GO111MODULE=on GOFLAGS=-mod=vendor go run sigs.k8s.io/controller-tools/cmd/controller-gen crd:trivialVersions=true \
  paths=./vendor/github.com/openshift/api/operator/v1/doc.go\;./vendor/github.com/openshift/api/operator/v1/types.go\;./vendor/github.com/openshift/api/operator/v1/types_ingress.go \
  output:crd:dir="$OUTDIR"
set +x

if [[ -z "${SKIP_COPY+1}" ]]; then
  cp "$OUTDIR/operator.openshift.io_ingresscontrollers.yaml" manifests/00-custom-resource-definition.yaml
fi
