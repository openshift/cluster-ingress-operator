#!/bin/bash

set -euo pipefail

# Absolute path to this script's directory, used to locate carried gateway-api
# patches after the script cds into the clone directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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

# Carry a local fix for MakeTLSRequestAndExpectFailureResponse in the conformance
# suite: the helper is one-shot and treats any request error as the expected TLS
# rejection, so pre-handshake connection failures (DNS propagation lag for freshly
# provisioned AWS ELBs) false-fail the Gateway client-certificate validation tests.
# The patch makes the helper retry until the TLS handshake reaches the
# certificate-request stage (the GetClientCertificateHook fires) or a deadline
# passes, while preserving the original semantics otherwise. Applied with git
# apply so a diverging upstream branch fails the job loudly instead of silently
# regressing to the flaky behavior. Drop once the fix lands on the cloned branch.
git apply "${SCRIPT_DIR}/gateway-api-conformance-tls-failure-retry.patch"
echo "Applied gateway-api TLS failure-retry patch"

echo "Go version: $(go version)"
if [[ -f go.work ]]; then
    go work vendor
else
    GOWORK=off go mod vendor
fi

# Modify the default timeout of "MaxTimeToConsistency" to make tests pass on AWS
# because the AWS ELB needs an extra ~60s for DNS propagation.  See
# <https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/best-practices-dns.html#DNS_change_propagation>.
# Also, GRPCRouteListenerHostnameMatching tests are taking longer than 150s to converge to passing.
sed -i -e '/MaxTimeToConsistency:/ s/30/360/' conformance/utils/config/timeout.go

# Features NOT claimed in SUPPORTED_FEATURES (tests auto-skipped):
# - GatewayStaticAddresses: we don't support specific address pool configuration (https://redhat.atlassian.net/browse/NE-2416).
# - GatewayBackendClientCertificate, GatewayHTTPSListenerDetectMisdirectedRequests:
#   not supported by Istio 1.31.1 (https://github.com/istio/istio/blob/1.31.1/pilot/pkg/config/kube/gateway/supported_features.go#L22).
# - ListenerSet: not supported by the OpenShift Gateway API implementation even though
#   Istio 1.31.1 supports it; the operator configures Istio to ignore listenersets and
#   marks them NotAccepted (see PILOT_IGNORE_RESOURCES in
#   pkg/operator/controller/gatewayclass/istio_olm.go). Downstream enablement is tracked
#   by https://redhat.atlassian.net/browse/NE-2598.
# - HTTPRouteRetry, HTTPRouteRetryBackendTimeout, HTTPRouteRetryConnectionError: retry is
#   not part of the Gateway API v1.6.x standard-channel HTTPRoute schema installed here;
#   it exists only in the experimental channel. With strict validation the API server
#   rejects spec.rules[N].retry as an unknown field; otherwise the field may be pruned.
#   Retry is in the standard channel by Gateway API v1.7.0.
# - UDPRoute: the UDPRoute conformance tests are part of the GATEWAY-UDP profile,
#   which is not run because the OpenShift Gateway API implementation does not
#   support UDPRoute; the operator configures Istio to ignore udproutes
#   (see PILOT_IGNORE_RESOURCES in pkg/operator/controller/gatewayclass/istio_olm.go).
SUPPORTED_FEATURES="BackendTLSPolicy,BackendTLSPolicySANValidation,Gateway,GatewayAddressEmpty,GatewayFrontendClientCertificateValidation,GatewayFrontendClientCertificateValidationInsecureFallback,GatewayHTTPListenerIsolation,GatewayInfrastructure,GatewayPort8080,GRPCRoute,HTTPRoute,HTTPRoute303RedirectStatusCode,HTTPRoute307RedirectStatusCode,HTTPRoute308RedirectStatusCode,HTTPRouteBackendProtocolH2C,HTTPRouteBackendProtocolWebSocket,HTTPRouteBackendRequestHeaderModification,HTTPRouteBackendTimeout,HTTPRouteCORS,HTTPRouteDestinationPortMatching,HTTPRouteHostRewrite,HTTPRouteMethodMatching,HTTPRouteNamedRouteRule,HTTPRouteParentRefPort,HTTPRoutePathRedirect,HTTPRoutePathRewrite,HTTPRoutePortRedirect,HTTPRouteQueryParamMatching,HTTPRouteRequestMirror,HTTPRouteRequestMultipleMirrors,HTTPRouteRequestPercentageMirror,HTTPRouteRequestTimeout,HTTPRouteResponseHeaderModification,HTTPRouteSchemeRedirect,ReferenceGrant,TLSRoute,TLSRouteModeMixed,TLSRouteModeTerminate"
SKIPPED_TESTS=""

echo "Start Gateway API Conformance Testing"
go test ./conformance -v -timeout 60m -run TestConformance -args "--gateway-class=conformance" "--report-output=openshift.yaml" "--organization=Red Hat" "--project=Openshift Service Mesh" "--version=3.5.0" "--url=https://www.redhat.com/en/technologies/cloud-computing/openshift/container-platform" "--contact=https://github.com/openshift/cluster-ingress-operator" "--conformance-profiles=GATEWAY-HTTP,GATEWAY-GRPC,GATEWAY-TLS" "--supported-features=${SUPPORTED_FEATURES}" "--skip-tests=${SKIPPED_TESTS}"
cat conformance/openshift.yaml
