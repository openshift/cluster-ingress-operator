# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

The cluster-ingress-operator is an OpenShift component that manages ingress controllers (HAProxy-based routers) for external cluster access. It reconciles `IngressController` custom resources to deploy and configure router pods, manage DNS records, provision load balancers, and handle TLS certificates.

**Core Responsibilities**:
- Deploy and manage HAProxy-based ingress controllers
- Provision cloud load balancers (AWS ELB/NLB, Azure LB, GCP LB)
- Manage DNS records via cloud provider APIs
- Configure endpoint publishing strategies (LoadBalancerService, NodePortService, HostNetwork, Private)
- Handle TLS certificates and certificate rotation
- Support OpenShift Routes and Kubernetes Ingress resources
- Gateway API integration (experimental)

## Build and Test Commands

### Building

```bash
make build                    # Build ingress-operator binary
make generate                 # Generate manifests (runs pkg/manifests generation)
```

### Testing

```bash
# Unit tests
make test                     # Run all unit tests
make TEST=TestFoo test        # Run specific unit test

# E2E tests (requires KUBECONFIG pointing to OpenShift cluster)
make test-e2e                            # Run all e2e tests via TestAll
make TEST_E2E=TestClientTLS test-e2e     # Run specific e2e test
make test-e2e-list                       # List all available e2e tests

# Gateway API conformance
make gatewayapi-conformance   # Run Gateway API conformance suite
```

**Important**: E2E tests in `test/e2e/all_test.go` are organized into parallel and serial test groups. The `TestAll` function is the entrypoint that orchestrates execution to minimize test time while avoiding conflicts from concurrent ingresscontroller modifications.

### Code Verification

```bash
make verify                   # Run all verification checks
# Includes: gofmt, generated CRD validation, profile manifests, deps, e2e test presence
```

Auto-fix formatting: `hack/verify-gofmt.sh | xargs -n 1 gofmt -s -w`

### Update Generated Code

```bash
make update                   # Alias for 'make crd'
make crd                      # Regenerate CRDs and profile manifests
# Runs: hack/update-generated-crd.sh and hack/update-profile-manifests.sh
```

## Development Workflow

### Local Development (Recommended)

Run the operator locally without deploying to cluster:

```bash
make run-local                # Build and run operator from local machine
# Set ENABLE_CANARY=true to enable ingress canary during local runs
```

This scales down the cluster's operator and runs your local binary against the cluster. To restore cluster operator:

```bash
oc scale --replicas 1 -n openshift-cluster-version deployments/cluster-version-operator
```

### Deploying to Cluster

Build custom image and deploy to cluster:

```bash
# 1. Uninstall existing operator (scales CVO to 0)
make uninstall

# 2. Build and push custom image with custom manifests
REPO=docker.io/you/cluster-ingress-operator make release-local
# Or with UBI Dockerfile:
DOCKERFILE=Dockerfile.ubi REPO=docker.io/you/cluster-ingress-operator make release-local

# 3. Apply generated manifests
oc apply -f /tmp/manifests/<path-from-output>

# 4. When done, restore CVO
oc scale --replicas 1 -n openshift-cluster-version deployments/cluster-version-operator
```

### Remote Cluster Build

For environments where local builds are difficult:

```bash
# Create buildconfig (one-time or when updating branch/remote)
make buildconfig
# Override defaults:
make buildconfig GIT_BRANCH=feature-x GIT_URL=https://github.com/user/cluster-ingress-operator.git

# Start build
make cluster-build              # Silent build
make cluster-build V=1          # Show logs during build
make cluster-build DEPLOY=1     # Build and patch operator to use new image
```

## Architecture

### Controller Pattern

The operator uses controller-runtime with multiple reconciliation controllers in `pkg/operator/controller/`:

- **ingress**: Main controller - reconciles `IngressController` resources to manage router deployments, services, DNS records
- **dns**: Manages `DNSRecord` resources by calling cloud provider DNS APIs
- **certificate**: Generates and rotates default ingress certificates
- **certificate-publisher**: Publishes router certs to openshift-config-managed namespace
- **canary**: Monitors ingress health via canary route checks
- **status**: Updates ClusterOperator status based on ingress controller states
- **gatewayapi**: Manages Gateway API resources (experimental)
- **ingressclass**: Manages IngressClass resources
- **route-metrics**: Collects metrics from routes

