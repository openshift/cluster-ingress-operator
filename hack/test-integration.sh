#!/bin/bash
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-}"
MANIFESTS="${MANIFESTS:-}"

if [ -z "${CLUSTER_NAME}" ]; then echo "CLUSTER_NAME is required"; exit 1; fi
if [ -z "${MANIFESTS}" ]; then echo "MANIFESTS is required"; exit 1; fi

export WATCH_NAMESPACE="openshift-cluster-ingress-operator"
export KUBERNETES_CONFIG="${KUBECONFIG}"
go test -v -tags integration ./test/integration --manifests-dir "${MANIFESTS}" --cluster-name "${CLUSTER_NAME}"
