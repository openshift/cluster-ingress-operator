#!/usr/bin/env bash
# 01-setup-routes.sh
# Idempotent: always wipes and recreates the namespace, then creates N
# TLS secrets + routes with spec.tls.externalCertificate.
#
# Usage: ./01-setup-routes.sh [--count 200] [--namespace extcert-bench]

set -euo pipefail

COUNT=200
NAMESPACE=extcert-bench
APP_DOMAIN=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --count)     COUNT="$2";     shift 2 ;;
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --domain)    APP_DOMAIN="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

# ── Wipe existing namespace (idempotent) ──────────────────────────────────────
echo "==> Wiping namespace $NAMESPACE (if exists)"
oc delete ns "$NAMESPACE" --ignore-not-found --wait=true 2>/dev/null || true

echo "==> Creating namespace $NAMESPACE"
oc create ns "$NAMESPACE"

# ── Auto-detect cluster app domain ───────────────────────────────────────────
if [[ -z "$APP_DOMAIN" ]]; then
  APP_DOMAIN=$(oc get ingresses.config.openshift.io cluster \
    -o jsonpath='{.spec.domain}' 2>/dev/null || echo "apps.example.com")
  echo "==> Detected app domain: $APP_DOMAIN"
fi

# ── RBAC: custom Role granting the router SA access to secrets ───────────────
# NOTE: system:router clusterrole does NOT include secrets — must use custom Role.
echo "==> Creating RBAC for router secret access"
oc apply -n "$NAMESPACE" -f - <<RBAC_EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: router-secret-reader
  namespace: ${NAMESPACE}
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: router-secret-reader
  namespace: ${NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: router-secret-reader
subjects:
- kind: ServiceAccount
  name: router
  namespace: openshift-ingress
RBAC_EOF

# ── Generate a self-signed cert (reused for all routes) ──────────────────────
echo "==> Generating test TLS certificate"
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT
openssl req -x509 -newkey rsa:2048 \
  -keyout "$TMPDIR/tls.key" -out "$TMPDIR/tls.crt" \
  -days 365 -nodes \
  -subj "/CN=bench.${APP_DOMAIN}" \
  -addext "subjectAltName=DNS:bench.${APP_DOMAIN}" \
  2>/dev/null

# ── Create secrets, services, and routes ─────────────────────────────────────
echo "==> Creating $COUNT secrets and routes in namespace $NAMESPACE"
CREATED=0
for i in $(seq 1 "$COUNT"); do
  NAME="extcert-bench-${i}"

  oc create secret tls "$NAME" \
    --cert="$TMPDIR/tls.crt" \
    --key="$TMPDIR/tls.key" \
    -n "$NAMESPACE" 2>/dev/null

  oc create service clusterip "$NAME" --tcp=8080:8080 \
    -n "$NAMESPACE" 2>/dev/null

  oc apply -n "$NAMESPACE" -f - 2>/dev/null <<ROUTE_EOF
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: ${NAME}
spec:
  host: ${NAME}.${APP_DOMAIN}
  to:
    kind: Service
    name: ${NAME}
    weight: 100
  tls:
    termination: edge
    externalCertificate:
      name: ${NAME}
    insecureEdgeTerminationPolicy: Redirect
ROUTE_EOF

  CREATED=$((CREATED + 1))
  if ((CREATED % 10 == 0)); then
    echo "  ... created $CREATED/$COUNT routes"
  fi
done

echo ""
echo "==> Done: $COUNT external-cert routes in namespace $NAMESPACE"
echo "    Secrets: $(oc get secrets -n $NAMESPACE --no-headers | wc -l | tr -d ' ')"
echo "    Routes:  $(oc get routes  -n $NAMESPACE --no-headers | wc -l | tr -d ' ')"
