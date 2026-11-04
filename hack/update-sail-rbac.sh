#!/bin/bash
# Generates the istiod runtime RBAC rules in manifests/00-cluster-role-sail-library.yaml
# from the vendored istiod Helm chart templates.
#
# Run this after bumping the sail-operator vendor dependency:
#   hack/update-sail-rbac.sh
#   git add manifests/00-cluster-role-sail-library.yaml
#   git commit -m "chore: regenerate Sail RBAC for Istio <version>"
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${REPO_ROOT}/manifests/00-cluster-role-sail-library.yaml"

echo "Generating Sail RBAC from vendored Istio charts..."
go run "${REPO_ROOT}/cmd/ingress-operator" sail-rbac generate "${MANIFEST}"
echo "Done. Run 'make verify' to validate."
