#!/bin/bash
set -euo pipefail

TMP_DIR="$(mktemp -d)"

OUTDIR="$TMP_DIR" SKIP_COPY=true ./hack/update-generated-crd.sh

diff -Naup "$TMP_DIR/operator.openshift.io_ingresscontrollers.yaml" manifests/00-custom-resource-definition.yaml
diff -Naup "$TMP_DIR/ingress.operator.openshift.io_dnsrecords.yaml" manifests/00-custom-resource-definition-internal.yaml
