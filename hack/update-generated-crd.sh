#!/bin/bash
set -euo pipefail

VENDORED_CRD="vendor/github.com/openshift/api/operator/v1/0000_50_ingress-operator_00-custom-resource-definition.yaml"
LOCAL_CRD="manifests/00-custom-resource-definition.yaml"
OUTDIR="${OUTDIR:-}"
if [[ -z "$OUTDIR" ]]; then
  OUTDIR="$(mktemp -d)"
fi

set -x
GO111MODULE=on GOFLAGS=-mod=vendor go run sigs.k8s.io/controller-tools/cmd/controller-gen crd:trivialVersions=true paths=./pkg/api/v1 output:crd:dir="$OUTDIR"
set +x

if [[ -z "${SKIP_COPY+1}" ]]; then
  cp "$OUTDIR/ingress.operator.openshift.io_dnsrecords.yaml" manifests/00-custom-resource-definition-internal.yaml
  if ! diff -Naup "$VENDORED_CRD" "$LOCAL_CRD"; then
    cp "$VENDORED_CRD" "$LOCAL_CRD"
  fi
fi
