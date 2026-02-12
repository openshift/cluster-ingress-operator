# AGENTS.md

This document provides guidance for AI coding agents working with the cluster-ingress-operator repository.

## Project Structure and Repository Layout

```
cluster-ingress-operator/
├── cmd/ingress-operator/        # Main entry point
│   ├── main.go                  # CLI commands (start, render)
│   └── start.go                 # Operator initialization and controller registration
├── pkg/
│   ├── operator/
│   │   ├── controller/          # Reconciliation controllers (17 controllers)
│   │   ├── client/              # Kubernetes client setup with custom schemes
│   │   └── config/              # Operator configuration structures
│   ├── dns/                     # DNS provider implementations
│   │   ├── aws/                 # AWS Route 53
│   │   ├── azure/               # Azure DNS
│   │   ├── gcp/                 # Google Cloud DNS
│   │   └── ibm/                 # IBM Cloud DNS (public and private)
│   └── manifests/               # Kubernetes manifest generation
├── manifests/                   # Static Kubernetes YAML (CRDs, RBAC, monitoring)
├── test/
│   └── e2e/                     # End-to-end integration tests
├── hack/                        # Development and CI scripts
├── Makefile                     # Build automation
└── HACKING.md                   # Developer documentation
```

## Feature Development

### Adding a New Controller

Controllers follow this pattern:

1. Create a package in `pkg/operator/controller/<name>/`
2. Define a `reconciler` struct:
   ```go
   type reconciler struct {
       client            client.Client
       recorder          record.EventRecorder
       cache             cache.Cache
       operatorNamespace string
       operandNamespace  string
   }
   ```
3. Implement `New()` factory function to create controller and register watches
4. Implement `Reconcile()` method with idempotent `ensure*()` functions
5. Register the controller in `cmd/ingress-operator/start.go`

### Existing Controllers

Located in `pkg/operator/controller/`:

| Controller | Purpose |
|------------|---------|
| `ingress` | Main controller for IngressController resources |
| `canary` | Health check canary for ingress controllers |
| `certificate` | TLS certificate management |
| `certificate-publisher` | Publishes router certs to openshift-config-managed |
| `clientca-configmap` | Syncs client CA configmaps between namespaces |
| `configurable-route` | Manages custom route configuration |
| `crl` | Certificate Revocation List management |
| `dns` | DNS record management |
| `gatewayapi` | Gateway API CRD management |
| `gatewayclass` | Istio/OSSM installation for Gateway API |
| `gateway-labeler` | Labels Gateway resources |
| `gateway-service-dns` | DNS for Gateway services |
| `ingressclass` | IngressClass resource management |
| `monitoring-dashboard` | Monitoring dashboard creation |
| `route-metrics` | Route metrics collection |
| `status` | ClusterOperator status management |
| `sync-http-error-code-configmap` | HTTP error code page sync |

### DNS Providers

Located in `pkg/dns/`:

| Provider | Description |
|----------|-------------|
| `aws` | AWS Route 53 DNS |
| `azure` | Azure DNS (with workload identity support) |
| `gcp` | Google Cloud DNS |
| `ibm` | IBM Cloud DNS (public CIS and private DNS Services) |
| `split` | Meta-provider routing between public/private providers |

DNS providers implement the `dns.Provider` interface:
- `Ensure(record, zone)` - Create or update DNS record
- `Delete(record, zone)` - Remove DNS record
- `Replace(record, zone)` - Replace existing record

## Building

```bash
make build          # Build the operator binary
make all            # Run generate and build (default target)
```

- Uses vendored dependencies (`-mod=vendor`)
- Requires `CGO_ENABLED=1`

## Running

### Prerequisites

- An OpenShift cluster
- Admin-scoped `KUBECONFIG`

### Local Execution

```bash
make run-local                      # Run operator locally
ENABLE_CANARY=true make run-local   # With canary enabled
```

### Remote Deployment

See [HACKING.md](HACKING.md) for:
- Building and deploying to cluster (`make release-local`)
- Remote builds on cluster (`make buildconfig`, `make cluster-build`)

## Tests

### Running Tests

```bash
make test                            # Run unit tests
make test-e2e                        # Run all e2e tests
make test-e2e TEST="^TestRouter$"    # Run specific test
make test-e2e-list                   # List available tests
make gatewayapi-conformance          # Gateway API conformance tests
```

### Test Framework

- Standard Go testing package
- `testify` for assertions (`assert`, `require`)
- `google/go-cmp` for deep comparisons

### Test Patterns

- **Behavior-based testing**: Focus on public API behavior, not implementation details
- **Table-driven tests**: Use for testing multiple scenarios
- **Subtests**: Use `t.Run()` with descriptive names for nested tests

### Test Organization

- **Unit tests**: Alongside source files as `*_test.go`
- **E2E tests**: In `test/e2e/` with build tag `// +build e2e`
- **Parallel tests** (~90): Run concurrently, independent of each other
- **Serial tests** (~50): Run sequentially, modify cluster-wide resources

### Assertions

```go
// Use testify/require for fatal assertions
require.NoError(t, err, "failed to create resource")

// Use google/go-cmp for deep comparisons
if diff := cmp.Diff(expected, actual); diff != "" {
    t.Errorf("mismatch (-want +got):\n%s", diff)
}
```

