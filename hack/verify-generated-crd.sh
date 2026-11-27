#!/bin/bash
set -euo pipefail

# openshift/api owns the CRDs listed in payloadCRDNames
# (hack/verify-generated-crd/main.go) in the release payload.  Keep them out
# of this component's payload to avoid applying two independently ordered
# copies of the same CRD.
if ingress_crd_manifests=$(GO111MODULE=on GOFLAGS=-mod=vendor go run ./hack/verify-generated-crd manifests); then
  if [[ -z "$ingress_crd_manifests" ]]; then
    exit 0
  fi
  printf '%s\n' "$ingress_crd_manifests"
  echo "CRDs listed in payloadCRDNames (hack/verify-generated-crd/main.go) must only ship from openshift/api"
  exit 1
else
  scan_status=$?
  echo "failed to scan manifests for CRDs that must only ship from openshift/api" >&2
  exit "$scan_status"
fi
