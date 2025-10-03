#!/bin/bash

# Clean up:
#   oc -n openshift-marketplace delete catalogsource custom-istio-catalog
#   oc delete ImageDigestMirrorSet stage-registry
#   oc delete ImageTagMirrorSet stage-registry
#   oc delete ImageContentSourcePolicy brew-registry

set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "jq not found"; exit 1; }

CLUSTER_CONTEXT=$(oc config current-context)
KONFLUX_SERVER="api.stone-prod-p02.hjvn.p1.openshiftapps.com"
BREW_IMAGE_REGISTRY="brew.registry.redhat.io"
STAGE_IMAGE_REGISTRY="registry.stage.redhat.io"
BREW_MIRROR_NEEDED="false"

echo "> Login to konflux cluster to access the index image"
oc login --token="$TOKEN" --server=https://${KONFLUX_SERVER}:6443

echo "> Get the index image"
INDEX_IMAGE=$(oc get releases -l appstudio.openshift.io/application=ossm-fbc-next --sort-by=.metadata.creationTimestamp -o name -n service-mesh-tenant | tail -1 | xargs oc -n service-mesh-tenant get -ojson | jq -r '.status.artifacts.index_image.index_image')
INDEX_IMAGE=${INDEX_IMAGE##*/}
if [[ -z "${INDEX_IMAGE}" ]]; then
    echo "> No index image found"
    exit 2
fi
echo "> Index image tag: $INDEX_IMAGE"

echo "> Switch back to kube cluster"
oc config use-context "$CLUSTER_CONTEXT"

# flexy-install clusters have pull secrets set up by default.
# clusterbot and CI cluster don't have pull secrets for the brew
# and stage registries. We have to add them explicitly, for this
# the registry service account credentials will be used.
echo "> Check ${BREW_IMAGE_REGISTRY} pull secret"
oc get secret/pull-secret -n openshift-config -o json | jq -r '.data.".dockerconfigjson"' | base64 -d > /tmp/authfile
if ! grep -q "${BREW_IMAGE_REGISTRY}" /tmp/authfile; then
    echo "> Add ${BREW_IMAGE_REGISTRY} pull secret"
    podman login --authfile /tmp/authfile --username "${REGISTRY_SA_USERNAME}" --password "${REGISTRY_SA_PASSWORD}" "${BREW_IMAGE_REGISTRY}"
    oc set data secret/pull-secret -n openshift-config --from-file=.dockerconfigjson=/tmp/authfile
    BREW_MIRROR_NEEDED="true"
else
    BREW_MIRROR_NEEDED="true"
fi

echo "> Check ${STAGE_IMAGE_REGISTRY} pull secret"
oc get secret/pull-secret -n openshift-config -o json | jq -r '.data.".dockerconfigjson"' | base64 -d > /tmp/authfile
if ! grep -q "${STAGE_IMAGE_REGISTRY}" /tmp/authfile; then
    echo "> Add ${STAGE_IMAGE_REGISTRY} pull secret"
    podman login --authfile /tmp/authfile --username "${REGISTRY_SA_USERNAME}" --password "${REGISTRY_SA_PASSWORD}" "${STAGE_IMAGE_REGISTRY}"
    oc set data secret/pull-secret -n openshift-config --from-file=.dockerconfigjson=/tmp/authfile
fi

# All images in OSSM FBC use the stage image registry.
# We have to add the mirroring from registry.redhat.io.
echo "> Apply mirror sets for ${STAGE_IMAGE_REGISTRY}"
oc apply -f -<<EOF
apiVersion: config.openshift.io/v1
kind: ImageTagMirrorSet
metadata:
    name: stage-registry
spec:
    imageTagMirrors:
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh
          source: registry.redhat.io/openshift-service-mesh
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh-tech-preview
          source: registry.redhat.io/openshift-service-mesh-tech-preview
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh-dev-preview-beta
          source: registry.redhat.io/openshift-service-mesh-dev-preview-beta
---
apiVersion: config.openshift.io/v1
kind: ImageDigestMirrorSet
metadata:
    name: stage-registry
spec:
    imageDigestMirrors:
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh
          source: registry.redhat.io/openshift-service-mesh
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh-tech-preview
          source: registry.redhat.io/openshift-service-mesh-tech-preview
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh-dev-preview-beta
          source: registry.redhat.io/openshift-service-mesh-dev-preview-beta
EOF

if [[ "${BREW_MIRROR_NEEDED}" == "true" ]]; then
    # OSSM index index is from registry-proxy.engineering.redhat.com.
    # We have to add mirroring from it to the brew image registry.
    echo "> Apply mirror sets for ${BREW_IMAGE_REGISTRY}"
    oc apply -f -<<EOF
apiVersion: operator.openshift.io/v1alpha1
kind: ImageContentSourcePolicy
metadata:
  name: brew-registry
spec:
  repositoryDigestMirrors:
  - mirrors:
    - brew.registry.redhat.io
    source: registry-proxy.engineering.redhat.com
EOF
fi

echo "> Apply custom istio catalog source"
oc apply -f -<<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: custom-istio-catalog
  namespace: openshift-marketplace
spec:
  displayName: RH Custom Istio catalog
  image: brew.registry.redhat.io/rh-osbs/$INDEX_IMAGE
  sourceType: grpc
EOF

MAX_TRIES=60
TRIES=0

while (( TRIES < MAX_TRIES )); do
    CATALOG_STATE=$(oc get catalogsource -n openshift-marketplace custom-istio-catalog -ojson | jq -r '.status.connectionState.lastObservedState')
    if [[ "$CATALOG_STATE" = "READY" ]]; then
        echo "> Istio custom catalog source is ready!"
        break
    fi
    TRIES=$((TRIES+1))
    echo "(${TRIES}/${MAX_TRIES}) Istio catalog source is not ready, retrying..."
    sleep 2
done

if (( TRIES >= MAX_TRIES )); then
    echo "> Exceeded max tries waiting for catalog source to become ready"
    exit 3
fi

CUSTOM_CATALOG_SOURCE=custom-istio-catalog
TRIES=0
while (( TRIES < MAX_TRIES )); do
    OSSM_VERSION=$(oc get packagemanifests -n openshift-marketplace -o jsonpath="{range .items[?(@.metadata.labels.catalog=='custom-istio-catalog')]} {.status.channels[*].currentCSV}{\"\n\"}{end}" | grep servicemeshoperator3 | awk '{print $NF}')
    if [[ "$OSSM_VERSION" != "" ]]; then
        echo "> OSSM version found!"
        break
    fi
    TRIES=$((TRIES+1))
    echo "(${TRIES}/${MAX_TRIES}) OSSM version is not available, retrying..."
    sleep 2
done
if (( TRIES >= MAX_TRIES )); then
    echo "> OSSM version is not found in $CUSTOM_CATALOG_SOURCE"
    exit 4
fi
echo "> OSSM version from the custom catalog source: $OSSM_VERSION"
CUSTOM_OSSM_VERSION=$OSSM_VERSION CUSTOM_CATALOG_SOURCE=$CUSTOM_CATALOG_SOURCE TEST=TestGatewayAPI make test-e2e
