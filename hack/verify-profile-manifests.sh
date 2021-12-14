#!/bin/bash

set -euo pipefail

REPO_ROOT="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." >/dev/null 2>&1 && pwd )"
MANIFESTS_DIR="${REPO_ROOT}/manifests"
SCRIPT_TMP="$(mktemp -d)"

cleanup() {
  rm -rf "${SCRIPT_TMP}"
}
trap cleanup EXIT

verify_manifests() {
  # Generate manifests in temporary directory
  cp -R "${MANIFESTS_DIR}" "${SCRIPT_TMP}/"
  MANIFESTS_DIR="${SCRIPT_TMP}/manifests" hack/update-profile-manifests.sh

  # Ensure there is no difference in existing manifests with the generated ones
  if ! diff -Na "${MANIFESTS_DIR}" "${SCRIPT_TMP}/manifests"; then
    echo "Manifest directories do not match. Ensure profile manifests are up to date by executing hack/update-profile-manifests.sh"
    exit 1
  fi
}

verify_manifests
