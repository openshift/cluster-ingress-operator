#!/usr/bin/env bash
# 02-build-patched.sh
# Builds the router binary with the patched library-go (OCPBUGS-77056 fix)
# and pushes to quay.io/btofel/router:patched.
#
# The BASELINE is the existing cluster router — no build needed for that.
#
# Usage: ./02-build-patched.sh [--registry quay.io/btofel/router]

set -euo pipefail

REGISTRY="${ROUTER_IMAGE_REGISTRY:-quay.io/btofel/router}"
IMAGE="${REGISTRY}:patched"
ROUTER_DIR="/Users/btofel/workspace/router"
LIBRARYGO_DIR="/Users/btofel/workspace/library-go"
CONTAINER_TOOL="${CONTAINER_TOOL:-podman}"

while [[ $# -gt 0 ]]; do
  case $1 in
    --registry) REGISTRY="$2"; IMAGE="${REGISTRY}:patched"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

echo "==> Building patched router image: $IMAGE"
echo "    Router:    $ROUTER_DIR"
echo "    Library-go: $LIBRARYGO_DIR (fixed branch: $(git -C $LIBRARYGO_DIR branch --show-current))"

# Work in a temp copy to avoid dirtying the real workspace
WORKDIR=$(mktemp -d)
trap "rm -rf $WORKDIR" EXIT
cp -a "$ROUTER_DIR/." "$WORKDIR/"
cd "$WORKDIR"

# ── Apply local library-go replace directive ──────────────────────────────────
echo "==> Applying library-go replace directive in go.mod"
LIBGO_MOD=$(grep '^module ' "$LIBRARYGO_DIR/go.mod" | awk '{print $2}')
if grep -q "replace.*library-go" go.mod 2>/dev/null; then
  # Update existing replace line (macOS-compatible sed)
  sed -i '' "s|replace ${LIBGO_MOD} =>.*|replace ${LIBGO_MOD} => ${LIBRARYGO_DIR}|" go.mod
else
  printf '\nreplace %s => %s\n' "$LIBGO_MOD" "$LIBRARYGO_DIR" >> go.mod
fi

echo "==> Running go mod tidy & vendor..."
go mod tidy 2>&1 | tail -5 || true
go mod vendor 2>&1 | tail -5

# Verify the fix is present in vendor
if grep -q "secret cache not synced yet" vendor/github.com/openshift/library-go/pkg/secret/secret_monitor.go; then
  echo "==> Verified: patched secret_monitor.go in vendor"
else
  echo "ERROR: patched library-go not found in vendor" >&2; exit 1
fi

# ── Build binary ──────────────────────────────────────────────────────────────
echo "==> Compiling openshift-router (linux/amd64)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o openshift-router -ldflags '-w -s' ./cmd/openshift-router
echo "==> Binary: $(ls -lh openshift-router | awk '{print $5}')"

# ── Build container image ─────────────────────────────────────────────────────
BASE_IMAGE=$(oc get deployment router-default -n openshift-ingress -o jsonpath='{.spec.template.spec.containers[0].image}')
echo "==> Building container from $BASE_IMAGE"
IMGCTX=$(mktemp -d)
cp openshift-router "$IMGCTX/"
cat > "$IMGCTX/Dockerfile" <<DEOF
FROM ${BASE_IMAGE}
COPY openshift-router /usr/bin/openshift-router
DEOF
$CONTAINER_TOOL build -t "$IMAGE" "$IMGCTX"
rm -rf "$IMGCTX"

# ── Push ──────────────────────────────────────────────────────────────────────
echo "==> Pushing $IMAGE"
$CONTAINER_TOOL push "$IMAGE"
echo "==> Done: $IMAGE"
