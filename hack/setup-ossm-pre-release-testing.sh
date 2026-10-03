#!/bin/bash

set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "jq not found"; exit 1; }

CLUSTER_CONTEXT=$(oc config current-context)
BREW_IMAGE_REGISTRY="brew.registry.redhat.io"
STAGE_IMAGE_REGISTRY="registry.stage.redhat.io"
BREW_MIRROR_NEEDED="false"

echo "> Switch back to kube cluster"
oc config use-context "$CLUSTER_CONTEXT"

# flexy-install clusters have pull secrets set up by default.
# clusterbot and CI cluster don't have pull secrets for the brew
# and stage registries. We have to add them explicitly from
# the variables provided by the script runner.
echo "> Check ${BREW_IMAGE_REGISTRY} pull secret"
oc get secret/pull-secret -n openshift-config -o json | jq -r '.data.".dockerconfigjson"' | base64 -d > /tmp/authfile
if ! grep -q "${BREW_IMAGE_REGISTRY}" /tmp/authfile; then
    echo "> Add ${BREW_IMAGE_REGISTRY} pull secret"
    echo "${AUTHBREW}" > /tmp/authbrew
    jq -s '.[0] * .[1]' /tmp/authfile /tmp/authbrew > /tmp/auth
    oc set data secret/pull-secret -n openshift-config --from-file=.dockerconfigjson=/tmp/auth
    BREW_MIRROR_NEEDED="true"
fi

echo "> Check ${STAGE_IMAGE_REGISTRY} pull secret"
oc get secret/pull-secret -n openshift-config -o json | jq -r '.data.".dockerconfigjson"' | base64 -d > /tmp/authfile
if ! grep -q "${STAGE_IMAGE_REGISTRY}" /tmp/authfile; then
    echo "> Add ${STAGE_IMAGE_REGISTRY} pull secret"
    echo "${AUTHSTAGE}" > /tmp/authstage
    jq -s '.[0] * .[1]' /tmp/authfile /tmp/authstage > /tmp/auth
    oc set data secret/pull-secret -n openshift-config --from-file=.dockerconfigjson=/tmp/auth
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
    # OSSM index image is from registry-proxy.engineering.redhat.com.
    # We have to add an Mirror Sets to mirror the brew image registry.
    echo "> Apply Image Content Source Policy for ${BREW_IMAGE_REGISTRY}"
    oc apply -f -<<EOF
apiVersion: config.openshift.io/v1
kind: ImageTagMirrorSet
metadata:
  name: brew-registry
spec:
  imageTagMirrors:
    - mirrors:
      - brew.registry.redhat.io
      source: registry-proxy.engineering.redhat.com
---
apiVersion: config.openshift.io/v1
kind: ImageDigestMirrorSet
metadata:
  name: brew-registry
spec:
  imageDigestMirrors:
    - mirrors:
      - brew.registry.redhat.io
      source: registry-proxy.engineering.redhat.com
EOF
fi

echo "> Pre-release secrets successfully applied"
