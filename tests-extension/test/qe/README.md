# cluster-ingress-operator OTE Test Extension — Ingress QE Tests

The QE package carries 39 test cases migrated from openshift-tests-private:

* 14 IngressController lifecycle, router deployment, routing, and certificate
  cases in `ingress_operator.go`.
* 11 Ingress-to-Route conversion, sharding, service recovery, capability, and
  label cases in `ingress.go`, migrated from
  `openshift-tests-private/test/extended/router/ingress.go`. TC41117 is
  intentionally omitted because equivalent IngressClass lifecycle coverage
  already exists in the operator e2e suite.
* 5 cloud load-balancer cases in `cloud_load_balancer.go`, migrated from
  `openshift-tests-private/test/extended/router/cloud_load_balancer.go`.
  OCP-36891, OCP-75439, and OCP-75617 are intentionally omitted.
* 9 tuning cases in `tuning.go`, migrated from
  `openshift-tests-private/test/extended/router/tuning.go`. OCP-43112 is
  intentionally omitted; because the omitted source spec also carried
  OCP-43111, that case is omitted with it.

These files follow the same structure: a single component describe block, one
`g.It` per case, numbered `g.By` steps, a test-owned namespace and fixtures,
and cleanup through `defer`. `ingress_operator.go` uses
`Network_Edge Component_Ingress-Operator`, `ingress.go` uses
`Network_Edge Component_Ingress`, `cloud_load_balancer.go` uses
`Network_Edge Component_Route-CLB`, and `tuning.go` uses
`Network_Edge Component_Route-Tunning`. They are registered
together through the `ingress-operator/*` suites; there is no separate router
suite family.

## Test Files

| File | Tests | Description |
|------|-------|-------------|
| `ingress_operator.go` | 14 | IngressController lifecycle, router deployment, routing, and certificate tests |
| `ingress.go` | 11 | Ingress-to-Route conversion, subdomain and shard routing, service recovery, capability, and label propagation |
| `cloud_load_balancer.go` | 5 | Default endpoint publishing and AWS/Azure load-balancer configuration tests |
| `tuning.go` | 9 | HAProxy tuningOptions configuration and validation tests |

## Test naming and registration

Every test name has the shape:

```text
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge <component> <description> [<execution tags>] [Suite:...]
```

Each `g.It` carries its canonical case label, for example
`g.Label("OCP-33960")`. Authors appear only in `//Author:` source comments.
Priorities and platform tokens are omitted from titles. `[Serial]` and
`[Disruptive]` remain execution tags, and `appendSuiteNames` in
`../../cmd/main.go` appends the conformance suite suffix.

The 11 cases in `ingress.go` were first registered as
`Network_Edge Component_Router` cases. The existing `ingress_operator.go` and
`ingress.go` cases now use component-specific prefixes, and `OriginalName`
records their first `Component_Router` names for downstream continuity. All
`[OTP]` specs are **informing** while they build a reliability record.

`[Level0]` gates entry to the conformance suites. Confirm the classification
against the QE-approved Level0 plan before promotion.

## Test Suites

All 39 `[OTP]` cases are selected by the existing `ingress-operator/*` suites:

| Suite | Cases before environment filtering | Conformance parent |
|-------|------------------------------------|--------------------|
| `ingress-operator/conformance/parallel` | 31 | `openshift/conformance/parallel` |
| `ingress-operator/conformance/serial` | 4: OCP-56772, OCP-62530, OCP-57370, OCP-85280 | `openshift/conformance/serial` |
| `ingress-operator/disruptive` | 4: OCP-75907, OCP-75908, OCP-75909, OCP-56228 | None |
| `ingress-operator/non-disruptive` | 35 | None |
| `ingress-operator/all` | 39 (`--max-concurrency=1`) | None |

The disruptive cases have no conformance parent and require explicit
selection. 56228 deletes the default router service, and 75907, 75908, and
75909 patch cluster ingress configuration or restart the ingress operator.
Run `ingress-operator/disruptive` only on a cluster that can tolerate that
disruption.

