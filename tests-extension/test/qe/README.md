# cluster-ingress-operator OTE Test Extension — Ingress Operator QE Tests

All 14 ingress operator test cases have been migrated from openshift-tests-private to the OTE (openshift-tests-extension) framework.

## Test Files

| File | Tests | Description |
|------|-------|-------------|
| `ingress_operator.go` | 14 | IngressController lifecycle, router deployment, routing, and certificate tests |

## Test naming conventions

Every test name has the shape:

```text
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router <description> [<execution tags>]
```

| Token | Meaning |
|-------|---------|
| `[sig-network-edge]` | Broad-area grouping required for component mapping. |
| `[OTP]` | Ported from openshift-tests-private; tracks migration volume and selects the specs into the `ingress-operator/*` suites. |
| `[Level0]` | Tracking tag that selects the test into the `ingress-operator/conformance/*` suites. |
| `[Feature:Router]` | Required for ownership; see [Ownership](#ownership). |
| `OCP-<case ID>` label | Canonical case identity, stored with `g.Label` and independent of the title. |
| `[Serial]` / `[Disruptive]` | Scheduling isolation; see [Test suites](#test-suites). |

Case IDs are Ginkgo labels, and environment selectors key on those labels, so
rewording a description cannot change a test's identity or drop a platform
constraint. The prefix and execution-tag spacing match the router and
cluster-dns-operator extensions.

`[Level0]` should reflect the QE case classification. The OTP source has no
explicit Level0 markers for these 14 cases, so the tag is applied pending
confirmation against the QE-approved Level0 plan or a Polarion export.

Priority prefixes (`Critical`, `High`, `Medium`) are omitted, matching the
router and cluster-dns-operator extensions, neither of which carries one.

### Ownership

`[Feature:Router]` is not decorative. ci-test-mapping assigns these tests to
`Networking / router`, and its two matchers for that component are:

| Matcher | Behaviour |
|---------|-----------|
| `Suite: "Network_Edge"` | Compares the junit **suite** field by exact equality. Tests run through the payload runner report suite `openshift-tests`, so this never matches. |
| `SIG: "sig-network-edge"` + `IncludeAll: ["Feature:Router"]` | Matches on the test **name**. This is the one that can claim these tests, and only if the name contains `Feature:Router`. |

Without that tag the specs are unowned in Component Readiness: regressions are
not attributed to Networking / router and nobody is paged for them. The Gateway
API and LB security group specs in this extension already carry it; the ported
specs now do too. Registering the binary in origin makes tests discoverable, it
does not assign ownership, and marking a test informing does not waive it.

Authors are recorded as a source comment immediately above each `g.It`, not in
the test name:

```go
//Author:shudili
g.It("misc tests for ingress operator", g.Label("OCP-26150"), func() {
```

Ownership is resolved through ci-test-mapping rather than a `[Jira:]` tag.

## Test Suites

| Suite | Parent | Contents |
|-------|--------|----------|
| `ingress-operator/conformance/parallel` | `openshift/conformance/parallel` | 9 `[Level0]` specs, neither serial nor disruptive |
| `ingress-operator/conformance/serial` | `openshift/conformance/serial` | 2 non-disruptive `[Level0] [Serial]` specs: 56772, 62530 |
| `ingress-operator/disruptive` | — | The 3 `[Disruptive]` specs: 75907, 75908, 75909 |
| `ingress-operator/non-disruptive` | — | All ported specs except the disruptive three |
| `ingress-operator/all` | — | All 14 (`--max-concurrency=1`) |

The disruptive specs have no conformance parent and require explicit selection.
75908 patches cluster ingress component routes for console and oauth, and 75909
deletes the ingress-operator pod. These changes can trigger availability monitor
failures in a conformance job even when cleanup succeeds. Run
`ingress-operator/disruptive` only on a cluster that can tolerate the disruption.

## Support Matrix

These tests came from openshift-tests-private, where tokens in the test name
(`NonHyperShiftHOST`, `ConnectedOnly`) acted as skips. OTE does not parse test
names, so those tokens have no effect here and the constraints are declared as
environment selectors in `cmd/environment_selectors.go` instead. Add a selector
there, keyed on the `OCP-<id>` label, when a new case is platform-limited.

| Case | Not run on | Why |
|------|-----------|-----|
| all 14 | MicroShift (no `config.openshift.io` API group) | No ingress operator; no IngressController API or `dns.config/cluster` |
| 22633, 63832, 64611, 75907, 75909 | HyperShift guest (`topology=External`) | Operator and router run on the management cluster, unreachable from a guest kubeconfig |
| 63832 | Disconnected | Verifies health checks that require reaching the load balancer from outside |
| 22636, 22637, 60012, 60013 | Clusters reporting no optional capabilities | Test web server is pulled from the internal registry |
| 75909 | bare metal, vSphere | Internal LoadBalancer never converges there; timed out in all 24 runs. 75907 also uses a private LB but measured 100% on those platforms, so it is not excluded. |

MicroShift is gated on the API group, not on the platform. Excluding
`platform=none` looks like the obvious way to skip it and is wrong: origin sets
`ProviderName` only for vSphere, AWS, GCP, Azure and IBM Cloud, defaulting
otherwise to `skeleton` and reporting that as `none`. Assisted bare metal and
Azure Stack therefore report `none` too, and excluding it would silently
disable all 14 specs on supported platforms. `openshift-tests` passes
`--api-group` only when it can discover cluster state, which it cannot do on
MicroShift.

Two cases carry their own runtime guards rather than selectors, because the
condition is not expressible as a cluster property: 63832 and 64611 skip
themselves on non-AWS platforms, and 64611 also skips when the cluster is not
a shared-VPC install.

### Renaming or removing a test

`make update-metadata` compares the binary against the committed
`.openshift-tests-extension/openshift_payload_cluster-ingress-operator.json`
and **refuses to regenerate it** when a test has disappeared from the binary
without being declared:

```text
Missing Tests:
  * [sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router <previous description>
Error: missing tests, if you've renamed tests you must add their names to
       OriginalName, or mark them obsolete
```

A rename counts as a disappearance, so declare it *before* regenerating. For
the first rename, record the previously registered name as `OriginalName` on
the spec in [`cmd/main.go`](../../cmd/main.go). For later renames, keep that
first recorded name and update only the current-name match, so the original
identity is never overwritten:

```go
if spec.Name == "<current name>" && spec.OriginalName == "" {
    spec.OriginalName = "<first recorded name>"
}
```

For a test that is genuinely gone, declare it with `ext.IgnoreObsoleteTests`.
Either way, run `make update-metadata` afterwards and commit the result;
`make verify` fails while the committed copy is stale.

### Lifecycle

All 14 are **informing**: they report results but do not fail a payload. The
integration guide sets a 99% pass bar for the conformance suites and allows a
ported test to inform while it earns that. Before promoting any of them to
blocking, confirm clean runs on MicroShift, metal IPv6 disconnected,
metal/vSphere serial, HyperShift serial, and ordinary cloud lanes, and map the
IDs into Sippy.

### Verifying the selectors

`list` applies environment selectors, so the per-lane membership can be checked
without a cluster:

```bash
./bin/cluster-ingress-operator-tests-ext list -o names \
  --api-group config.openshift.io --optional-capability ImageRegistry \
  --platform baremetal --network-stack ipv6 \
  --external-connectivity Disconnected
```

Pass `--api-group config.openshift.io` for any lane other than MicroShift;
omitting it reproduces the MicroShift case, where that group is absent.

| Lane | Ported specs |
|------|--------------|
| AWS | 14 |
| MicroShift (no `--api-group`) | 0 |
| assisted bare metal (`platform=none`) | 14 |
| metal IPI (`platform=baremetal`) | 13 |
| vSphere | 13 |
| HyperShift guest (`topology=External`) | 9 |
| disconnected | 13 |

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
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/conformance/parallel

# Run the two non-disruptive serial conformance tests
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/conformance/serial --max-concurrency=1

# Run the three disruptive tests separately on a suitable cluster
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/disruptive --max-concurrency=1
```

### Run Only ingress_operator.go Tests

```bash
# Run all 14 ingress operator tests by name filter
./bin/cluster-ingress-operator-tests-ext run-test \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router misc tests for ingress operator [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router The nodeSelector and tolerations of router deployment are controlled by ingresscontroller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router The namespaceSelector of router is controlled by ingresscontroller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router The routeSelector of router is controlled by ingresscontroller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Ingress Controller sets allowPrivilegeEscalation on the haproxy container in the router deployment [Serial] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router matchExpressions for routeSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router matchExpressions for namespaceSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router openshift ingress operator is failing to update router-certs [Serial] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Cluster ingress health checks and routes fail on swapping application router between public and private [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Ingress operator support for private hosted zones in Shared VPC clusters [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Ingress Operator should not remain progressing after a clientca-configmap or service port change [Disruptive]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router http2 connection coalescing component routing should not be broken with single certificate [Disruptive]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Ingress Operator should not remain progressing from repeated internal service or router deployment updates [Disruptive]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Router should support SHA1 CA certificates in the default certificate chain [Suite:openshift/conformance/parallel]" --max-concurrency=1
```

### Run a Single Test

```bash
# Run a single test by its full name
./bin/cluster-ingress-operator-tests-ext run-test "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router matchExpressions for routeSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]"
```

### Running All Tests (Gateway API + Ingress Operator + LB Security Groups)

```bash
# Run all non-slow tests, including disruptive ones
./bin/cluster-ingress-operator-tests-ext run-suite ingress/all --max-concurrency=1

# Run all parallel tests (Gateway API + Ingress Operator + LB Security Groups)
./bin/cluster-ingress-operator-tests-ext run-suite ingress/parallel

# Run non-disruptive serial tests (Gateway API + Ingress Operator + LB Security Groups)
./bin/cluster-ingress-operator-tests-ext run-suite ingress/serial --max-concurrency=1

# Run all [Slow]-tagged tests (none currently)
./bin/cluster-ingress-operator-tests-ext run-suite ingress/slow
```

These combined suites are prefixed with `ingress/` because suite names must be
unique across every test extension in the payload, and `openshift-tests` already
declares internal suites such as `all`.

**Important**: 2 of the 14 ingress operator tests carry `[Serial]`, and 3 carry `[Disruptive]`. Run the disruptive suite separately with `--max-concurrency=1`; those tests patch cluster-wide ingress configuration or restart the ingress operator.

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
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router misc tests for ingress operator [Suite:openshift/conformance/parallel]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router The nodeSelector and tolerations of router deployment are controlled by ingresscontroller [Suite:openshift/conformance/parallel]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router The namespaceSelector of router is controlled by ingresscontroller [Suite:openshift/conformance/parallel]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router The routeSelector of router is controlled by ingresscontroller [Suite:openshift/conformance/parallel]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Ingress Controller sets allowPrivilegeEscalation on the haproxy container in the router deployment [Serial] [Suite:openshift/conformance/serial]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router matchExpressions for routeSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router matchExpressions for namespaceSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router openshift ingress operator is failing to update router-certs [Serial] [Suite:openshift/conformance/serial]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Cluster ingress health checks and routes fail on swapping application router between public and private [Suite:openshift/conformance/parallel]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Ingress operator support for private hosted zones in Shared VPC clusters [Suite:openshift/conformance/parallel]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Ingress Operator should not remain progressing after a clientca-configmap or service port change [Disruptive]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router http2 connection coalescing component routing should not be broken with single certificate [Disruptive]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Ingress Operator should not remain progressing from repeated internal service or router deployment updates [Disruptive]
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Router should support SHA1 CA certificates in the default certificate chain [Suite:openshift/conformance/parallel]
```
