#!/bin/bash

set -euo pipefail

REPO_ROOT="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." >/dev/null 2>&1 && pwd )"
SCRIPT_TMP="$(mktemp -d)"
MANIFESTS_DIR="${MANIFESTS_DIR:-${REPO_ROOT}/manifests}"
PROFILE_PATCHES_DIR="${REPO_ROOT}/profile-patches"
OC_CLI="oc"
MANIFEST_HEADER="# WARNING: DO NOT EDIT, generated by hack/update-profile-manifests.sh"

ensure_oc() {
  if which "${OC_CLI}" &> /dev/null; then
    return
  fi

  local os_type="linux"
  if [[ "${OSTYPE}" == "darwin"* ]]; then
    os_type="macosx"
  fi

  local download_url="https://mirror.openshift.com/pub/openshift-v4/clients/oc/latest/${os_type}/oc.tar.gz"
  curl --silent --fail --location "${download_url}" --output - | tar -C "${SCRIPT_TMP}" -xz
  OC_CLI="${SCRIPT_TMP}/oc"
  chmod +x "${OC_CLI}"
}

cleanup() {
  rm -rf "${SCRIPT_TMP}"
}
trap cleanup EXIT

apply_patch() {
  local profile="$1"
  local patch_file="$2"

  local file_to_patch
  file_to_patch="$(basename "${patch_file}")"
  file_to_patch="${file_to_patch%.*}"               # Remove .patch extension
  file_to_patch="${MANIFESTS_DIR}/${file_to_patch}" # Prefix with manifests directory path

  local target_file="${file_to_patch}"
  target_file="${target_file%.*}"                   # Remove .yaml extension
  target_file="${target_file}-${profile}.yaml"      # Append profile name as suffix and restore extension
  tmp_manifest=${SCRIPT_TMP}/tmp.yaml

  "${OC_CLI}" patch --local \
    --filename "${file_to_patch}" --type=json \
    --patch "$(< ${patch_file})" \
    --output yaml > "${tmp_manifest}"

  { echo "${MANIFEST_HEADER}"; cat "${tmp_manifest}"; } > "${target_file}"
}

# Iterates over patch files inside a profile directory and
# applies them, resulting in profile-specific manifests in
# the manifests directory
apply_profile_patches() {
  local profile_dir="$1"
  for patch_file in "${profile_dir}"*.patch; do
    apply_patch "$(basename "${profile_dir}")" "${patch_file}"
  done
}

# Iterates over profile directories under the profile-patches
# directory and applies all patches within
apply_all_profile_patches() {
  for profile_dir in "${PROFILE_PATCHES_DIR}/"*/; do
    apply_profile_patches "${profile_dir}"
  done
}

ensure_oc
apply_all_profile_patches