The combined `ingress/*` suites cover Gateway API, LB security group, and QE
tests. `ingress/serial` excludes disruptive cases; select
`ingress-operator/disruptive` explicitly for those.

## Support Matrix

These tests came from openshift-tests-private, where tokens in the test name
(`NonHyperShiftHOST`, `ConnectedOnly`) acted as skips. OTE does not parse test
names, so the constraints are declared as environment selectors in
`../../cmd/environment_selectors.go`, keyed on the `OCP-<id>` label. Add a
selector there when a new case is platform-limited.

| Restriction | Applied cases |
|-------------|---------------|
| Require `config.openshift.io` API group, excluding MicroShift | All 39 |
| Exclude hosted-cluster topology `External` | OCP-22633, OCP-51429, OCP-51437, OCP-57370, OCP-63832, OCP-64611, OCP-75440, OCP-75499, OCP-75907, OCP-75908, OCP-75909 |
| Exclude `externalConnectivity == "Disconnected"` | OCP-63832 |
| Require the `ImageRegistry` capability | OCP-22636, OCP-22637, OCP-33960, OCP-41109, OCP-51148, OCP-51429, OCP-51437, OCP-57089, OCP-60012, OCP-60013, OCP-85280, OCP-85950 |
| Require platform `azure` | OCP-57089, OCP-72126 |
| Exclude IPv6 network stack | OCP-72126 |
| Require platform `aws` | OCP-52837, OCP-75440, OCP-75499 |
| Exclude bare metal and vSphere | OCP-75909 |

The web server fixtures pull
`image-registry.openshift-image-registry.svc:5000/openshift/tools:latest`, so
the tests work on disconnected clusters when the `ImageRegistry` capability is
available. No case is excluded solely because of external connectivity. The
tests that create the web server are gated on `ImageRegistry` because that
capability serves the payload tools image.

MicroShift is gated on the API group, not on the platform. Excluding
`platform=none` looks like the obvious way to skip it and is wrong: origin sets
`ProviderName` only for vSphere, AWS, GCP, Azure and IBM Cloud, defaulting
otherwise to `skeleton` and reporting that as `none`. Assisted bare metal and
Azure Stack therefore report `none` too, and excluding it would silently
disable these specs on supported platforms. `openshift-tests` passes
`--api-group` only when it can discover cluster state, which it cannot do on
MicroShift.

Several cases also carry narrow runtime guards: OCP-57089 and OCP-72126 skip
on non-Azure platforms, OCP-52837, OCP-75440, and OCP-75499 skip on non-AWS
platforms, OCP-56228 skips when `openshift-ingress/router-default` is absent
(for example on a hosted control plane), OCP-57370 skips when the console
namespace is absent, OCP-63832 and OCP-64611 skip on non-AWS platforms,
OCP-64611 also skips when the cluster is not a shared-VPC install, and
OCP-75908 skips when HTTP/2 is not enabled.

### Web server fixture reliability

`CreateWebServerPodAndService` in `../util.go` waits for the namespace's
default service account to receive usable integrated-registry credentials
before it creates the pod, sets the resolved `imagePullSecrets` explicitly on
the pod, and waits for the pod to become ready before returning. It also fails
after 30 seconds of persistent `ErrImagePull`/`ImagePullBackOff` rather than
polling for the full timeout.

This matters for upgrade payloads: a pod created in a fresh namespace before
the service account's pull secret is injected attempts an anonymous pull, the
internal registry returns 401, and the resulting repeated BackOff events trip
the `legacy-test-framework-invariants-pathological` monitor. That is the
failure that caused the PR #1654 revert. Any new fixture that pulls an image
from the internal registry must use the same gate.

Before re-landing or re-enabling these cases, run the AWS minor-upgrade and GCP
micro-upgrade payload jobs and confirm the pathological-events invariant stays
clean.

