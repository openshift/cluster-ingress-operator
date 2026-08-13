#!/bin/bash
# Verifies that manifests/00-cluster-role-sail-library.yaml is a superset of all
# unconditional RBAC rules in the vendored istiod Helm chart templates.
#
# Run this after bumping the sail-operator vendor dependency to catch any new
# istiod RBAC rules that need to be reflected in the manifest.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${REPO_ROOT}/manifests/00-cluster-role-sail-library.yaml"
RESOURCES_DIR="${REPO_ROOT}/vendor/github.com/istio-ecosystem/sail-operator/resources"

LATEST_VERSION=$(ls "${RESOURCES_DIR}" | grep -v '\.go$' | sort -V | tail -1)
CLUSTERROLE_TEMPLATE="${RESOURCES_DIR}/${LATEST_VERSION}/charts/istiod/templates/clusterrole.yaml"
ROLE_TEMPLATE="${RESOURCES_DIR}/${LATEST_VERSION}/charts/istiod/templates/role.yaml"

echo "Verifying Sail RBAC against Istio ${LATEST_VERSION}..."
python3 "${REPO_ROOT}/hack/sail-rbac.py" verify "${MANIFEST}" "${CLUSTERROLE_TEMPLATE}" "${ROLE_TEMPLATE}"
