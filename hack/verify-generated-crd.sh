#!/bin/bash
set -euo pipefail

TMP_DIR="$(mktemp -d)"
VENDORED_CRD="vendor/github.com/openshift/api/operator/v1/0000_50_ingress-operator_00-custom-resource-definition.yaml"
LOCAL_CRD="manifests/00-custom-resource-definition.yaml"

OUTDIR="$TMP_DIR" SKIP_COPY=true ./hack/update-generated-crd.sh

diff -Naup "$TMP_DIR/ingress.operator.openshift.io_dnsrecords.yaml" manifests/00-custom-resource-definition-internal.yaml
diff -Naup "$VENDORED_CRD" "$LOCAL_CRD"
