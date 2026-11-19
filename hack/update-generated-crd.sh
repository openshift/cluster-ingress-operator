#!/bin/bash
set -euo pipefail

function install_crd {
  local SRC="$1"
  local DST="$2"
  if [[ -e "$SRC" ]]; then
    if [[ -e "$DST" ]]; then
      if ! diff -Naup "$SRC" "$DST"; then
        cp "$SRC" "$DST"
        echo "updated CRD: $SRC => $DST"
      else
        echo "skipped CRD that is already up to date: $DST"
      fi
    else
      cp "$SRC" "$DST"
      echo "updated CRD: $SRC => $DST"
    fi
  else
    if [[ -e "$DST" ]]; then
      rm "$DST"
      echo "removed CRD that not vendored: $DST"
    else
      echo "skipped CRD that is not vendored: $SRC"
    fi
  fi
}

# Can't rely on associative arrays for old Bash versions (e.g. OSX)

shopt -s extglob
# ingresses.operator.openshift.io, ingresscontrollers.operator.openshift.io and
# dnsrecords.ingress.operator.openshift.io are intentionally not copied here
# because openshift/api owns those CRDs in the release payload.