### Per-lane membership

These counts assume the `ImageRegistry` capability is present and no tests are
filtered by name.

| Lane | `ingress-operator/all` |
|------|------------------------|
| Azure | 36 |
| AWS | 37 |
| bare metal IPI (`platform=baremetal`) | 33 |
| assisted bare metal (`platform=none`) | 34 |
| vSphere | 33 |
| disconnected | 36 |
| HyperShift guest (`topology=External`) | 26 |
| MicroShift (no `--api-group`) | 0 |

## Build, list, and run

Run from `tests-extension/`; `make build` refreshes
`bin/cluster-ingress-operator-tests-ext`:

```bash
make build

# List all QE cases.
./bin/cluster-ingress-operator-tests-ext list tests --suite ingress-operator/all -o names

# Inspect the selection for a supported cluster profile.
./bin/cluster-ingress-operator-tests-ext list tests --suite ingress-operator/all \
  --api-group config.openshift.io --optional-capability ImageRegistry \
  --platform azure --external-connectivity Direct \
  --topology HighlyAvailable --network-stack ipv4 -o names

export KUBECONFIG=/path/to/kubeconfig
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/all --max-concurrency=1

# Run the conformance subsets.
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/conformance/parallel
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/conformance/serial --max-concurrency=1

# Run the disruptive cases only on a cluster that can tolerate the disruption.
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/disruptive --max-concurrency=1

# Resolve a case ID to its current title, then run that exact name.
test_name=$(./bin/cluster-ingress-operator-tests-ext list tests --suite ingress-operator/all | \
  jq -r '.[] | select(.labels | has("OCP-33960")) | .name')
./bin/cluster-ingress-operator-tests-ext run-test "$test_name"
```

An unfiltered list shows all registered cases; passing environment flags
applies the cluster selectors. Case IDs appear in labels rather than names.

### Run only `ingress_operator.go` tests

```bash
# Run all 14 ingress operator tests by name.
./bin/cluster-ingress-operator-tests-ext run-test \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator misc tests for ingress operator [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator The nodeSelector and tolerations of router deployment are controlled by ingresscontroller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator The namespaceSelector of router is controlled by ingresscontroller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator The routeSelector of router is controlled by ingresscontroller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator Ingress Controller sets allowPrivilegeEscalation on the haproxy container in the router deployment [Serial] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator matchExpressions for routeSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator matchExpressions for namespaceSelector defined in an ingress-controller [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator openshift ingress operator is failing to update router-certs [Serial] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator Cluster ingress health checks and routes fail on swapping application router between public and private [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator Ingress operator support for private hosted zones in Shared VPC clusters [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator Ingress Operator should not remain progressing after a clientca-configmap or service port change [Disruptive]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator http2 connection coalescing component routing should not be broken with single certificate [Disruptive]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator Ingress Operator should not remain progressing from repeated internal service or router deployment updates [Disruptive]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress-Operator Router should support SHA1 CA certificates in the default certificate chain [Suite:openshift/conformance/parallel]" \
  --max-concurrency=1
```

### Run only `ingress.go` cases

```bash
# Run the 11 Ingress-to-Route cases by name.
./bin/cluster-ingress-operator-tests-ext run-test \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress Setting route.openshift.io/termination annotation to Edge/Passthrough/Reencrypt in ingress resource [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress use IngressClass controller for ingress-to-route [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress host name of the route depends on the subdomain if provided [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress different router deployment with same route using subdomain [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress Router deployment using different shard with same subdomain [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress Deletion of default router service is recovered [Disruptive]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress Azure external and internal load balancer services receive IPs [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress component route hostname is RFC compliant [Serial] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress Ingress operator is enabled as an optional component [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress router rejects invalid certificate content on ingress [Serial] [Suite:openshift/conformance/serial]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress Propagate ingress labels to generated routes [Suite:openshift/conformance/parallel]" \
  --max-concurrency=1
```

