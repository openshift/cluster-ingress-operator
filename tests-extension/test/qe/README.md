# cluster-ingress-operator OTE Test Extension — Ingress Operator QE Tests

All 14 ingress operator test cases have been migrated from openshift-tests-private to the OTE (openshift-tests-extension) framework.

## Test Files

| File | Tests | Description |
|------|-------|-------------|
| `ingress_operator.go` | 14 | IngressController lifecycle, router deployment, routing, and certificate tests |

## Test Suites

### ingress-operator/all
All 14 tests (use `--max-concurrency=1` since some are disruptive)

### ingress-operator/parallel
9 non-serial, non-disruptive tests (safe to run in parallel):
- 26150 - misc tests for ingress operator
- 22633 - The nodeSelector and tolerations of router deployment are controlled by ingresscontroller
- 22636 - The namespaceSelector of router is controlled by ingresscontroller
- 22637 - The routeSelector of router is controlled by ingresscontroller
- 60012 - matchExpressions for routeSelector defined in an ingress-controller
- 60013 - matchExpressions for namespaceSelector defined in an ingress-controller
- 63832 - Cluster ingress health checks and routes fail on swapping application router between public and private (AWS-only)
- 64611 - Ingress operator support for private hosted zones in Shared VPC clusters (AWS-only)
- 77283 - Router should support SHA1 CA certificates in the default certificate chain

### ingress-operator/serial
5 serial or disruptive tests (must run sequentially):
- 56772 - Ingress Controller does not set allowPrivilegeEscalation in the router deployment [Serial]
- 62530 - openshift ingress operator is failing to update router-certs [Serial]
- 75907 - Ingress Operator should not always remain in the progressing state [Disruptive]
- 75908 - http2 connection coalescing component routing should not be broken with single certificate [Disruptive]
- 75909 - Ingress Operator should not always remain in the progressing state [Disruptive]

### ingress-operator/slow
[Slow]-tagged tests (none currently)

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

### Run All Tests

```bash
# Run all 14 ingress operator tests (serially recommended)
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/all --max-concurrency=1
```

### Run by Suite

```bash
# Run parallel-safe tests (default concurrency is fine)
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/parallel

# Run only serial/disruptive tests (serially)
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/serial --max-concurrency=1
```

### Run Only ingress_operator.go Tests

```bash
# Run all 14 ingress operator tests by name filter
./bin/cluster-ingress-operator-tests-ext run-test \
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-Medium-26150-misc tests for ingress operator [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:hongli-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-Medium-22633-The nodeSelector and tolerations of router deployment are controlled by ingresscontrolle [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:mjoseph-ROSA-OSD_CCS-ARO-Critical-22636-The namespaceSelector of router is controlled by ingresscontroller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:mjoseph-ROSA-OSD_CCS-ARO-High-22637-The routeSelector of router is controlled by ingresscontroller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-Medium-56772-Ingress Controller does not set allowPrivilegeEscalation in the router deployment [Serial] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-NonPreRelease-Medium-60012-matchExpressions for routeSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-NonPreRelease-Medium-60013-matchExpressions for namespaceSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-Critical-62530-openshift ingress operator is failing to update router-certs [Serial] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:asood-NonHyperShiftHOST-ConnectedOnly-ROSA-OSD_CCS-Medium-63832-Cluster ingress health checks and routes fail on swapping application router between public and private [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:mjoseph-NonHyperShiftHOST-Critical-64611-Ingress operator support for private hosted zones in Shared VPC clusters [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-High-75907-Ingress Operator should not always remain in the progressing state [Disruptive] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-High-75908-http2 connection coalescing component routing should not be broken with single certificate [Disruptive] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-High-75909-Ingress Operator should not always remain in the progressing state [Disruptive] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-Critical-77283-Router should support SHA1 CA certificates in the default certificate chain [Suite:openshift/conformance/parallel]" --max-concurrency=1
```

### Run a Single Test

```bash
# Run a single test by its full name
./bin/cluster-ingress-operator-tests-ext run-test "[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-NonPreRelease-Medium-60012-matchExpressions for routeSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]"
```

### Running All Tests (Gateway API + Ingress Operator + LB Security Groups)

