# cluster-ingress-operator OTE Test Extension — Ingress QE Tests

The QE package carries 14 IngressController lifecycle, router deployment,
routing, and certificate cases migrated from openshift-tests-private.

All cases live in `ingress_operator.go` and share a single
`Network_Edge Component_Router` describe block, one `g.It` per case, numbered
`g.By` steps, a test-owned namespace and fixtures, and cleanup through `defer`.
They are registered through the `ingress-operator/*` suites; there is no
separate router suite family.

## Test Files

| File | Tests | Description |
|------|-------|-------------|
| `ingress_operator.go` | 14 | IngressController lifecycle, router deployment, routing, and certificate tests |

## Test naming and registration

Every test name has the shape:

```text
[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router <description> [<execution tags>] [Suite:...]
```

Each `g.It` carries its canonical case label, for example
`g.Label("OCP-26150")`. Authors appear only in `//Author:` source comments.
Priorities and platform tokens are omitted from titles. `[Serial]` and
`[Disruptive]` remain execution tags, and `appendSuiteNames` in
`../../cmd/main.go` appends the conformance suite suffix.

The cases are **informing**: `cmd/main.go` marks every `[OTP]` spec informing
while it builds a reliability record. `[Level0]` gates entry to the conformance
suites. Confirm the classification against the QE-approved Level0 plan before
promotion.

## Test Suites

All 14 `[OTP]` cases are selected by the existing `ingress-operator/*` suites:

| Suite | Cases before environment filtering | Conformance parent |
|-------|------------------------------------|--------------------|
| `ingress-operator/conformance/parallel` | 9 | `openshift/conformance/parallel` |
| `ingress-operator/conformance/serial` | 2: OCP-56772, OCP-62530 | `openshift/conformance/serial` |
| `ingress-operator/disruptive` | 3: OCP-75907, OCP-75908, OCP-75909 | None |
| `ingress-operator/non-disruptive` | 11 | None |
| `ingress-operator/all` | 14 (`--max-concurrency=1`) | None |

The disruptive cases have no conformance parent and require explicit
selection. 75907, 75908, and 75909 patch cluster ingress configuration or
restart the ingress operator. Run `ingress-operator/disruptive` only on a
cluster that can tolerate that disruption.

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
| Require `config.openshift.io` API group, excluding MicroShift | All 14 |
| Exclude hosted-cluster topology `External` | OCP-22633, OCP-63832, OCP-64611, OCP-75907, OCP-75908, OCP-75909 |
| Exclude `externalConnectivity == "Disconnected"` | OCP-63832 |
| Require the `ImageRegistry` capability | OCP-22636, OCP-22637, OCP-60012, OCP-60013 |
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

Several cases also carry narrow runtime guards: OCP-63832 and OCP-64611 skip on
non-AWS platforms, OCP-64611 also skips when the cluster is not a shared-VPC
install, and OCP-75908 skips when HTTP/2 is not enabled.

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
| Azure | 14 |
| AWS | 14 |
| bare metal IPI (`platform=baremetal`) | 13 |
| assisted bare metal (`platform=none`) | 14 |
| vSphere | 13 |
| disconnected | 13 |
| HyperShift guest (`topology=External`) | 8 |
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
  jq -r '.[] | select(.labels | has("OCP-26150")) | .name')
./bin/cluster-ingress-operator-tests-ext run-test "$test_name"
```

An unfiltered list shows all registered cases; passing environment flags
applies the cluster selectors. Case IDs appear in labels rather than names.

### Run only `ingress_operator.go` tests

```bash
# Run all 14 ingress operator tests by name.
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
  -n "[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router Router should support SHA1 CA certificates in the default certificate chain [Suite:openshift/conformance/parallel]" \
  --max-concurrency=1
```

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
g.It("misc tests for ingress operator", g.Label("OCP-26150"), func() {
```

## Renaming or removing a test

`make update-metadata` compares the binary against the committed metadata and
refuses to regenerate it when a test has disappeared without being declared:

```text
Missing Tests:
  * [sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Router <previous description>
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

All 14 cases are **informing**: they report results but do not fail a payload.
The integration guide sets a 99% pass bar for the conformance suites and allows
a ported test to inform while it earns that. Before promoting any case to
blocking, confirm clean runs on MicroShift, metal IPv6 disconnected,
metal/vSphere serial, HyperShift serial, and ordinary cloud lanes, and map the
IDs into Sippy.

## Test execution time

* A single case normally takes 2-5 minutes. OCP-63832 provisions load
  balancers and can run longer.
* The full `ingress-operator/all` suite takes roughly 20-40 minutes at
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
│   ├── util.go                             # Shared fixtures, cleanup, and certificate helpers
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
