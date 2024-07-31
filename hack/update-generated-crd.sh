#!/bin/bash
set -euo pipefail

function install_crd {
  local SRC="$1"
  local DST="$2"
  if ! diff -Naup "$SRC" "$DST"; then
    cp "$SRC" "$DST"
    echo "installed CRD: $SRC => $DST"
  fi
}

# Can't rely on associative arrays for old Bash versions (e.g. OSX)
install_crd \
  "vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_50_ingress_00_ingresscontrollers-Default.crd.yaml" \
  "manifests/00-custom-resource-definition.yaml"

install_crd \
  "vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_50_ingress_00_ingresscontrollers-CustomNoUpgrade.crd.yaml" \
  "manifests/00-custom-resource-definition-CustomNoUpgrade.yaml"

install_crd \
  "vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_50_ingress_00_ingresscontrollers-DevPreviewNoUpgrade.crd.yaml" \
  "manifests/00-custom-resource-definition-DevPreviewNoUpgrade.yaml"

install_crd \
  "vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_50_ingress_00_ingresscontrollers-TechPreviewNoUpgrade.crd.yaml" \
  "manifests/00-custom-resource-definition-TechPreviewNoUpgrade.yaml"

install_crd \
  "vendor/github.com/openshift/api/operatoringress/v1/zz_generated.crd-manifests/0000_50_dns_01_dnsrecords.crd.yaml" \
  "manifests/00-custom-resource-definition-internal.yaml"
