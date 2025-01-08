#!/bin/bash

set -euo pipefail

# Get Gateway API CRD bundle-version
BUNDLE_VERSION=$(oc get crds gateways.gateway.networking.k8s.io -ojson | jq -r '.metadata.annotations."gateway.networking.k8s.io/bundle-version"')

if [[ "${BUNDLE_VERSION}" == "null" ]]; then
    echo "Cannot find bundle-version annotation from Gateway API CRD"
    exit 1
fi
echo "Gateway API CRD bundle-version: ${BUNDLE_VERSION}"

echo "Creat GatewayClass gateway-conformance"
oc apply -f -<<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: gateway-conformance
spec:
  controllerName: openshift.io/gateway-controller
EOF

oc wait --for=condition=Accepted=true gatewayclass/gateway-conformance --timeout=300s

echo "All gatewayclass ststus:"
oc get gatewayclass -A

echo "Go version: $(go version)"
cd ../..
mkdir -p kubernetes-sigs
cd kubernetes-sigs
rm -rf gateway-api

# find the branch of gateway-api repo
RELEASE_VERSION=${BUNDLE_VERSION/v/}
RELEASE_VERSION=$(echo ${RELEASE_VERSION} | grep -oP "^\d+\.\d+")
BRANCH="release-${RELEASE_VERSION}"
echo "gateway-api repo branch: ${BRANCH}"

# clone the branch that matched the installed CRDs version
# branch release-1.0 -> bundle-version v1.0.0
# branch release-1.1 -> bundle-version v1.1.1
# branch release-1.2 -> bundle-version v1.2.1
git clone --branch ${BRANCH} https://github.com/kubernetes-sigs/gateway-api
cd gateway-api
go mod vendor

# modify default timeout of "MaxTimeToConsistency" to make tests pass on AWS
sed -i "s/MaxTimeToConsistency:              30/MaxTimeToConsistency:              90/g" conformance/utils/config/timeout.go

echo "Start Gateway API Conformance Testing"
go test ./conformance -v -timeout 0 -run TestConformance -args --supported-features=Gateway,HTTPRoute
