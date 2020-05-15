#!/usr/bin/env bash

set -euo pipefail

oc scale --replicas 0 -n openshift-cluster-version deployments/cluster-version-operator
oc scale --replicas 0 -n openshift-ingress-operator deployments ingress-operator

IMAGE=$(oc get -n openshift-ingress-operator deployments/ingress-operator -o json | jq -r '.spec.template.spec.containers[0].env[] | select(.name=="IMAGE").value')
RELEASE_VERSION=$(oc get clusterversion/version -o json | jq -r '.status.desired.version')
NAMESPACE="${NAMESPACE:-"openshift-ingress-operator"}"
SHUTDOWN_FILE="${SHUTDOWN_FILE:-""}"

echo "Image: ${IMAGE}"
echo "Release version: ${RELEASE_VERSION}"
echo "Namespace: ${NAMESPACE}"

${DELVE:-} ./ingress-operator start --image "${IMAGE}" --release-version "${RELEASE_VERSION}" \
--namespace "${NAMESPACE}" --shutdown-file "${SHUTDOWN_FILE}" "$@"
