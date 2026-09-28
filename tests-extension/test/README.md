# cluster-ingress-operator OTE Test Extension — Gateway API & LB Security Groups Tests

All 10 test cases cover Gateway API CRD lifecycle and AWS NLB security group management, split across two test files.

## Test Files

| File | Tests | Description |
|------|-------|-------------|
| `gatewayapi.go` | 5 | Gateway API CRD lifecycle tests (installation, deletion protection, update protection) |
| `lb_security_groups.go` | 5 | AWS NLB security group management tests (managed, unmanaged, reconciliation) |

## Test Suites

### gateway-api/parallel
All 10 tests (non-Serial, non-Disruptive, non-Slow, excludes IngressOperator):
- Gateway API CRD tests (5) — read-only/DryRun, fully independent
- LB Security Groups tests (5, AWS-only, feature-gated) — each test creates its own isolated IC

The 4 managed LB Security Groups tests use a `g.Ordered` Ginkgo container. Each test's `BeforeAll` creates its own IngressController, so tests work independently when OTE re-execs them as separate processes (via `run-test` or `run-suite`). No `[Serial]` tag is needed.

### gateway-api/serial
Serial or Disruptive tests excluding IngressOperator (none currently)

### gateway-api/slow
Slow tests excluding IngressOperator (none currently)

## How to Run

```bash
# Run these commands from the repository root
cd tests-extension

export KUBECONFIG=/path/to/kubeconfig

# Build the binary
make build

# List all available suites
./bin/cluster-ingress-operator-tests-ext list suites

# List all tests
./bin/cluster-ingress-operator-tests-ext list tests
```

### Run All Gateway API & LB Security Groups Tests

```bash
# Run all 10 tests via suite
./bin/cluster-ingress-operator-tests-ext run-suite gateway-api/parallel
```

### Run by Test File

```bash
# Run only Gateway API CRD tests (parallel-safe, no ordering constraints)
./bin/cluster-ingress-operator-tests-ext list -o names | grep GatewayAPI | ./bin/cluster-ingress-operator-tests-ext run-test

# Run only LB Security Groups tests (AWS-only, skipped on non-AWS)
# Each test runs in its own process with its own BeforeAll, so they work independently
./bin/cluster-ingress-operator-tests-ext list -o names | grep IngressControllerLBSecurityGroupsAWS | ./bin/cluster-ingress-operator-tests-ext run-test
```

### Run Only gatewayapi.go Tests

```bash
# Run all 5 Gateway API CRD tests by name filter (parallel-safe)
./bin/cluster-ingress-operator-tests-ext run-test \
  -n "[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure required CRDs should already be installed" \
  -n "[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure existing CRDs can not be deleted" \
  -n "[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure existing CRDs can not be updated" \
  -n "[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure CRD of standard group can not be created" \
  -n "[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure CRD of experimental group is not installed"
```

### Run Only lb_security_groups.go Tests

```bash
# Run all 5 LB Security Groups tests by name filter
# OTE re-execs each test as a separate process; each gets its own BeforeAll and IC
./bin/cluster-ingress-operator-tests-ext run-test \
  -n "[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups managed through the IngressController spec provisions a LoadBalancer service with the specified security group" \
  -n "[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups managed through the IngressController spec effectuates an update to the security groups" \
  -n "[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups managed through the IngressController spec removes the security groups using the auto-delete-load-balancer annotation" \
  -n "[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups managed through the IngressController spec reflects the effective security groups in the IngressController status" \
  -n "[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups set directly on the LoadBalancer service (unmanaged) preserves a security groups annotation and reconciles once the spec matches"
```

### Run Only the Unmanaged LB Security Groups Test

```bash
# This test is self-contained (has its own BeforeEach) and can run independently
./bin/cluster-ingress-operator-tests-ext run-test "[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups set directly on the LoadBalancer service (unmanaged) preserves a security groups annotation and reconciles once the spec matches"
```

### Run a Single Test

```bash
# Run a single test by its full name
./bin/cluster-ingress-operator-tests-ext run-test "[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure required CRDs should already be installed"
```

### Running All Tests (Gateway API + Ingress Operator + LB Security Groups)

```bash
# Run all non-slow tests (parallel + serial combined)
./bin/cluster-ingress-operator-tests-ext run-suite all --max-concurrency=1

# Run all parallel tests (Gateway API + Ingress Operator + LB Security Groups)
./bin/cluster-ingress-operator-tests-ext run-suite parallel

# Run all serial/disruptive tests (Gateway API + Ingress Operator + LB Security Groups)
./bin/cluster-ingress-operator-tests-ext run-suite serial --max-concurrency=1
```

**Important**: The 4 managed LB Security Groups tests use a `g.Ordered` Ginkgo container. OTE re-execs each matched spec as a separate child process (see `pkg/ginkgo/parallel.go`), so each test runs its own `BeforeAll` and creates an independent IngressController. The tests are designed to work independently — each test waits for its LB to be provisioned before operating on it. Running all 5 tests concurrently creates 5 separate ICs and security groups (more resources than a single-process run), but each test is self-contained.

The Gateway API CRD tests are fully parallel-safe (all mutations use DryRun). The unmanaged LB Security Groups test creates its own IC via `BeforeEach` and is also parallel-safe.

The LB Security Groups tests carry the `[OCPFeatureGate:IngressControllerLBSecurityGroupsAWS]` tag and are automatically skipped on non-AWS platforms.

The Gateway API CRD tests carry the `[OCPFeatureGate:GatewayAPI]` tag and only run when the feature gate is enabled.

## Test Execution Time

- Single test: 1-5 minutes
- Gateway API suite: 2-5 minutes
- LB Security Groups suite: 10-15 minutes (NLB provisioning)
- All 10 tests: 15-20 minutes

## Files

```text
tests-extension/
├── cmd/main.go                             # OTE entry point, suite definitions
├── test/
│   ├── util.go                             # Shared helper functions (package test)
│   ├── aws_util.go                         # AWS EC2 helper functions
│   ├── gatewayapi.go                       # 5 Gateway API CRD test implementations
│   ├── lb_security_groups.go               # 5 LB security groups test implementations
│   ├── README.md                           # This file
│   └── qe/
│       ├── ingress_operator.go             # 14 ingress operator test implementations
│       └── README.md                       # Ingress operator test documentation
├── Makefile                                # Build targets
├── go.mod                                  # Dependencies
├── go.sum                                  # Dependency checksums
└── vendor/                                 # Vendored dependencies
```

## All Test Names

### gatewayapi.go (5 tests)

```text
[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure required CRDs should already be installed
[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure existing CRDs can not be deleted
[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure existing CRDs can not be updated
[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure CRD of standard group can not be created
[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure CRD of experimental group is not installed
```

### lb_security_groups.go (5 tests)

#### Managed (g.Ordered — each test creates its own IC via BeforeAll)

```text
[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups managed through the IngressController spec provisions a LoadBalancer service with the specified security group
[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups managed through the IngressController spec effectuates an update to the security groups
[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups managed through the IngressController spec removes the security groups using the auto-delete-load-balancer annotation
[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups managed through the IngressController spec reflects the effective security groups in the IngressController status
```

#### Unmanaged (self-contained, parallel-safe)

```text
[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups set directly on the LoadBalancer service (unmanaged) preserves a security groups annotation and reconciles once the spec matches
```
