#!/usr/bin/env bash
# 03-run-benchmark.sh
# Restarts the router, waits for it to become Ready, and measures:
#   - Secret loading span (from router logs: first→last informer sync)
#   - Pod→Ready wall-clock time
#
# Two modes:
#   --existing   Benchmark the cluster's current router (baseline, no image change)
#   --image IMG  Deploy a custom image first, then benchmark (patched)
#
# Usage:
#   ./03-run-benchmark.sh --existing               # baseline
#   ./03-run-benchmark.sh --image quay.io/btofel/router:patched
#   ./03-run-benchmark.sh --restore                # put original image back

set -euo pipefail

MODE=""
IMAGE=""
RESTORE=false
INGRESS_NS=openshift-ingress
IC_NS=openshift-ingress-operator
TIMEOUT=600
LABEL="ingresscontroller.operator.openshift.io/deployment-ingresscontroller=default"

while [[ $# -gt 0 ]]; do
  case $1 in
    --existing) MODE=existing; shift ;;
    --image)    MODE=patched; IMAGE="$2"; shift 2 ;;
    --restore)  RESTORE=true; shift ;;
    --timeout)  TIMEOUT="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

# ── Restore ───────────────────────────────────────────────────────────────────
if [[ "$RESTORE" == "true" ]]; then
  echo "==> Restoring original router image"
  oc scale --replicas=1 deployment/ingress-operator -n "$IC_NS"
  
  # Wait a moment for the operator to wake up and trigger a rollout
  sleep 5
  echo "==> Removing CVO override for Ingress Operator"
  oc patch clusterversion version --type=json -p='[{"op":"remove","path":"/spec/overrides"}]' 2>/dev/null || true

  echo "==> Waiting for router to recover..."
  oc rollout status deployment/router-default -n "$INGRESS_NS" --timeout="${TIMEOUT}s"
  echo "==> Cleanup done."
  exit 0
fi

if [[ -z "$MODE" ]]; then
  echo "ERROR: specify --existing or --image <img>"
  exit 1
fi

TAG="$MODE"

echo ""
echo "══════════════════════════════════════════════════════════════"
echo "  OCPBUGS-77056 Benchmark — $TAG"
echo "══════════════════════════════════════════════════════════════"

# ── Optionally swap the image ─────────────────────────────────────────────────
if [[ "$MODE" == "patched" ]]; then
  echo "==> Pausing Ingress Operator to prevent image revert..."
  oc patch clusterversion version --type=merge -p '{"spec":{"overrides":[{"kind":"Deployment","group":"apps","name":"ingress-operator","namespace":"openshift-ingress-operator","unmanaged":true}]}}'
  oc scale --replicas=0 deployment/ingress-operator -n "$IC_NS"
  sleep 3 # Give it a moment to terminate
  
  echo "==> Deploying image: $IMAGE"
  oc set image deployment/router-default "router=${IMAGE}" -n "$INGRESS_NS"
  
  echo "==> Forcing imagePullPolicy to Always"
  oc patch deployment/router-default -n "$INGRESS_NS" --type=strategic -p '{"spec":{"template":{"spec":{"containers":[{"name":"router","imagePullPolicy":"Always"}]}}}}'
fi

# ── Restart and time it ───────────────────────────────────────────────────────
echo "==> Restarting router-default"
oc rollout restart deployment/router-default -n "$INGRESS_NS"

READY_START=$(date +%s)