```bash
# Run all non-slow tests (parallel + serial combined)
./bin/cluster-ingress-operator-tests-ext run-suite ingress/all --max-concurrency=1

# Run all parallel tests (Gateway API + Ingress Operator + LB Security Groups)
./bin/cluster-ingress-operator-tests-ext run-suite ingress/parallel

# Run all serial/disruptive tests (Gateway API + Ingress Operator + LB Security Groups)
./bin/cluster-ingress-operator-tests-ext run-suite ingress/serial --max-concurrency=1

# Run all [Slow]-tagged tests (none currently)
./bin/cluster-ingress-operator-tests-ext run-suite ingress/slow
```

These combined suites are prefixed with `ingress/` because suite names must be
unique across every test extension in the payload, and `openshift-tests` already
declares internal suites such as `all`.

**Important**: 5 of 14 ingress operator tests are `[Serial]` or `[Disruptive]` and **must** run with `--max-concurrency=1`. Disruptive tests modify the default IngressController or cluster-wide ingress configuration and rely on cleanup defers, so running them concurrently causes cascading failures.

The 9 parallel ingress operator tests create isolated custom IngressControllers with unique names and clean up via `defer`, so they are safe to run concurrently.

The 4 managed LB Security Groups tests use a `g.Ordered` Ginkgo container. When run via `run-suite`, Ginkgo keeps the ordered container on one process and runs the specs sequentially, sharing one IC. When run via `run-test -n`, OTE re-execs each matched spec as a separate child process — each process runs its own `BeforeAll` (creating an independent IC), so the tests work independently but consume more resources. See `test/README.md` for LB Security Groups run commands.

## Test Execution Time

- Single test: 2-5 minutes
- Parallel suite: 5-15 minutes
- Serial suite: 20-40 minutes
- All tests: 30-50 minutes

## Files

```text
tests-extension/
├── cmd/main.go                             # OTE entry point, suite definitions
├── test/
│   ├── util.go                             # Shared helper functions (package test)
│   ├── aws_util.go                         # AWS EC2 helper functions
│   ├── lb_security_groups.go               # LB security groups tests
│   ├── gatewayapi.go                       # Gateway API tests
│   ├── README.md                           # Gateway API & LB security groups documentation
│   └── qe/
│       ├── ingress_operator.go             # 14 ingress operator test implementations
│       └── README.md                       # This file
├── Makefile                                # Build targets
├── go.mod                                  # Dependencies
├── go.sum                                  # Dependency checksums
└── vendor/                                 # Vendored dependencies
```

## All Test Names

### ingress_operator.go (14 tests)

```text
[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-Medium-26150-misc tests for ingress operator
[sig-network-edge][Feature:IngressOperator] Author:hongli-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-Medium-22633-The nodeSelector and tolerations of router deployment are controlled by ingresscontrolle
[sig-network-edge][Feature:IngressOperator] Author:mjoseph-ROSA-OSD_CCS-ARO-Critical-22636-The namespaceSelector of router is controlled by ingresscontroller
[sig-network-edge][Feature:IngressOperator] Author:mjoseph-ROSA-OSD_CCS-ARO-High-22637-The routeSelector of router is controlled by ingresscontroller
[sig-network-edge][Feature:IngressOperator] Author:shudili-Medium-56772-Ingress Controller does not set allowPrivilegeEscalation in the router deployment [Serial]
[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-NonPreRelease-Medium-60012-matchExpressions for routeSelector defined in an ingress-controller
[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-NonPreRelease-Medium-60013-matchExpressions for namespaceSelector defined in an ingress-controller
[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-Critical-62530-openshift ingress operator is failing to update router-certs [Serial]
[sig-network-edge][Feature:IngressOperator] Author:asood-NonHyperShiftHOST-ConnectedOnly-ROSA-OSD_CCS-Medium-63832-Cluster ingress health checks and routes fail on swapping application router between public and private
[sig-network-edge][Feature:IngressOperator] Author:mjoseph-NonHyperShiftHOST-Critical-64611-Ingress operator support for private hosted zones in Shared VPC clusters
[sig-network-edge][Feature:IngressOperator] Author:shudili-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-High-75907-Ingress Operator should not always remain in the progressing state [Disruptive]
[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-High-75908-http2 connection coalescing component routing should not be broken with single certificate [Disruptive]
[sig-network-edge][Feature:IngressOperator] Author:shudili-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-High-75909-Ingress Operator should not always remain in the progressing state [Disruptive]
[sig-network-edge][Feature:IngressOperator] Author:shudili-ROSA-OSD_CCS-ARO-Critical-77283-Router should support SHA1 CA certificates in the default certificate chain
```
