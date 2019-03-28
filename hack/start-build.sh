#!/bin/bash

set -euo pipefail

oc -n openshift-ingress-operator start-build ingress-operator \
   ${V+--follow} --wait

if [[ -n "${DEPLOY+1}" ]]
then
    oc -n openshift-ingress-operator patch deploy/ingress-operator \
       --type=strategic --patch='
{
  "spec": {
    "template": {
      "spec": {
        "containers": [
          {
            "name": "ingress-operator",
            "env": [
              {
                "name": "IMAGE",
                "value": "image-registry.openshift-image-registry.svc:5000/openshift-ingress-operator/ingress-operator:latest"
              }
            ],
            "image": "image-registry.openshift-image-registry.svc:5000/openshift-ingress-operator/ingress-operator:latest",
            "imagePullPolicy": "Always"
          }
        ]
      }
    }
  }
}
'
fi
