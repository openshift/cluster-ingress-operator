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
  vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_50_ingress_00_ingresscontrollers?(-Default).crd.yaml \
  "manifests/00-custom-resource-definition.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_50_ingress_00_ingresscontrollers-CustomNoUpgrade.crd.yaml" \
  "manifests/00-custom-resource-definition-CustomNoUpgrade.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_50_ingress_00_ingresscontrollers-DevPreviewNoUpgrade.crd.yaml" \
  "manifests/00-custom-resource-definition-DevPreviewNoUpgrade.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_50_ingress_00_ingresscontrollers-TechPreviewNoUpgrade.crd.yaml" \
  "manifests/00-custom-resource-definition-TechPreviewNoUpgrade.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operatoringress/v1/zz_generated.crd-manifests/0000_50_dns_01_dnsrecords.crd.yaml" \
  "manifests/00-custom-resource-definition-internal.yaml"

verify_crd \
  "vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_50_ingress_00_ingresscontrollers-OKD.crd.yaml" \
  "manifests/00-custom-resource-definition-OKD.yaml"
