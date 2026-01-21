#!/bin/bash
set -euo pipefail

# This script updates Istio Helm charts from the sail-operator Go module
# for use in the Gateway API Helm installation POC.
#
# The charts are copied from the Go module cache, which is populated by
# the sail-operator dependency in go.mod.
#
# All available Istio versions are copied, allowing runtime version selection.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

SAIL_OPERATOR_MODULE="github.com/istio-ecosystem/sail-operator"
CHARTS_DEST="${REPO_ROOT}/pkg/manifests/assets/istio/charts"

echo "Updating Istio charts from sail-operator..."

# Ensure the sail-operator module is in the module cache
echo "Downloading sail-operator module to cache..."
go mod download "${SAIL_OPERATOR_MODULE}"

# Get the module cache directory for sail-operator
MODULE_DIR=$(go mod download -json "${SAIL_OPERATOR_MODULE}" 2>/dev/null | jq -r '.Dir')

if [ -z "${MODULE_DIR}" ] || [ ! -d "${MODULE_DIR}" ]; then
    echo "ERROR: Failed to locate sail-operator in module cache"
    echo "Try running: go mod download ${SAIL_OPERATOR_MODULE}"
    exit 1
fi

echo "Using sail-operator from: ${MODULE_DIR}"

# Check if resources directory exists
RESOURCES_DIR="${MODULE_DIR}/resources"
if [ ! -d "${RESOURCES_DIR}" ]; then
    echo "ERROR: Resources directory not found at ${RESOURCES_DIR}"
    exit 1
fi

# Get list of available Istio versions
echo "Discovering available Istio versions..."
VERSIONS=$(ls -1 "${RESOURCES_DIR}" | grep "^v[0-9]" || true)

if [ -z "${VERSIONS}" ]; then
    echo "ERROR: No Istio versions found in ${RESOURCES_DIR}"
    exit 1
fi

echo "Found Istio versions:"
echo "${VERSIONS}" | sed 's/^/  - /'
echo ""

# Remove existing charts directory
if [ -d "${CHARTS_DEST}" ]; then
    echo "Removing existing charts directory..."
    # Files from module cache are read-only, make them writable first
    chmod -R +w "${CHARTS_DEST}" 2>/dev/null || true
    rm -rf "${CHARTS_DEST}"
fi

# Create destination directory
mkdir -p "${CHARTS_DEST}"

# Copy all Istio versions
COPIED_COUNT=0
FAILED_COUNT=0

for VERSION in ${VERSIONS}; do
    CHART_SOURCE="${RESOURCES_DIR}/${VERSION}/charts/istiod"
    CHART_DEST="${CHARTS_DEST}/${VERSION}"

    if [ ! -d "${CHART_SOURCE}" ]; then
        echo "⚠ Skipping ${VERSION}: istiod chart not found"
        FAILED_COUNT=$((FAILED_COUNT + 1))
        continue
    fi

    echo "Copying ${VERSION}..."
    mkdir -p "${CHART_DEST}"
    cp -r "${CHART_SOURCE}" "${CHART_DEST}/"

    # Make copied files writable (they're read-only from module cache)
    chmod -R +w "${CHART_DEST}"

    # Verify the chart was copied
    if [ ! -f "${CHART_DEST}/istiod/Chart.yaml" ]; then
        echo "✗ ERROR: ${VERSION} - Chart.yaml not found after copy"
        FAILED_COUNT=$((FAILED_COUNT + 1))
        continue
    fi

    COPIED_COUNT=$((COPIED_COUNT + 1))
done

echo ""
echo "=========================================="
echo "Successfully copied ${COPIED_COUNT} Istio chart version(s)"
if [ ${FAILED_COUNT} -gt 0 ]; then
    echo "Failed to copy ${FAILED_COUNT} version(s)"
fi
echo "=========================================="
echo ""
echo "Charts location: ${CHARTS_DEST}"
