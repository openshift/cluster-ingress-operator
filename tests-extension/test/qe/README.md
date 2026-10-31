# cluster-ingress-operator OTE Test Extension — Ingress Operator QE Tests

All 14 ingress operator test cases have been migrated from openshift-tests-private to the OTE (openshift-tests-extension) framework.

The QE package also includes 11 migrated router/Ingress cases from
`openshift-tests-private/test/extended/router/ingress.go`. TC41117 is
intentionally omitted because equivalent IngressClass lifecycle coverage
already exists in the operator e2e suite. These cases share the
`ingress-router/*` suite family described below. The original 14 cases in
`ingress_operator.go` retain the `ingress-operator/*` suites.

## Test Files

| File | Tests | Description |
|------|-------|-------------|
| `ingress_operator.go` | 14 | IngressController lifecycle, router deployment, routing, and certificate tests |
| `ingress_router.go` | 11 | Migrated Ingress conversion, sharding, service recovery, capability, and label cases |

## Migrated Ingress/router registration

The 11 cases in `ingress_router.go` follow the registration conventions in
[openshift/router#871](https://github.com/openshift/router/pull/871):

```text
[sig-network-edge][OTP][Level0] Network_Edge Component_Router <description> [<execution tags>]
```

Each `g.It` has its canonical case label, for example `g.Label("OCP-33960")`.
Authors appear only in `//Author:` source comments, and priorities and platform
tokens are omitted from titles. `[Serial]` and `[Disruptive]` remain execution
tags. `[Level0]` gates entry to the new conformance suites. Ownership must be
verified in ci-test-mapping for the emitted names; no `[Jira:]` tag is added.
Confirm the Level0 classification against the QE-approved plan before promotion.

These 11 cases are **informing**. `OriginalName` preserves their previous OTE
identities. The test bodies, fixtures, and helper behavior are unchanged by this
registration update. The existing 14 ingress-operator cases keep their current
names and lifecycle.

| Suite | Cases before environment filtering | Conformance parent |
|-------|------------------------------------|--------------------|
| `ingress-router/conformance/parallel` | 8 | `openshift/conformance/parallel` |
| `ingress-router/conformance/serial` | 2: OCP-57370, OCP-85280 | `openshift/conformance/serial` |
| `ingress-router/disruptive` | 1: OCP-56228 | None |
| `ingress-router/non-disruptive` | 10 | None |
| `ingress-router/all` | 11 | None |

Serial and disruptive cases have resource isolation. OCP-56228 is excluded from
both new conformance suites and the combined `ingress/serial` suite. Select it
explicitly in a lane that supports intentional router outages.

| Restriction | Applied cases |
|-------------|---------------|
| Require `config.openshift.io` API group, excluding MicroShift | All 11 |
| Exclude `externalConnectivity == "Disconnected"` while fixtures require external images | All 11 |
| Exclude hosted-cluster topology `External` | OCP-51437, OCP-57370 |
| Require platform `azure` | OCP-57089 |

Bare metal (`platform=none`), SingleReplica, and connected/proxied IPv6 remain
eligible. The disconnected restriction is temporary: image setup still pulls
directly from the pinned source registry. Removing that restriction requires CI
image mapping/mirror support and a passing disconnected run, as described in the
reference PR. This update does not change image setup.

### Build, list, and run the 11 migrated cases

Run from `tests-extension/`; `make build` refreshes `bin/cluster-ingress-operator-tests-ext`:

```bash
make build
./bin/cluster-ingress-operator-tests-ext list tests --suite ingress-router/all -o names

# Inspect the selection for a supported cluster profile.
./bin/cluster-ingress-operator-tests-ext list tests --suite ingress-router/all \
  --api-group config.openshift.io --external-connectivity Direct \
  --platform azure --topology HighlyAvailable -o names

export KUBECONFIG=/path/to/kubeconfig
./bin/cluster-ingress-operator-tests-ext run-suite ingress-router/non-disruptive --max-concurrency=1

# Resolve a case ID to its current descriptive title, then run that exact name.
test_name=$(./bin/cluster-ingress-operator-tests-ext list tests --suite ingress-router/all | \
  jq -r '.[] | select(.labels | has("OCP-33960")) | .name')
./bin/cluster-ingress-operator-tests-ext run-test "$test_name"
```

An unfiltered list shows all registered cases; passing environment flags applies
the cluster selectors. Case IDs now appear in labels rather than names. A run
command using the earlier `Author:...-33960-...` title must use the new title.

### Registration metadata and validation

```bash
make update-metadata
go test -mod=vendor ./...
go vet -mod=vendor ./...
make fmt
```

Commit `.openshift-tests-extension/openshift_payload_cluster-ingress-operator.json`
with registration changes. `make verify-metadata` regenerates metadata and
rejects uncommitted changes. Renames must retain the first `OriginalName`;
declare intentional removals with OTE's obsolete-test mechanism before updating.
Source locations are normalized so the metadata does not depend on the checkout.

The registration unit test checks real Ginkgo discovery, Level0 gates, case labels,
suite counts, and the connectivity/topology/platform/network-stack selection
matrix. It does not run the cluster test bodies or establish passing e2e coverage.

## Test Suites

### ingress-operator/all
All 14 original ingress-operator tests (use `--max-concurrency=1` since some are disruptive)

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
- 56772 - Ingress Controller sets allowPrivilegeEscalation on the haproxy container in the router deployment [Serial]
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
  -n "[sig-network-edge][Feature:IngressOperator] Author:shudili-Medium-56772-Ingress Controller sets allowPrivilegeEscalation on the haproxy container in the router deployment [Serial] [Suite:openshift/conformance/serial]" \
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

### Run Only ingress_router.go Tests

```bash
# Run all 11 ingress router tests by name filter
./bin/cluster-ingress-operator-tests-ext run-test \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router Setting route.openshift.io/termination annotation to Edge/Passthrough/Reencrypt in ingress resource" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router use IngressClass controller for ingress-to-route" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router host name of the route depends on the subdomain if provided" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router different router deployment with same route using subdomain" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router Router deployment using different shard with same subdomain" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router Deletion of default router service is recovered [Disruptive]" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router Azure external and internal load balancer services receive IPs" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router component route hostname is RFC compliant [Serial]" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router Ingress operator is enabled as an optional component" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router router rejects invalid certificate content on ingress [Serial]" \
  -n "[sig-network-edge][OTP][Level0] Network_Edge Component_Router Propagate ingress labels to generated routes" \
  --max-concurrency=1
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

The 4 managed LB Security Groups tests use a `g.Ordered` Ginkgo container. OTE re-execs each matched spec as a separate child process for both `run-suite` and `run-test -n` — it only runs in-process when a single spec is selected — so each process runs its own `BeforeAll` and creates an independent IC. The tests work independently but consume more resources. Use `run-suite lb-security-groups/all`, which pins `Parallelism: 1`, to create them one at a time. See `test/README.md` for LB Security Groups run commands.

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
[sig-network-edge][Feature:IngressOperator] Author:shudili-Medium-56772-Ingress Controller sets allowPrivilegeEscalation on the haproxy container in the router deployment [Serial]
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
