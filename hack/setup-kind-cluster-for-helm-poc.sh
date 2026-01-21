#!/bin/bash
set -euo pipefail

# This script sets up a kind cluster for testing the Gateway API Helm installation POC.
# It creates a kind cluster, installs Gateway API CRDs from the local repo, and prepares
# the environment for running the isolated POC gatewayclass controller.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Configuration
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-gwapi-helm-poc}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

info() {
    echo -e "${GREEN}==>${NC} $*"
}

warn() {
    echo -e "${YELLOW}Warning:${NC} $*"
}

error() {
    echo -e "${RED}Error:${NC} $*" >&2
}

check_requirements() {
    info "Checking required tools..."

    local missing_tools=()

    if ! command -v kind &> /dev/null; then
        missing_tools+=("kind")
    fi

    if ! command -v kubectl &> /dev/null; then
        missing_tools+=("kubectl")
    fi

    if ! command -v go &> /dev/null; then
        missing_tools+=("go")
    fi

    if [ ${#missing_tools[@]} -ne 0 ]; then
        error "Missing required tools: ${missing_tools[*]}"
        exit 1
    fi

    info "All required tools found"
}

create_cluster() {
    info "Checking if cluster '${KIND_CLUSTER_NAME}' already exists..."

    if kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER_NAME}$"; then
        warn "Cluster '${KIND_CLUSTER_NAME}' already exists"
        read -p "Delete and recreate? [y/N] " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            info "Deleting existing cluster..."
            kind delete cluster --name="${KIND_CLUSTER_NAME}"
        else
            info "Using existing cluster"
            return 0
        fi
    fi

    info "Creating kind cluster '${KIND_CLUSTER_NAME}'..."
    kind create cluster --name="${KIND_CLUSTER_NAME}"

    info "Waiting for cluster to be ready..."
    kubectl wait --for=condition=Ready nodes --all --timeout=120s

    info "Cluster created successfully"
}

install_gateway_api_crds() {
    info "Installing Gateway API CRDs from vendored assets..."

    local crd_dir="${REPO_ROOT}/pkg/manifests/assets/gateway-api"

    if [ ! -d "${crd_dir}" ]; then
        error "Gateway API CRD directory not found: ${crd_dir}"
        exit 1
    fi

    # Install the CRDs
    kubectl apply -f "${crd_dir}"

    info "Gateway API CRDs installed successfully"
}

create_namespaces() {
    info "Creating required namespaces..."

    kubectl create namespace openshift-ingress-operator --dry-run=client -o yaml | kubectl apply -f -
    kubectl create namespace openshift-ingress --dry-run=client -o yaml | kubectl apply -f -

    info "Namespaces created"
}

print_next_steps() {
    echo ""
    echo "=========================================="
    echo -e "${GREEN}Kind cluster setup complete!${NC}"
    echo "=========================================="
    echo ""
    echo "Cluster name: ${KIND_CLUSTER_NAME}"
    echo "Context: kind-${KIND_CLUSTER_NAME}"
    echo ""
    echo "Next steps:"
    echo ""
    echo "1. Run the Gateway API Helm POC with the start-gatewayclass command:"
    echo ""
    echo -e "   ${YELLOW}./ingress-operator start-gatewayclass${NC}"
    echo ""
    echo "2. In another terminal, create a GatewayClass to trigger Helm installation:"
    echo ""
    echo "kubectl apply -f - <<EOF"
    echo "apiVersion: gateway.networking.k8s.io/v1"
    echo "kind: GatewayClass"
    echo "metadata:"
    echo "  name: openshift-default"
    echo "spec:"
    echo "  controllerName: openshift.io/gateway-controller/v1"
    echo "EOF"
    echo ""
    echo "3. Verify the Helm release was created:"
    echo "   helm list -A"
    echo ""
    echo "4. Check istiod deployment:"
    echo "   kubectl get pods -n openshift-ingress"
    echo ""
    echo "To delete the cluster when done:"
    echo "   kind delete cluster --name=${KIND_CLUSTER_NAME}"
}

# Main execution
main() {
    info "Setting up kind cluster for Gateway API Helm installation POC"
    echo ""

    check_requirements
    create_cluster

    install_gateway_api_crds
    create_namespaces

    print_next_steps
}

main "$@"
