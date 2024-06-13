#!/bin/bash
set -euo pipefail

: "${REPO:=}"
: "${MANIFESTS:=}"
: "${DOCKERFILE:=Dockerfile}"

if [ -z "$REPO" ]; then echo "REPO is required"; exit 1; fi
if [ -z "$MANIFESTS" ]; then echo "MANIFESTS is required"; exit 1; fi

TEMP_COMMIT="false"
test -z "$(git status --porcelain)" || TEMP_COMMIT="true"

if [[ "${TEMP_COMMIT}" == "true" ]]; then
  git add .
  git commit -m "Temporary" || true
fi

REV=$(git rev-parse --short HEAD)
TAG="${TAG:-$REV}"

if [[ -z "${DOCKER+1}" ]] && command -v buildah >& /dev/null; then
  buildah bud -t $REPO:$TAG -f "${DOCKERFILE}" .
  buildah push $REPO:$TAG docker://$REPO:$TAG
elif [[ -z "${DOCKER+1}" ]] && command -v podman >& /dev/null; then
  podman build -t $REPO:$TAG -f "${DOCKERFILE}" .
  podman push $REPO:$TAG
else
  docker build -t $REPO:$TAG -f "${DOCKERFILE}" .
  docker push $REPO:$TAG
fi

if [[ "${TEMP_COMMIT}" == "true" ]]; then
  git reset --soft HEAD~1
fi

if [[ -n "$GET_IMAGE" ]]; then
  IMAGE=$(oc get -n openshift-ingress-operator deployments/ingress-operator -o json | jq -r '.spec.template.spec.containers[0].env[] | select(.name=="IMAGE").value')
fi

cp -R manifests/* $MANIFESTS
if [ -z $IMAGE ]; then
  cat manifests/02-deployment.yaml | sed "s~openshift/origin-cluster-ingress-operator:latest~$REPO:$TAG~" > "$MANIFESTS/02-deployment.yaml"
else
  cat manifests/02-deployment.yaml | sed -e "s~openshift/origin-cluster-ingress-operator:latest~$REPO:$TAG~" -e "s~openshift/origin-haproxy-router:v4.0~$IMAGE~" > "$MANIFESTS/02-deployment.yaml"
fi
# To simulate CVO, ClusterOperator resource need to be created by the operator.
rm $MANIFESTS/03-cluster-operator.yaml

echo "Pushed $REPO:$TAG"
echo "Install manifests using:"
echo ""
echo "oc apply -f $MANIFESTS"
echo ""
echo "Alternatively, rollout just a new operator deployment with:"
echo ""
echo "oc apply -f $MANIFESTS/02-deployment.yaml"
