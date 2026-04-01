# AGENTS.md

This document provides guidance for AI coding agents working with the cluster-ingress-operator repository.

## 1. SYSTEM CONTEXT & TOPOLOGY
* **Domain:** North-South network traffic management (Layer 7 & Layer 4).
* **Prerequisites:** CNI (Cluster Network Operator), Pod IP routing, internal CoreDNS.
* **Dual Architecture Mode:**
  1. **Legacy/Primary:** HAProxy orchestration via `openshift3/ose-haproxy-router`.
  2. **Modern/GWAPI:** Gateway API via OpenShift Service Mesh (OSSM / Istio / Envoy).

## 2. STRICT NAMESPACE BOUNDARIES
* **`openshift-ingress-operator`**: Execution environment for operator logic, control loops, and metrics endpoints. **DO NOT** deploy data-plane operands here.
* **`openshift-ingress`**: Execution environment for all data-plane workloads. HAProxy pods, `haproxy.cfg` ConfigMaps, TLS secrets, Envoy proxies, and Services **MUST** be strictly confined to this namespace.

## 3. CUSTOM RESOURCE DEFINITION (CRD) MATRIX

### Core Configuration
* **`Ingress`** (`config.openshift.io/v1`, Cluster-scoped): Defines cluster-wide routing defaults and toggles.
* **`IngressController`** (`operator.openshift.io/v1`, Namespace-scoped): Primary interface for legacy HAProxy. Controls replicas, deployment affinity, and endpoint publishing.
* **`DNSRecord`** (`ingress.operator.openshift.io/v1`, Namespace-scoped): Internal abstraction for cloud DNS manipulation. 

### Gateway API (GWAPI)
* **`GatewayClass`**: Trigger resource. Detecting `openshift.io/gateway-controller/v1` initiates OSSM deployment.
* **`Gateway`**: Defines external proxy instances / ports. Mapped to Envoy proxies.
* **`HTTPRoute`**: Advanced routing, traffic weighting, and header matching to backend services.

## 4. DIRECTORY TOPOGRAPHY
* `pkg/operator/controller/ingress/`: Legacy core. Reconciles `IngressController` and manages HAProxy deployment, load balancer services, and status loops.
* `pkg/operator/controller/dns/`: Fulfills `DNSRecord` resources. Interfaces with AWS/GCP/Azure SDKs. 
* `pkg/operator/controller/gatewayapi/`: GWAPI meta-controller. Manages CRD lifecycles and launches dependent watchers.
* `pkg/operator/controller/gatewayclass/`: Provisions OSSM and generates the `ServiceMeshControlPlane`.
* `pkg/operator/controller/certificate/`: Manages automated certificate rotation and expiry.

## 5. TACTICAL DIRECTIVES & CONSTRAINTS
* **RULE 1: No Silent Failures.** If a cloud API times out or a version conflict occurs, set the expected error condition in `.status.conditions` (`expectedCondition` in `status.go`) and mark the operator as `Degraded`.
* **RULE 2: OSSM Meta-Management for GWAPI.** The operator does not write Envoy configs directly. It manages the OpenShift Service Mesh Operator by generating a `ServiceMeshControlPlane` (SMCP) resource.
* **RULE 3: Decouple DNS for GWAPI.** Directly generate `DNSRecord` objects for GWAPI endpoints; do not tie external endpoints for Envoy to the legacy HAProxy `IngressController`.
* **RULE 4: Finalizer Management.** Explicitly handle the removal of finalizers (e.g., `ingress.openshift.io/operator`) during deletion workflows in `load_balancer_service.go` to prevent infinite `Terminating` states.
* **RULE 5: Cloud Credential Operator (CCO) Awareness.** Gracefully degrade and report HTTP 403 Forbidden errors if assumed roles lack permissions in "manual mode" where dynamic IAM credentials are disabled.
* **RULE 6: Immutable HAProxy Template.** Do not inject arbitrary, unsupported HAProxy directives into the base router template.

