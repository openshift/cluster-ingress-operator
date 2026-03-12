# Cluster Ingress Operator Tests Extension (OTE)

This directory contains the [OpenShift Tests Extension (OTE)](https://github.com/openshift-eng/openshift-tests-extension) for the cluster-ingress-operator. It is a separate Go module that builds a single test binary (`cluster-ingress-operator-tests-ext.gz`) shipped inside the operator's release image and discovered at runtime by `openshift-tests`.

## Directory Structure

```text
tests-extension/
├── cmd/main.go                          # binary entry point
├── go.mod                               # separate Go module
├── Makefile                             # build targets
├── test/
│   └── *.go                             # tests migrated from openshift/origin (no Extended label)
└── test/qe/
    ├── specs/
    │   └── *.go                         # tests migrated from openshift-tests-private (auto-labeled Extended)
    └── util/
        ├── client.go                    # shared client helpers
        ├── clusters.go                  # cluster-type helpers (HyperShift, SNO, etc.)
        ├── extensiontest.go             # IngressQeTestsOnly() selector
        └── filters/
            └── filters.go              # suite qualifier helpers
```

> **Note**: The `test/qe/` folder is introduced in PR5+ when the first tests-private tests are migrated.
> PR1 only contains `test/` with origin-migrated GatewayAPI tests.

## Test Placement

| Source | Directory | `Extended` label | Included in conformance? |
|--------|-----------|-----------------|--------------------------|
| `openshift/origin` | `test/` | No | Always |
| `openshift-tests-private` | `test/qe/specs/` | Auto-applied | Only if also `ReleaseGate` |

## Suite Hierarchy

```text
# PR1 (current) — origin-migrated tests only
ingress/parallel    → openshift/conformance/parallel   (standard, non-Serial, non-Slow)
ingress/serial      → openshift/conformance/serial     (standard, Serial, non-Slow)
ingress/slow        → openshift/optional/slow          (standard, Slow)

# PR5+ — added when tests-private tests are migrated
ingress/all                                            (all standard + ReleaseGate extended)
ingress/extended                                       # all Extended (from tests-private)
├── ingress/extended/releasegate                       # Extended + ReleaseGate → conformance
└── ingress/extended/candidate                         # Extended, no ReleaseGate → QE periodic
    ├── ingress/extended/candidate/parallel
    ├── ingress/extended/candidate/serial
    ├── ingress/extended/candidate/fast
    ├── ingress/extended/candidate/slow
    └── ingress/extended/candidate/stress
```

## Label Reference

### Title tags (in test name string)

The tagging policy differs by test source:

- **Origin-migrated tests** (`test/`): preserve the original test name exactly as it appeared in `openshift/origin`. Do NOT add new tags — doing so would constitute a rename and sever historical result data in Sippy and ci-test-mapping.

- **tests-private-migrated tests** (`test/qe/specs/`): these are new to OTE and have no prior result history, so apply the following tags:

| Tag | Meaning |
|-----|---------|
| `[sig-network-edge]` | Required — signals routing/ingress component |
| `PolarionID:XXXXX` | Required — identifies the test case in Polarion |
| `[Serial]` | Must run in isolation |
| `[Slow]` | Takes > 5 minutes; requires architect approval |

> **Migration note**: Replace `[Disruptive]` from old QE tests with `[Serial]` when migrating to OTE.


## Build and Verify

```bash
cd tests-extension/

# Build the binary
make build

# List all registered tests
./bin/cluster-ingress-operator-tests-ext list -o names
```

## Renaming Tests

Avoid renaming tests. Downstream systems such as [Sippy](https://sippy.dptools.openshift.org) and [Component Readiness](https://github.com/openshift-eng/ci-test-mapping) track test results by name — renaming a test severs its historical pass/fail data. Prefer additive changes such as appending new tags rather than rewriting the description.

If a rename is truly necessary, the test mapping in [ci-test-mapping](https://github.com/openshift-eng/ci-test-mapping) may need to be updated to preserve result history.

## Why `openshift/origin` Cannot Be Imported

Origin's package-level `init()` functions register the entire test suite and pull in all cloud SDKs on import, which breaks OTE binaries. Instead, follow the OLM pattern: copy needed utilities into `test/util/` and import `k8s.io/kubernetes/test/e2e/framework` via the OpenShift fork for shared helpers like `e2e.Logf`.

Adding `k8s.io/kubernetes` significantly increases `vendor/` size, so it is deferred to PR4 when the full util package is built. In PR1, use `fmt.Fprintf(g.GinkgoWriter, ...)` and `g.Fail(...)` instead.

## CI Coverage

| Tests | Covered by |
|-------|-----------|
| `test/` (origin-migrated) | Existing `e2e-aws-ovn` presubmit (automatic via suite parent) |
| `test/qe/specs/` with `ReleaseGate` | Existing `e2e-aws-ovn` presubmit (automatic via suite parent) |
| `test/qe/specs/` without `ReleaseGate` | Optional `e2e-aws-ovn-ingress-ext-candidate` presubmit |

The first two rows are automatic once the binary is in the operator image. Only candidate Extended tests need the dedicated optional presubmit (`/test e2e-aws-ovn-ingress-ext-candidate`).

## Migration Plan

Tests are migrated incrementally across multiple repositories. The table below tracks the planned PRs and their status.

| PR | Repo | Description | Status |
|----|------|-------------|--------|
| PR1 | `cluster-ingress-operator` | OTE scaffolding + GatewayAPI tests migrated from origin | In Progress |
| PR2 | `release` | Add `tests-extension` sanity presubmit and optional `e2e-aws-ovn-ingress-ext-candidate` presubmit | Pending |
| PR3 | `origin` | Register the extension binary in the origin extension manifest; remove migrated GatewayAPI tests to avoid duplicate runs | Pending |
| PR4 | `cluster-ingress-operator` | Migrate remaining ingress-operator tests from origin; introduce `test/util/` helper package | Pending |
| PR5+ | `cluster-ingress-operator` | Migrate first tests from `openshift-tests-private`; introduce `test/qe/` infrastructure and Extended suite support | Pending |

## References

- [OTE Enhancement Proposal](https://github.com/openshift/enhancements/blob/master/enhancements/testing/openshift-tests-extension.md)
- [openshift-tests-extension framework](https://github.com/openshift-eng/openshift-tests-extension)
- [operator-framework-operator-controller](https://github.com/openshift/operator-framework-operator-controller/tree/main/openshift/tests-extension) — primary reference
- [operator-framework-olm](https://github.com/openshift/operator-framework-olm/tree/main/tests-extension) — secondary reference
