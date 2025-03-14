#!/bin/bash

set -euo pipefail

# Get Gateway API CRD bundle-version
BUNDLE_VERSION=$(oc get crds gateways.gateway.networking.k8s.io -ojson | jq -r '.metadata.annotations."gateway.networking.k8s.io/bundle-version"')

if [[ "${BUNDLE_VERSION}" == "null" ]]; then
    echo "Cannot find bundle-version annotation from Gateway API CRD"
    exit 1
fi
echo "Gateway API CRD bundle-version: ${BUNDLE_VERSION}"

echo "Create GatewayClass gateway-conformance"
oc apply -f -<<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: gateway-conformance
spec:
  controllerName: openshift.io/gateway-controller
EOF

oc wait --for=condition=Accepted=true gatewayclass/gateway-conformance --timeout=300s

echo "All gatewayclass status:"
oc get gatewayclass -A

CLONE_DIR=$(mktemp -d)
cd "${CLONE_DIR}"

# find the branch of gateway-api repo
RELEASE_VERSION=$(\grep -oP "^\d+\.\d+" <<<"${BUNDLE_VERSION#v}")
BRANCH="release-${RELEASE_VERSION}"
echo "gateway-api repo branch \"${BRANCH}\" into ${CLONE_DIR}..."

# clone the branch that matched the installed CRDs version
# branch release-1.0 -> bundle-version v1.0.0
# branch release-1.1 -> bundle-version v1.1.1
# branch release-1.2 -> bundle-version v1.2.1
git clone --branch "${BRANCH}" https://github.com/kubernetes-sigs/gateway-api
cd gateway-api
echo "Go version: $(go version)"
go mod vendor

# Modify the default timeout of "MaxTimeToConsistency" to make tests pass on AWS
# because the AWS ELB needs an extra ~60s for DNS propagation.  See
# <https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/best-practices-dns.html#DNS_change_propagation>.
sed -i -e '/MaxTimeToConsistency:/ s/30/90/' conformance/utils/config/timeout.go

SUPPORTED_FEATURES="Gateway,HTTPRoute,ReferenceGrant,GatewayPort8080,HTTPRouteQueryParamMatching,HTTPRouteMethodMatching,HTTPRouteResponseHeaderModification,HTTPRoutePortRedirect,HTTPRouteSchemeRedirect,HTTPRoutePathRedirect,HTTPRouteHostRewrite,HTTPRoutePathRewrite,HTTPRouteRequestMirror,HTTPRouteRequestMultipleMirrors,HTTPRouteBackendProtocolH2C,HTTPRouteBackendProtocolWebSocket"

echo "Start Gateway API Conformance Testing"
go test ./conformance -v -timeout 10m -run TestConformance -args "--supported-features=${SUPPORTED_FEATURES}"