Named `run-test` selection bypasses the environment selectors. On HyperShift
(`topology=External`) or clusters without the `ImageRegistry` capability, omit
OCP-51429 and OCP-51437 from this command; both read router pods and both
create the web server.

### Run only `cloud_load_balancer.go` cases

```bash
# Run the 5 cloud load-balancer cases by name.
./bin/cluster-ingress-operator-tests-ext run-test \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-CLB IngressController sets the expected endpoint publishing strategy for the cluster platform [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-CLB Switching an AWS load balancer between Classic and NLB requires service recreation [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-CLB IngressController supports multiple CIDR blocks in one Azure NSG rule [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-CLB AWS NLB supports selecting subnets [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-CLB AWS NLB allocates and updates Elastic IP allocations [Suite:openshift/conformance/parallel]" \
  --max-concurrency=1
```

Named selection bypasses the environment selectors, but the specs still have
runtime platform guards. OCP-52837, OCP-75440, and OCP-75499 skip on non-AWS
clusters; OCP-72126 skips on non-Azure clusters. OCP-75440 and OCP-75499 are
excluded from hosted-cluster topology `External` by environment selectors; when
running the named command directly on a hosted cluster, omit those two specs.

### Cloud load-balancer case notes

* OCP-21599 is read-only and checks the default IngressController endpoint
  publishing strategy on every platform.
* OCP-52837 changes a custom router between AWS Classic and Network load
  balancers and recreates its LoadBalancer Service twice.
* OCP-72126 creates an Azure load balancer with 3995 allowed source ranges,
  then adds six more to verify the 4000-source-IP limit is reported. This case
  is Azure-only and intentionally IPv4-specific; it uses IPv4 `/32` ranges and
  does not apply to IPv6 clusters.
* OCP-75440 discovers installer-created public subnets and configures them on
  an AWS NLB. It uses the `auto-delete-load-balancer` annotation to replace the
  Service.
* OCP-75499 allocates two sets of AWS Elastic IPs, assigns each set in turn to
  an NLB, and releases the addresses during cleanup. The number of Elastic IPs
  matches the number of discovered public subnets.

### Run only `tuning.go` cases

```bash
# Run the 9 tuning cases by name.
./bin/cluster-ingress-operator-tests-ext run-test \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning IngressController tuningOptions configure HAProxy header buffers [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning The threadCount parameter controls HAProxy nbthread [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning TuningOptions configure TCP client and server timeouts [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning TuningOptions configure TLS inspect delay [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning TuningOptions configure router backend check interval [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning TuningOptions configure ROUTER_MAX_CONNECTIONS [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning TuningOptions configure HAProxy reload interval [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning Default HAProxy maxconn is 50000 [Suite:openshift/conformance/parallel]" \
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning TuningOptions configure HTTP keep-alive timeout [Suite:openshift/conformance/parallel]" \
  --max-concurrency=1
```

### Tuning case notes

* OCP-40747 covers the source cases OCP-40747, OCP-40748, OCP-40821, and
  OCP-40822. Both custom IngressController names are variables, and patches and
  deployment lookups always use the matching variable.
* OCP-41110 also covers OCP-41128 validation.
* OCP-50662 also covers OCP-50663 validation.
* OCP-50926 also covers OCP-50928 validation.
* OCP-53605 also covers OCP-53608 validation.
* OCP-43105 asserts `ROUTER_DEFAULT_SERVER_TIMEOUT=33s` with the unit included.
* OCP-55367 compares the custom router deployment, not `router-default`, for
  the null-update observation. It configures `maxConnections: 500000` and
  asserts the exact `haproxy.config` line `maxconn 500000`; it no longer uses a
  substring assertion that would also match `maxconn 50000`.
* Every tuning case creates a NodePort IngressController, so the file has no
  additional platform selector.

## Registration metadata and validation