### Coverage

- Test both success and error paths
- Cover edge cases: empty inputs, nil values, boundary conditions
- Verify error messages contain useful context

### Error Handling in Tests

- Use `t.Fatalf()` for errors that should stop the test
- Use `t.Errorf()` for non-fatal errors allowing test to continue
- Always include context in error messages

### Test Helpers

**E2E Utilities** (`test/e2e/util_test.go`):

| Helper | Purpose |
|--------|---------|
| `buildEchoPod()` | Creates socat-based echo server pod |
| `buildCurlPod()` | Creates curl pod for HTTP testing |
| `waitForHTTPClientCondition()` | Polls HTTP endpoint with retry |

**Operator Test Helpers** (`test/e2e/operator_test.go`):

| Helper | Purpose |
|--------|---------|
| `waitForIngressControllerCondition()` | Poll for expected conditions |
| `waitForDeploymentComplete()` | Wait for deployment rollout |
| `waitForAvailableReplicas()` | Wait for replica count |
| `waitForClusterOperatorConditions()` | Poll ClusterOperator status |
| `deleteIngressController()` | Clean up with timeout |

## Linting

```bash
make verify
```

Runs verification scripts:
- `hack/verify-gofmt.sh` - Go formatting
- `hack/verify-generated-crd.sh` - CRD generation
- `hack/verify-profile-manifests.sh` - Profile manifests
- `hack/verify-deps.sh` - Dependencies

## Additional Makefile Targets

| Target | Description |
|--------|-------------|
| `make generate` | Generate manifests from API specs |
| `make crd` | Generate CRD YAML files |
| `make release-local` | Build image and deployment manifests |
| `make uninstall` | Remove operator from cluster |
| `make buildconfig` | Create OpenShift BuildConfig for remote builds |
| `make cluster-build` | Trigger remote cluster build |
| `make clean` | Remove binaries and generated files |

## Dependencies

Dependencies are vendored. After modifying `go.mod`:

```bash
go mod vendor
```

Key dependencies:
- `sigs.k8s.io/controller-runtime` v0.21.0
- `k8s.io/client-go` v0.34.1
- `sigs.k8s.io/gateway-api` v1.3.0
- `github.com/openshift/api`

## Coding Style

### Go Version

Go 1.24

### Code Organization

- Controllers in `pkg/operator/controller/<name>/`
- Each controller has `controller.go`, functional files (e.g., `deployment.go`), and corresponding `*_test.go` files

### Namespace Constants

Defined in `pkg/operator/controller/`:

```go
DefaultOperatorNamespace              = "openshift-ingress-operator"
DefaultOperandNamespace               = "openshift-ingress"
DefaultCanaryNamespace                = "openshift-ingress-canary"
GlobalMachineSpecifiedConfigNamespace = "openshift-config-managed"
GlobalUserSpecifiedConfigNamespace    = "openshift-config"
```

### Naming Functions

Defined in `pkg/operator/controller/names.go`:

| Function | Returns |
|----------|---------|
| `RouterDeploymentName(ic)` | `router-<name>` in openshift-ingress |
| `LoadBalancerServiceName(ic)` | `router-<name>` service |
| `NodePortServiceName(ic)` | `router-nodeport-<name>` service |
| `IngressClassName(name)` | `openshift-<name>` IngressClass |
| `CanaryDaemonSetName()` | Canary daemonset name |
| `ClientCAConfigMapName(ic)` | `router-client-ca-<name>` |
| `CRLConfigMapName(ic)` | `router-client-ca-crl-<name>` |

### Important Annotations

```go
IngressOperatorOwnedAnnotation = "ingress.operator.openshift.io/owned"
ControllerDeploymentLabel      = "ingresscontroller.operator.openshift.io/deployment-ingresscontroller"
ControllerDeploymentHashLabel  = "ingresscontroller.operator.openshift.io/hash"
CanaryDaemonSetLabel           = "ingresscanary.operator.openshift.io/daemonset-ingresscanary"
```

### Feature Gates

Controllers check these feature gates:
- `FeatureGateGatewayAPI` - Gateway API support
- `FeatureGateAzureWorkloadIdentity` - Azure workload identity
- `FeatureGateRouteExternalCertificate` - External route certificates

### Error Handling

- Return errors with context: `fmt.Errorf("failed to create deployment: %w", err)`
- Aggregate errors when multiple operations can fail independently
- Use `k8s.io/apimachinery/pkg/util/errors` for error aggregation

### Logging

- Use structured logging via `go-logr/logr`
- Include relevant context (namespace, name, resource type)

### Formatting

Go formatting is enforced:

```bash
hack/verify-gofmt.sh
```

## Distribution Methods

### Container Images

| Dockerfile | Description |
|------------|-------------|
| `Dockerfile` | Default build |
| `Dockerfile.rhel7` | RHEL 7 variant |
| `Dockerfile.ubi` | UBI (Universal Base Image) variant |

### Deployment

- Deployed as part of OpenShift installation
- Runs in `openshift-ingress-operator` namespace
- Managed by Cluster Version Operator (CVO)

### Local Development

```bash
# Build and push image, generate manifests
make release-local REPO=docker.io/you/cluster-ingress-operator

# Run operator locally (not in cluster pod)
make run-local
```
