#!/bin/bash
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-}"

if [ -z "${CLUSTER_NAME}" ]; then echo "CLUSTER_NAME is required"; exit 1; fi
if [ -z "${KUBECONFIG}" ]; then echo "KUBECONFIG is required"; exit 1; fi

# Required for the operator-sdk.
export KUBERNETES_CONFIG="${KUBECONFIG}"

go test -v -tags integration ./test/integration --cluster-name "${CLUSTER_NAME}"