# Wait for the new pod to appear
echo -n "==> Waiting for new pod to be created: "
NEW_POD=""
for i in $(seq 1 90); do
  sleep 2
  echo -n "."
  # Get the most-recently-created running/pending pod
  NEW_POD=$(oc get pods -n "$INGRESS_NS" -l "$LABEL" \
    --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{.items[-1:].metadata.name}' 2>/dev/null || true)
  if [[ -n "$NEW_POD" ]]; then
    POD_PHASE=$(oc get pod "$NEW_POD" -n "$INGRESS_NS" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [[ "$POD_PHASE" == "Running" || "$POD_PHASE" == "Pending" ]]; then
      echo " Found: $NEW_POD (Phase: $POD_PHASE)"
      break
    fi
  fi
done

if [[ -z "$NEW_POD" || "$POD_PHASE" == "" ]]; then
   echo " ERROR: Timed out waiting for a new pod to appear!"
   exit 1
fi

echo "==> Tracking pod: $NEW_POD"

# Wait for rollout ready
echo "==> Waiting for deployment rollout to finish (timeout ${TIMEOUT}s)..."
if ! oc rollout status deployment/router-default -n "$INGRESS_NS" --timeout="${TIMEOUT}s"; then
  echo "ERROR: router did not become ready in ${TIMEOUT}s"
  oc logs "$NEW_POD" -n "$INGRESS_NS" 2>/dev/null | tail -20
  exit 1
fi

READY_END=$(date +%s)
READY_ELAPSED=$((READY_END - READY_START))

# ── Parse log-based timing ────────────────────────────────────────────────────
sleep 3  # let logs flush
LOG=$(oc logs "$NEW_POD" -n "$INGRESS_NS" 2>/dev/null || true)

FIRST_TS=$(echo "$LOG" | grep -E 'starting informer|Starting informer' | head -1 | awk '{print $1, $2}' || true)
LAST_TS=$(echo "$LOG"  | grep -E 'secret informer started|secret handler added' | tail -1 | awk '{print $1, $2}' || true)
SECRET_COUNT=$(echo "$LOG" | grep -c 'secret informer started' || true)

SECRET_LOAD_SECS="N/A"
if [[ -n "$FIRST_TS" && -n "$LAST_TS" ]]; then
  SECRET_LOAD_SECS=$(python3 -c "
from datetime import datetime
import sys
try:
    ts1 = '${FIRST_TS}'  # e.g. I0302 15:26:54.629672
    ts2 = '${LAST_TS}'
    if not ts1 or not ts2: sys.exit(0)
    
    y = datetime.now().year
    # remove 'I', 'W', 'E' prefix from month
    d1 = f\"{y}{ts1[1:5]} {ts1.split()[1]}\"
    d2 = f\"{y}{ts2[1:5]} {ts2.split()[1]}\"
    
    a = datetime.strptime(d1, '%Y%m%d %H:%M:%S.%f')
    b = datetime.strptime(d2, '%Y%m%d %H:%M:%S.%f')
    print(f'{(b-a).total_seconds():.2f}')
except Exception as e:
    print('N/A')
" 2>/dev/null || echo "N/A")
fi

# ── Print results ─────────────────────────────────────────────────────────────
echo ""
echo "══════════════════════════════════════════════════════════════"
echo "  RESULTS — $TAG"
echo "══════════════════════════════════════════════════════════════"
printf "  %-30s %s\n" "Pod:"                    "$NEW_POD"
printf "  %-30s %s\n" "Image:"                  "${IMAGE:-<cluster default>}"
printf "  %-30s %s\n" "Secrets loaded:"         "$SECRET_COUNT"
printf "  %-30s %ss\n" "Secret loading span:"   "$SECRET_LOAD_SECS"
printf "  %-30s %ss\n" "Pod → Ready (wall):"    "$READY_ELAPSED"
echo "══════════════════════════════════════════════════════════════"

# Save result file
RESULT_FILE="benchmark-results-${TAG}-$(date +%Y%m%d-%H%M%S).txt"
cat > "$RESULT_FILE" <<EOF
mode=${TAG}
image=${IMAGE:-cluster-default}
pod=${NEW_POD}
secret_count=${SECRET_COUNT}
secret_load_seconds=${SECRET_LOAD_SECS}
pod_to_ready_seconds=${READY_ELAPSED}
timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
echo "==> Results saved: $RESULT_FILE"
