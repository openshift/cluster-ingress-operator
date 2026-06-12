#!/usr/bin/env bash
# 00-run-all.sh
# Full OCPBUGS-77056 benchmark orchestrator.
#
# Phases (each starts with a wipe for idempotency):
#   0. quay login + preflight checks
#   1. Wipe + create test routes (extcert-bench namespace)
#   2. BASELINE: restart existing cluster router, measure startup
#   3. PATCHED: deploy pre-built patched image, restart, measure startup
#   4. Print comparison table
#   5. Cleanup prompt
#
# !! DO NOT COMMIT with QUAY_PASSWORD set !!
# Usage: ./00-run-all.sh [--count 200] [--namespace extcert-bench]

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── Config ────────────────────────────────────────────────────────────────────
ROUTE_COUNT="${ROUTE_COUNT:-200}"
NAMESPACE="${BENCH_NAMESPACE:-extcert-bench}"
REGISTRY="${ROUTER_IMAGE_REGISTRY:-quay.io/btofel/router}"
PATCHED_IMAGE="${REGISTRY}:patched"
QUAY_USER="${QUAY_USER:-btofel}"
QUAY_PASSWORD="${QUAY_PASSWORD:-UZF9gy4jrRp0l+/gh0zqK6HJx39MEZ3k57m3CDs2I8BrnGkmAttI63KiXFyn7/XV}"

while [[ $# -gt 0 ]]; do
  case $1 in
    --count)      ROUTE_COUNT="$2"; shift 2 ;;
    --namespace)  NAMESPACE="$2";   shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

banner() {
  echo ""
  echo "══════════════════════════════════════════════════════════════"
  printf "  %s\n" "$*"
  echo "══════════════════════════════════════════════════════════════"
}

read_result() { grep "^${2}=" "${1}" 2>/dev/null | cut -d= -f2 || echo "N/A"; }

# ── Phase 0: Preflight ────────────────────────────────────────────────────────
banner "PHASE 0: Preflight"

echo "==> Checking oc connectivity"
oc whoami || { echo "ERROR: not logged into a cluster"; exit 1; }
oc get nodes --no-headers | head -3

echo "==> Logging into quay.io"
podman login -u="${QUAY_USER}" -p="${QUAY_PASSWORD}" quay.io

echo "==> Config: count=${ROUTE_COUNT} namespace=${NAMESPACE} image=${PATCHED_IMAGE}"

# ── Phase 1: Wipe + setup routes ─────────────────────────────────────────────
banner "PHASE 1: Route setup (wipe + create ${ROUTE_COUNT} external-cert routes)"
bash "${SCRIPT_DIR}/01-setup-routes.sh" \
  --count "$ROUTE_COUNT" \
  --namespace "$NAMESPACE"

# ── Phase 2: Baseline benchmark (existing cluster router) ────────────────────
banner "PHASE 2: BASELINE benchmark (existing cluster router — bug present)"
# Ensure we are actually evaluating the baseline buggy image by restoring it
bash "${SCRIPT_DIR}/03-run-benchmark.sh" --restore
bash "${SCRIPT_DIR}/03-run-benchmark.sh" --existing
BASELINE_FILE=$(ls -t "${SCRIPT_DIR}"/benchmark-results-existing-*.txt 2>/dev/null | head -1 || \
                ls -t benchmark-results-existing-*.txt 2>/dev/null | head -1 || echo "")

# ── Phase 3: Patched benchmark ───────────────────────────────────────────────
banner "PHASE 3: PATCHED benchmark (fixed library-go)"
# Wipe and recreate routes so the router sees the same load as the baseline run
bash "${SCRIPT_DIR}/01-setup-routes.sh" \
  --count "$ROUTE_COUNT" \
  --namespace "$NAMESPACE"
bash "${SCRIPT_DIR}/03-run-benchmark.sh" --image "$PATCHED_IMAGE"
PATCHED_FILE=$(ls -t "${SCRIPT_DIR}"/benchmark-results-patched-*.txt 2>/dev/null | head -1 || \
               ls -t benchmark-results-patched-*.txt 2>/dev/null | head -1 || echo "")

# ── Phase 4: Comparison ───────────────────────────────────────────────────────
banner "PHASE 4: Results comparison"
if [[ -f "${BASELINE_FILE:-}" && -f "${PATCHED_FILE:-}" ]]; then
  B_SECRETS=$(read_result "$BASELINE_FILE" secret_count)
  B_LOAD=$(read_result    "$BASELINE_FILE" secret_load_seconds)
  B_READY=$(read_result   "$BASELINE_FILE" pod_to_ready_seconds)
  P_SECRETS=$(read_result "$PATCHED_FILE"  secret_count)
  P_LOAD=$(read_result    "$PATCHED_FILE"  secret_load_seconds)
  P_READY=$(read_result   "$PATCHED_FILE"  pod_to_ready_seconds)

  printf "\n  %-32s %-16s %-16s\n" "Metric" "BASELINE (bug)" "PATCHED (fix)"
  printf "  %-32s %-16s %-16s\n"   "--------------------------------" "----------------" "----------------"
  printf "  %-32s %-16s %-16s\n"   "Secrets loaded"        "$B_SECRETS"  "$P_SECRETS"
  printf "  %-32s %-16s %-16s\n"   "Secret loading span (s)"  "$B_LOAD"  "$P_LOAD"
  printf "  %-32s %-16s %-16s\n"   "Pod → Ready wall time (s)" "$B_READY" "$P_READY"
  echo ""

  python3 -c "
b, p = '${B_LOAD}', '${P_LOAD}'
try:
    ratio = float(b)/float(p)
    print(f'  Speedup (secret loading): {ratio:.1f}x faster with the fix')
    print(f'  {float(b):.0f}s  →  {float(p):.0f}s')
except: pass
" 2>/dev/null || true

  echo ""
  echo "  Baseline file : $BASELINE_FILE"
  echo "  Patched file  : $PATCHED_FILE"
else
  echo "  (result files not found; check benchmark-results-*.txt)"
fi

# ── Phase 5: Cleanup ─────────────────────────────────────────────────────────
banner "PHASE 5: Cleanup"
read -rp "  Clean up test namespace and restore original router? [y/N] " CONFIRM
if [[ "${CONFIRM,,}" == "y" ]]; then
  bash "${SCRIPT_DIR}/04-cleanup.sh" --namespace "$NAMESPACE"
else
  echo "  Skipped. Run: ./04-cleanup.sh --namespace $NAMESPACE"
fi
