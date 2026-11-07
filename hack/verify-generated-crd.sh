#!/bin/bash
set -euo pipefail

function verify_crd {
  local SRC="$1"
  local DST="$2"
  if [[ -e "$SRC" ]]; then
    if [[ -e "$DST" ]]; then
      if ! diff -Naup "$SRC" "$DST"; then
        echo "inconsistent CRD: $SRC => $DST"
        exit 1
      fi
    else
      echo "missing CRD: $SRC => $DST"
      exit 1
    fi
  else
    if [[ -e "$DST" ]]; then
      echo "extra CRD: $DST"
      exit 1
    fi
  fi
}

shopt -s extglob
verify_crd \
  vendor/github.com/openshift/api/operatoringress/v1/zz_generated.crd-manifests/0000_50_dns_01_dnsrecords?(-Default).crd.yaml \
  "manifests/00-custom-resource-definition-internal.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operatoringress/v1/zz_generated.crd-manifests/0000_50_dns_01_dnsrecords-CustomNoUpgrade.crd.yaml" \
  "manifests/00-custom-resource-definition-internal-CustomNoUpgrade.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operatoringress/v1/zz_generated.crd-manifests/0000_50_dns_01_dnsrecords-DevPreviewNoUpgrade.crd.yaml" \
  "manifests/00-custom-resource-definition-internal-DevPreviewNoUpgrade.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operatoringress/v1/zz_generated.crd-manifests/0000_50_dns_01_dnsrecords-TechPreviewNoUpgrade.crd.yaml" \
  "manifests/00-custom-resource-definition-internal-TechPreviewNoUpgrade.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operatoringress/v1/zz_generated.crd-manifests/0000_50_dns_01_dnsrecords-OKD.crd.yaml" \
  "manifests/00-custom-resource-definition-internal-OKD.yaml"

# openshift/api owns the ingresses and ingresscontrollers operator.openshift.io CRDs in the release
# payload.  Keep it out of this component's payload to avoid applying two
# independently ordered copies of the same CRD.
if ingress_crd_manifests=$(GO111MODULE=on GOFLAGS=-mod=vendor go run ./hack/verify-generated-crd manifests); then
  if [[ -z "$ingress_crd_manifests" ]]; then
    exit 0
  fi
  printf '%s\n' "$ingress_crd_manifests"
  echo "the ingresses and ingresscontrollers operator.openshift.io CRDs must only ship from openshift/api"
  exit 1
else
  scan_status=$?
  echo "failed to scan manifests for ingresses/ingresscontrollers.operator.openshift.io CRDs" >&2
  exit "$scan_status"
fi
