#!/bin/bash

set -euo pipefail

# Get Gateway API CRD bundle-version
BUNDLE_VERSION=$(oc get crds gateways.gateway.networking.k8s.io -ojson | jq -r '.metadata.annotations."gateway.networking.k8s.io/bundle-version"')

if [[ "${BUNDLE_VERSION}" == "null" ]]; then
    echo "Cannot find bundle-version annotation from Gateway API CRD"
    exit 1
fi
echo "Gateway API CRD bundle-version: ${BUNDLE_VERSION}"

GATEWAYCLASS_NAME=conformance

echo "Create GatewayClass \"${GATEWAYCLASS_NAME}\""
oc apply -f -<<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: conformance
spec:
  controllerName: openshift.io/gateway-controller/v1
EOF

oc wait --for=condition=Accepted=true "gatewayclass/$GATEWAYCLASS_NAME" --timeout=300s

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

if [[ "$BUNDLE_VERSION" = "v1.3.0" ]]; then
    echo "Cherry-picking fix for CoreDNS deployment image tag shortname issue in v1.3.0"
    git fetch origin 7f612b97fec9edd3aa32d193a4e9b4c3161ed09a
    git cherry-pick 7f612b97fec9edd3aa32d193a4e9b4c3161ed09a
fi

echo "Go version: $(go version)"
go mod vendor

# Modify the default timeout of "MaxTimeToConsistency" to make tests pass on AWS
# because the AWS ELB needs an extra ~60s for DNS propagation.  See
# <https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/best-practices-dns.html#DNS_change_propagation>.
# Also, GRPCRouteListenerHostnameMatching tests are taking longer than 150s to converge to passing.
sed -i -e '/MaxTimeToConsistency:/ s/30/360/' conformance/utils/config/timeout.go

SUPPORTED_FEATURES="BackendTLSPolicy,BackendTLSPolicySANValidation,GRPCRoute,Gateway,GatewayAddressEmpty,GatewayHTTPListenerIsolation,GatewayInfrastructurePropagation,GatewayPort8080,HTTPRoute,HTTPRouteBackendProtocolH2C,HTTPRouteBackendProtocolWebSocket,HTTPRouteBackendRequestHeaderModification,HTTPRouteBackendTimeout,HTTPRouteCORS,HTTPRouteDestinationPortMatching,HTTPRouteHostRewrite,HTTPRouteMethodMatching,HTTPRouteNamedRouteRule,HTTPRouteParentRefPort,HTTPRoutePathRedirect,HTTPRoutePathRewrite,HTTPRoutePortRedirect,HTTPRouteQueryParamMatching,HTTPRouteRequestMirror,HTTPRouteRequestMultipleMirrors,HTTPRouteRequestPercentageMirror,HTTPRouteRequestTimeout,HTTPRouteResponseHeaderModification,HTTPRouteSchemeRedirect,ReferenceGrant"
# skipping BackendTLSPolicyConflictResolution confornance test because upstream istio is not supporting this at the moment: https://github.com/istio/istio/blob/ba49f7398a8542c3612788e9bd0371c079e44165/tests/integration/pilot/gateway_conformance_test.go#L67
SKIPPED_TESTS="HTTPRouteCORSAllowCredentialsBehavior,GatewayStaticAddresses,BackendTLSPolicyConflictResolution" 

echo "Start Gateway API Conformance Testing"
go test ./conformance -v -timeout 60m -run TestConformance -args "--gateway-class=conformance" "--report-output=openshift.yaml" "--organization=Red Hat" "--project=Openshift Service Mesh" "--version=3.3.1" "--url=https://www.redhat.com/en/technologies/cloud-computing/openshift/container-platform" "--conformance-profiles=GATEWAY-HTTP,GATEWAY-GRPC" "--supported-features=${SUPPORTED_FEATURES}" "--skip-tests=${SKIPPED_TESTS}"
cat conformance/openshift.yaml
