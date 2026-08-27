#!/bin/bash
# Generates the istiod runtime RBAC rules in manifests/00-cluster-role-sail-library.yaml
# from the vendored istiod Helm chart templates, taking the union across all supported
# Istio version bands so the manifest is correct for any version running at runtime.
#
# Run this after bumping the sail-operator vendor dependency:
#   hack/update-sail-rbac.sh
#   git add manifests/00-cluster-role-sail-library.yaml
#   git commit -m "chore: regenerate Sail RBAC for Istio <version>"
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${REPO_ROOT}/manifests/00-cluster-role-sail-library.yaml"
RESOURCES_DIR="${REPO_ROOT}/vendor/github.com/istio-ecosystem/sail-operator/resources"

echo "Generating Sail RBAC from vendored Istio charts..."
python3 "${REPO_ROOT}/hack/sail-rbac.py" generate "${MANIFEST}" "${RESOURCES_DIR}"
echo "Done. Run 'make verify' to validate."
