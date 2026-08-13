#!/bin/bash
# Verifies that manifests/00-cluster-role-sail-library.yaml matches the RBAC rules
# rendered from the vendored istiod Helm chart templates.
#
# Run this after bumping the sail-operator vendor dependency to catch any new
# istiod RBAC rules that need to be reflected in the manifest.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${REPO_ROOT}/manifests/00-cluster-role-sail-library.yaml"

echo "Verifying Sail RBAC against vendored Istio charts..."
go run "${REPO_ROOT}/cmd/ingress-operator" sail-rbac verify "${MANIFEST}"
