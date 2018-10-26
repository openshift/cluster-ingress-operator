#!/bin/bash
set -euo pipefail

REPO="${REPO:-}"
MANIFESTS="${MANIFESTS:-}"

if [ -z "$REPO" ]; then echo "REPO is required"; exit 1; fi
if [ -z "$MANIFESTS" ]; then echo "MANIFESTS is required"; exit 1; fi

TEMP_COMMIT="false"
test -z "$(git status --porcelain)" || TEMP_COMMIT="true"

if [[ "${TEMP_COMMIT}" == "true" ]]; then
  git add .
  git commit -m "Temporary" || true
fi

REV=$(git rev-parse --short HEAD)
if [[ -z "${DOCKER+1}" ]] && command -v buildah >& /dev/null; then
  buildah bud -t $REPO:$REV .
  buildah push $REPO:$REV docker://$REPO:$REV
else
  docker build -t $REPO:$REV .
  docker push $REPO:$REV
fi

if [[ "${TEMP_COMMIT}" == "true" ]]; then
  git reset --soft HEAD~1
fi

cp -R manifests/ $MANIFESTS
cat manifests/02-deployment.yaml | sed "s~openshift/origin-cluster-ingress-operator:latest~$REPO:$REV~" > "$MANIFESTS/02-deployment.yaml"

echo "Pushed $REPO:$REV"
echo "Install manifests using:"
echo ""
echo "oc apply -f $MANIFESTS"
