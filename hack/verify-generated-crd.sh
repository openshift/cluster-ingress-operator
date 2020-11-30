#!/bin/bash
set -euo pipefail

function verify_crd {
  local SRC="$1"
  local DST="$2"
  if ! diff -Naup "$SRC" "$DST"; then
    echo "invalid CRD: $SRC => $DST"
    exit 1
  fi
}

verify_crd \
  "vendor/github.com/openshift/api/operator/v1/0000_50_ingress-operator_00-ingresscontroller.crd.yaml" \
  "manifests/00-custom-resource-definition.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operatoringress/v1/0000_50_dns-record.yaml" \
  "manifests/00-custom-resource-definition-internal.yaml"