## 6. EXPLICIT NON-GOALS
* Managing underlying cloud infrastructure subnets or legacy security groups.
* Fixing application-level protocol downgrade failures (e.g., HTTP/2 to WebSocket).
* Generating default TLS certificates for Envoy proxies.

## Project Structure and Repository Layout

```
cluster-ingress-operator/
├── cmd/ingress-operator/        # Main entry point
│   ├── main.go                  # CLI commands (start, render)
│   ├── render.go                # Manifest rendering command
│   └── start.go                 # Operator startup and metrics registration
├── pkg/
│   ├── operator/
│   │   ├── operator.go          # Controller registration
│   │   ├── controller/          # Reconciliation controllers (sub-packages per controller)
│   │   ├── client/              # Kubernetes client setup with custom schemes
│   │   └── config/              # Operator configuration structure
│   ├── dns/                     # DNS provider implementations
│   │   ├── aws/                 # AWS Route 53
│   │   ├── azure/               # Azure DNS
│   │   ├── gcp/                 # Google Cloud DNS
│   │   ├── ibm/                 # IBM Cloud DNS (public and private)
│   │   └── split/               # Meta-provider routing between public/private
│   └── manifests/               # Kubernetes object manifests used by controllers
├── manifests/                   # CVO manifests (CRDs, RBAC, monitoring) — instantiated by CVO, not used by operator directly
├── test/
│   └── e2e/                     # End-to-end integration tests
├── hack/                        # Development and CI scripts
├── Makefile                     # Build automation
└── HACKING.md                   # Developer documentation
```

`pkg/manifests/` contains asset loading utilities (`manifests.go`) that bind Go templates to Kubernetes objects for controller use.

## Feature Development

### Adding a New Controller

Controllers follow this pattern:

1. Create a package in `pkg/operator/controller/<name>/`
2. Define a `reconciler` struct (for example — not all controllers need all fields; customize to required fields):
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
4. Implement `Reconcile()` method with idempotent `ensure*()` functions. Controllers delegate logic to `ensure<Resource>` methods that handle creation/update of specific resources (e.g., `ensureIngressController`, `ensureIngressDeleted`).
5. Register the controller in `pkg/operator/operator.go`. Metrics (if any) are registered in `cmd/ingress-operator/start.go`.

See `pkg/operator/controller/ingress/controller_test.go` as a reference for controller test patterns.

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
| `crl` | Certificate Revocation List management (deprecated since 4.14, pending removal — NE-2491) |
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
| `(fake)` | No-op provider for testing (defined in `pkg/dns/dns.go`) |

DNS providers implement the `dns.Provider` interface:
- `Ensure(record, zone)` - Create or update DNS record
- `Delete(record, zone)` - Remove DNS record
- `Replace(record, zone)` - Replace existing record

## Building

```bash
make build          # Build the operator binary (depends on generate)
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
- `github.com/stretchr/testify/assert` for assertions (e.g., `pkg/dns/aws/dns_test.go`)
- `google/go-cmp` for deep comparisons

### Test Patterns

- **Table-driven tests**: Use for testing multiple scenarios
- **Subtests**: Use `t.Run()` with descriptive names for nested tests
- **Test naming conventions**:
  - `Test_foo` — general test for function `foo`
  - `TestFooFunctionality` — test for specific functionality in `foo`

### Test Organization

- **Unit tests**: Alongside source files as `*_test.go`
- **E2E tests**: In `test/e2e/` with build tag `// +build e2e`
- **Parallel tests** (~90): Run concurrently, independent of each other
- **Serial tests** (~50): Run sequentially, modify cluster-wide resources

### Assertions

