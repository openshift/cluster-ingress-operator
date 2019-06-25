#!/usr/bin/env bash

set -euo pipefail


IMAGE=$(oc get -n openshift-ingress-operator deployments/ingress-operator -o json | jq -r '.spec.template.spec.containers[0].env[] | select(.name=="IMAGE").value')
RELEASE_VERSION=$(oc get clusterversion/version -o json | jq -r '.status.desired.version')

IMAGE="$IMAGE" RELEASE_VERSION="$RELEASE_VERSION" ./ingress-operator