```bash
make update-metadata
go test -mod=vendor ./...
go vet -mod=vendor ./...
make verify
```

Commit `.openshift-tests-extension/openshift_payload_cluster-ingress-operator.json`
with registration changes. `make verify-metadata` regenerates metadata and
rejects uncommitted changes. Source locations are normalized so the metadata
does not depend on the checkout directory.

## Ownership

`[Feature:Router]` is required for ownership. ci-test-mapping assigns these
tests to `Networking / router` through the `sig-network-edge` matcher with
`IncludeAll: ["Feature:Router"]`. The other matcher, `Suite: "Network_Edge"`,
compares the junit suite field and never matches tests run through the payload
runner, which reports suite `openshift-tests`.

Without the tag the specs are unowned in Component Readiness: regressions are
not attributed to Networking / router and nobody is paged. Registering the
binary in origin makes tests discoverable; it does not assign ownership, and
marking a test informing does not waive it. Authors are recorded as a source
comment immediately above each `g.It`:

```go
//Author:shudili
g.It("Propagate ingress labels to generated routes", g.Label("OCP-85950"), func() {
```

## Renaming or removing a test

`make update-metadata` compares the binary against the committed metadata and
refuses to regenerate it when a test has disappeared without being declared:

```text
Missing Tests:
  * [sig-network-edge][OTP][Level0][Feature:Router] Network_Edge <component> <previous description>
Error: missing tests, if you've renamed tests you must add their names to
       OriginalName, or mark them obsolete
```

A rename counts as a disappearance, so declare it *before* regenerating. Keep
the first recorded name in `OriginalName` so the original identity is never
overwritten:

```go
if spec.Name == "<current name>" && spec.OriginalName == "" {
    spec.OriginalName = "<first recorded name>"
}
```

For a test that is genuinely gone, declare it with `ext.IgnoreObsoleteTests`.
Either way, run `make update-metadata` afterwards and commit the result.

## Lifecycle

All 39 cases are **informing**: they report results but do not fail a payload.
The integration guide sets a 99% pass bar for the conformance suites and allows
a ported test to inform while it earns that. Before promoting any case to
blocking, confirm clean runs on MicroShift, metal IPv6 disconnected,
metal/vSphere serial, HyperShift serial, and ordinary cloud lanes, and map the
IDs into Sippy.

## Test execution time

* A single case normally takes 2-5 minutes. OCP-57089, OCP-63832, and the
  cloud load-balancer cases provision load balancers and can run longer;
  OCP-75499 also allocates Elastic IPs. OCP-51437 creates two routers and can
  approach the parallel-suite budget. The tuning cases create one or two
  NodePort routers and perform multiple rolling updates.
* The full `ingress-operator/all` suite takes roughly 40-70 minutes at
  `--max-concurrency=1`.
* The `ingress-operator/conformance/parallel` subset is the right default for
  presubmit coverage; run serial and disruptive cases separately.

## Files

```text
tests-extension/
├── cmd/
│   ├── main.go                             # OTE entry point, suite definitions
│   └── environment_selectors.go            # Platform, topology, and capability gates
├── test/
│   ├── util.go                             # Shared helper functions (package test)
│   ├── aws_util.go                         # AWS EC2 helper functions
│   ├── lb_security_groups.go               # LB security groups tests
│   ├── gatewayapi.go                       # Gateway API tests
│   ├── README.md                           # Gateway API & LB security groups documentation
│   └── qe/
│       ├── ingress_operator.go             # 14 ingress operator test implementations
│       ├── ingress.go                      # 11 Ingress-to-Route test implementations
│       ├── cloud_load_balancer.go          # 5 cloud load-balancer test implementations
│       ├── tuning.go                       # 9 HAProxy tuningOptions test implementations
│       └── README.md                       # This file
├── Makefile                                # Build targets
├── go.mod                                  # Dependencies
├── go.sum                                  # Dependency checksums
└── vendor/                                 # Vendored dependencies
```