```go
// Use testify/assert for assertions
assert.NoError(t, err, "failed to create resource")

// Use google/go-cmp for deep comparisons
if diff := cmp.Diff(expected, actual); diff != "" {
    t.Errorf("mismatch (-want +got):\n%s", diff)
}
```

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
- `hack/verify-gofmt.sh` - gofmt
- `hack/verify-generated-crd.sh` - Verifies CRDs under `manifests/` match CRDs under `vendor/`
- `hack/verify-profile-manifests.sh` - Verifies profile-specific manifests (e.g., `02-deployment.yaml` for `ibm-cloud-managed`) are up to date
- `hack/verify-deps.sh` - Verifies `go mod` vendoring is up to date (`go mod vendor` / `go mod tidy`)

## Additional Makefile Targets

| Target | Description |
|--------|-------------|
| `make generate` | Update embedded manifests (operator namespace, ingresscontrollers CRD) used by `ingress-operator render` |
| `make crd` | Generate CRD YAML files |
| `make release-local` | Build image and deployment manifests |
| `make uninstall` | Remove operator from cluster |
| `make buildconfig` | Create OpenShift BuildConfig for remote builds |
| `make cluster-build` | Trigger remote cluster build |
| `make clean` | Remove binaries and generated files |

## Dependencies

Dependencies are vendored. After modifying `go.mod`:

```bash
go mod tidy
go mod vendor
```

Dependencies:
- `sigs.k8s.io/controller-runtime`
- `k8s.io/client-go`
- `sigs.k8s.io/gateway-api`
- `github.com/openshift/api`

## Coding Style

### Go Version

Go version is specified in `go.mod`.

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
| `CRLConfigMapName(ic)` | `router-client-ca-crl-<name>` (deprecated — see crl controller) |

### Important Annotations and Labels

Defined as constants in `pkg/operator/controller/names.go`:

| Constant | Value | Purpose |
|----------|-------|---------|
| `IngressOperatorOwnedAnnotation` | `ingress.operator.openshift.io/owned` | Marks a resource as owned by the ingress operator (used on subscriptions) |
| `ControllerDeploymentLabel` | `ingresscontroller.operator.openshift.io/deployment-ingresscontroller` | Identifies a deployment as an ingress controller; value is the IC name |
| `ControllerDeploymentHashLabel` | `ingresscontroller.operator.openshift.io/hash` | Identifies an ingress controller deployment's generation (used for affinity/anti-affinity) |
| `CanaryDaemonSetLabel` | `ingresscanary.operator.openshift.io/daemonset-ingresscanary` | Identifies a daemonset as an ingress canary daemonset; value is the owning canary controller name |

### Feature Gates

Controllers check these feature gates (from `github.com/openshift/api/features`):
- `features.FeatureGateGatewayAPI` — Gateway API support
- `features.FeatureGateGatewayAPIController` — Gateway API controller
- `features.FeatureGateAzureWorkloadIdentity` — Azure workload identity
- `features.FeatureGateIngressControllerDynamicConfigurationManager` — Dynamic configuration management
- `features.FeatureGateRouteExternalCertificate` — External route certificates (being removed)

### Error Handling

- Return errors with context: `fmt.Errorf("failed to create deployment: %w", err)`
- Use `%w` for wrapped error values to allow `errors.Is`/`errors.As` unwrapping
- Aggregate errors when multiple operations can fail independently
- Use `k8s.io/apimachinery/pkg/util/errors` for error aggregation

### Logging

- Use structured logging via `go-logr/logr`
- Include relevant context (namespace, name, resource type)
- Follow [Kubernetes logging conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines)

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
| `Dockerfile.rhel7` | RHEL 7 variant (outdated, may be removed) |
| `Dockerfile.ubi` | UBI (Universal Base Image) variant |

### Deployment

- Deployed as part of OpenShift installation
- Runs in `openshift-ingress-operator` namespace
- Managed by Cluster Version Operator (CVO)

## Contribution Conventions

- Commit messages should reference the Jira ticket: `NE-XXXX: description`
- PRs should have logical, atomic commits
- Test coverage is expected for new features and bug fixes