### DNS Provider Abstraction

`pkg/dns/dns.go` defines the `Provider` interface with `Ensure()`, `Delete()`, and `Replace()` methods. Cloud-specific implementations:

- `pkg/dns/aws/` - Route53 integration
- `pkg/dns/azure/` - Azure DNS zones (public and private)
- `pkg/dns/gcp/` - Google Cloud DNS
- `pkg/dns/ibm/` - IBM Cloud DNS (CIS for public, DNS Services for private)

### Load Balancer Service Management

`pkg/operator/controller/ingress/load_balancer_service.go` handles cloud load balancer provisioning via Kubernetes Service annotations:

- AWS: ELB (Classic), NLB (Network) with proxy protocol, subnets, EIP allocation support
- Azure: Azure Load Balancer with internal/external variants
- GCP: GCP Load Balancer with global access options

Key AWS annotations: `service.beta.kubernetes.io/aws-load-balancer-type`, `service.beta.kubernetes.io/aws-load-balancer-proxy-protocol`, etc.

### Manifest Generation

`pkg/manifests/manifests.go` uses `go:generate` directives to embed YAML manifests from `assets/` into the binary. These templates are used to create Kubernetes resources. Run `make generate` or `go generate ./pkg/manifests` after modifying assets.

## Key Concepts

### IngressController Resource

Defined in `github.com/openshift/api/operator/v1` (vendored dependency). The operator watches for changes and reconciles:
- `.spec.replicas` - Router pod count
- `.spec.domain` - Ingress subdomain (immutable after creation)
- `.spec.endpointPublishingStrategy` - How to expose the router (immutable after creation)
- `.spec.nodePlacement` - Node selector, tolerations for router pods
- `.spec.routeSelector`, `.spec.namespaceSelector` - Route filtering
- `.spec.tlsSecurityProfile` - TLS version and cipher configuration

**Critical**: Domain and endpoint publishing strategy cannot be changed after creation.

### E2E Test Organization

Tests in `test/e2e/` use build tag `//go:build e2e`. The `TestAll` function in `all_test.go` orchestrates parallel and serial test execution:
- **Parallel tests**: Run concurrently for speed (most tests)
- **Serial tests**: Run sequentially to avoid conflicts (e.g., tests modifying cluster-wide config)

When adding new e2e tests:
1. Add test function to appropriate file in `test/e2e/`
2. Add to `TestAll` in `all_test.go` (parallel or serial group)
3. Test must call `t.Parallel()` if added to parallel group
4. Verify with `make verify` which checks all tests are in `TestAll`

### Running Single E2E Test

```bash
# Run specific test directly (bypasses TestAll grouping)
make TEST_E2E=TestClientTLS test-e2e

# Or using TEST variable (applies to both unit and e2e)
make TEST=TestClientTLS test-e2e
```

## Dependencies

- **Controller Runtime**: `sigs.k8s.io/controller-runtime` for operator pattern
- **OpenShift APIs**: `github.com/openshift/api` for IngressController, Route, DNSRecord types
- **OpenShift Client**: `github.com/openshift/client-go` for OpenShift API clients
- **Cloud SDKs**: AWS SDK, Azure SDK, GCP SDK, IBM Cloud SDK for DNS and load balancer management
- **HAProxy Router**: Manages `github.com/openshift/router` deployment (router image, not in this repo)

## Working with Gateway API

Gateway API support is experimental and requires Service Mesh operator:

```bash
make gatewayapi-conformance   # Runs conformance suite
```

Gateway API controllers in `pkg/operator/controller/gatewayapi/`, `pkg/operator/controller/gatewayclass/`, etc. Integration with Istio-based Service Mesh.

## Common Debugging

Inspect operator status:
```bash
oc describe clusteroperators/ingress
oc logs -n openshift-ingress-operator deployments/ingress-operator
```

Inspect specific ingress controller:
```bash
oc describe -n openshift-ingress-operator ingresscontroller/<name>
oc get -n openshift-ingress deployment/router-<name>
```

Check DNS records:
```bash
oc get dnsrecords -n openshift-ingress-operator
oc describe dnsrecord -n openshift-ingress-operator <name>
```
