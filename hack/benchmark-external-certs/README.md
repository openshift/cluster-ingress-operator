# External Certificate Route Benchmark Harness

Benchmarks OpenShift router startup time with `spec.tls.externalCertificate` routes
to validate the fix for [OCPBUGS-77056](https://issues.redhat.com/browse/OCPBUGS-77056)
(sequential secret loading causing router startup times of 20–40 minutes at scale).

## Background

When a router starts with N external-cert routes, it registers a secret informer for each.
The bug: each registration holds a global write lock while waiting for the informer to sync
via etcd, serializing all N registrations. The fix (library-go PR #2132) releases the lock
before `WaitForCacheSync`, allowing concurrent loading.

| Script | Purpose |
|---|---|
| `00-run-all.sh` | **Orchestrator** — runs all phases end-to-end |
| `01-setup-routes.sh` | Creates N TLS secrets + routes with `externalCertificate` (idempotent: wipes first) |
| `03-run-benchmark.sh` | Restarts router, measures secret-loading span + pod-ready wall time |
| `04-cleanup.sh` | Deletes test namespace, restores original router image |

> **Note:** `00-run-all.sh` contains quay credentials — it is `.gitignore`d.
> **Note:** The patched image has been pre-built and pushed to `quay.io/btofel/router:patched`.

## Quick Start (just run the orchestrator)

```bash
./00-run-all.sh --count 200
```

This runs four phases automatically:

1. **Preflight** — verify `oc` login, login to quay.io
2. **Setup** — wipe + create 200 external-cert routes
3. **Baseline** — restart the existing cluster router, measure startup (this shows the bug)
4. **Patched** — deploy pre-built patched image, restart, measure startup (shows the fix)
5. **Compare** — print side-by-side results table

```
  Metric                           BASELINE (bug)   PATCHED (fix)
  -------------------------------- ---------------- ----------------
  Secrets loaded                   200              200
  Secret loading span (s)          ~120s            ~2s
  Pod → Ready wall time (s)        ~150s            ~10s

  Speedup (secret loading): ~60x faster with the fix
```

## Options

```
--count N          Number of routes to create (default: 200)
--namespace NAME   Namespace for test routes (default: extcert-bench)
```

## Requirements

- `oc` CLI logged into a cluster as `cluster-admin`
- `podman` with a running podman machine (`podman machine start`)
- `go` 1.22+ (for building the patched router)
- `openssl` (for self-signed test certs)
- Access to `openshift/router` at `/Users/btofel/workspace/router`
- Access to `openshift/library-go` at `/Users/btofel/workspace/library-go`
  (on branch `fix-external-cert-serialization` with the OCPBUGS-77056 fix)
- `quay.io/btofel/router` repository created on quay.io and accessible from cluster nodes

## What is measured

- **Secret loading span**: time between first `starting informer` and last
  `secret informer started` log line in the router pod — the precise window
  where the serial vs. concurrent lock behavior matters
- **Pod → Ready wall time**: end-to-end time from restart to `Ready=True`

## Running individual phases

```bash
# Create routes only
./01-setup-routes.sh --count 200

# Benchmark existing router (baseline)
./03-run-benchmark.sh --existing

# Benchmark custom image
./03-run-benchmark.sh --image quay.io/btofel/router:patched

# Restore original router image and delete test namespace
./04-cleanup.sh
```
