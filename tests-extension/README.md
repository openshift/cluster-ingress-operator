# Cluster Ingress Operator Tests Extension (OTE)

This directory contains the [OpenShift Tests Extension (OTE)](https://github.com/openshift-eng/openshift-tests-extension) for the cluster-ingress-operator. It is a separate Go module that builds a single test binary (`cluster-ingress-operator-tests-ext.gz`) shipped inside the operator's release image and discovered at runtime by `openshift-tests`.

## Directory Structure

```text
tests-extension/
├── cmd/main.go                          # binary entry point
├── go.mod                               # separate Go module
├── Makefile                             # build and metadata targets
├── .openshift-tests-extension/
│   └── openshift_payload_cluster-ingress-operator.json  # generated, committed
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

| Tag | Meaning |
|-----|---------|
| `[sig-network-edge]` | Required on all tests — signals routing/ingress component |
| `[OTE]` | Required on all tests — marks the test as living in this repo, not in origin |
| `[Jira:NE]` | Jira project key for the Network Edge team; confirm exact key with the team |
| `[Serial]` | Must run in isolation |
| `[Slow]` | Takes > 5 minutes; requires architect approval |
| `[Disruptive]` | Disrupts the cluster; auto-promoted to `[Serial]` |

### Ginkgo labels (via `g.Label(...)`)

| Label | Meaning |
|-------|---------|
| `ReleaseGate` | Promotes an Extended test into the standard conformance suites |
| `Extended` | Auto-applied to all tests in `test/qe/specs/`; do NOT add manually |
| `StressTest` | Routes test into the `candidate/stress` suite only |
| `NonHyperShiftHOST` | Excludes test from HyperShift external topology |

> **Rule**: Do NOT add `ReleaseGate` to `[Disruptive]`, `[Slow]`, or `StressTest` tests.

## Build and Verify

```bash
cd tests-extension/

# Build the binary
make build

# List all registered tests
./bin/cluster-ingress-operator-tests-ext list -o names

# Regenerate metadata after adding/renaming/removing tests
make build-update

# Verify metadata is up to date (runs in CI)
# Rebuilds the binary, regenerates the JSON, and fails with a diff if the
# committed JSON does not match what the binary produces.
make verify-metadata
```

## Renaming and Deleting Tests

The committed metadata JSON (`.openshift-tests-extension/openshift_payload_cluster-ingress-operator.json`) is the source of truth for what tests exist. Only the gzipped binary is shipped in the operator image — the JSON is **not** included. At runtime, `openshift-tests` discovers the binary, decompresses it, and invokes it directly to get the test list and metadata; the binary is self-describing.

The JSON's purpose is purely **dev/CI time validation**: `make verify-metadata` runs in CI on every PR. It rebuilds the binary, regenerates the JSON from scratch, and **fails if the result differs from the committed file**. This catches accidental renames, deletions, or additions before they merge.

> **Note**: A test's full name is the concatenation of all enclosing `Describe`/`Context` strings and the `It` string, joined by spaces. Tags such as `[sig-network-edge]` and `[OTE]` are part of the outer `Describe` string and are therefore part of the full name. Changing any tag counts as a rename.

### Rename

When a test is renamed, the old name remains in the committed metadata JSON. `make verify-metadata` fails because the JSON contains a name that the binary no longer registers. Historical test results in dashboards (Sippy, etc.) are also tracked by name — a silent rename severs that history.

To rename correctly:

1. Add `g.Label("original-name:<old full test name>")` to the test. This tells the framework the test was previously known by that name, allowing it to match the old metadata entry to the new name and preserve result history.
2. Run `make build-update` to regenerate and commit the updated metadata JSON.

If the test was renamed multiple times, add one `original-name:` label for each previous name.

### Delete

When a test is deleted, its name remains in the committed metadata JSON. `make verify-metadata` fails because a name in the JSON has no corresponding test in the binary.

To delete correctly:

1. Add the old test name to `ext.IgnoreObsoleteTests(...)` in `cmd/main.go` before removing the test code. This explicitly tells the framework the test was intentionally removed.
2. Run `make build-update` to drop the entry from the JSON and commit the result.

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
