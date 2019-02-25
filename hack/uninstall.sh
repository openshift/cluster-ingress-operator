#!/bin/bash
set -uo pipefail

WHAT="${WHAT:-managed}"

# Disable the CVO
oc scale --replicas 0 -n openshift-cluster-version deployments/cluster-version-operator

# Uninstall the cluster-ingress-operator
oc delete -n openshift-ingress-operator deployments/ingress-operator
oc patch -n openshift-ingress-operator clusteringresses/default --patch '{"metadata":{"finalizers": []}}' --type=merge
# TODO: this leaves DNS dangling
oc patch -n openshift-ingress services/router-default --patch '{"metadata":{"finalizers": []}}' --type=merge
oc delete clusteroperator.config.openshift.io/ingress
oc delete --force --grace-period=0 -n openshift-ingress-operator clusteringresses/default

if [ "$WHAT" == "all" ]; then
  oc delete namespaces/openshift-ingress-operator
else
  oc delete -n openshift-ingress-operator secrets/router-ca
  oc delete -n openshift-ingress-operator events --all
fi

oc delete namespaces/openshift-ingress

if [ "$WHAT" == "all" ]; then
  oc delete clusterroles/openshift-ingress-operator
  oc delete clusterroles/openshift-ingress-router
  oc delete clusterroles/router-monitoring
  oc delete clusterrolebindings/openshift-ingress-operator
  oc delete clusterrolebindings/openshift-ingress-router
  oc delete clusterrolebindings/router-monitoring
  oc delete customresourcedefinition.apiextensions.k8s.io/clusteringresses.ingress.openshift.io
fi

oc delete -n openshift-config-managed configmaps/router-ca
oc delete -n openshift-config-managed secrets/router-certs
