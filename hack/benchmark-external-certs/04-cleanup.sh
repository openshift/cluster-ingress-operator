#!/usr/bin/env bash
# 04-cleanup.sh
# Removes test namespace and restores the original router image.
# Usage: ./04-cleanup.sh [--namespace extcert-bench] [--skip-router-restore]

set -euo pipefail

NAMESPACE=extcert-bench
SKIP_RESTORE=false
INGRESS_NS=openshift-ingress
IC_NS=openshift-ingress-operator

while [[ $# -gt 0 ]]; do
  case $1 in
    --namespace)           NAMESPACE="$2"; shift 2 ;;
    --skip-router-restore) SKIP_RESTORE=true; shift ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

echo "==> Deleting namespace $NAMESPACE"
oc delete ns "$NAMESPACE" --ignore-not-found --wait=true 2>/dev/null || true

if [[ "$SKIP_RESTORE" == "false" ]]; then
  echo "==> Restoring original router image"
  oc scale --replicas=1 deployment/ingress-operator -n "$IC_NS"
  
  echo "==> Removing CVO override for Ingress Operator"
  oc patch clusterversion version --type=json -p='[{"op":"remove","path":"/spec/overrides"}]' 2>/dev/null || true

  echo "==> Waiting for router to recover..."
  sleep 5
  oc rollout status deployment/router-default -n "$INGRESS_NS" --timeout=600s || true
  
  echo "==> Cleanup done."
fi
