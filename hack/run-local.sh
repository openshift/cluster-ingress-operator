#!/usr/bin/env bash

set -euo pipefail

oc scale --replicas 0 -n openshift-cluster-version deployments/cluster-version-operator
oc scale --replicas 0 -n openshift-ingress-operator deployments ingress-operator

IMAGE=$(oc get -n openshift-ingress-operator deployments/ingress-operator -o json | jq -r '.spec.template.spec.containers[0].env[] | select(.name=="IMAGE").value')
RELEASE_VERSION=$(oc get clusterversion/version -o json | jq -r '.status.desired.version')

echo "Image: ${IMAGE}"
echo "Release version: ${RELEASE_VERSION}"

IMAGE="${IMAGE}" RELEASE_VERSION="${RELEASE_VERSION}" ./ingress-operator
