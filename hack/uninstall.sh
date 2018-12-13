#!/bin/bash
set -uo pipefail

OPERAND_ONLY="${OPERAND_ONLY:-}"

# Disable the CVO
oc scale --replicas 0 -n openshift-cluster-version deployments/cluster-version-operator

# Uninstall the cluster-ingress-operator
oc delete -n openshift-ingress-operator deployments/ingress-operator
oc patch -n openshift-ingress-operator clusteringresses/default --patch '{"metadata":{"finalizers": []}}' --type=merge
oc delete clusteroperator.config.openshift.io/openshift-ingress-operator
oc delete --force --grace-period=0 -n openshift-ingress-operator clusteringresses/default

if [ "$OPERAND_ONLY" != "true" ]; then
  oc delete namespaces/openshift-ingress-operator
fi
oc delete namespaces/openshift-ingress

if [ "$OPERAND_ONLY" != "true" ]; then
  oc delete clusterroles/openshift-ingress-operator
  oc delete clusterroles/openshift-ingress-router
  oc delete clusterrolebindings/openshift-ingress-operator
  oc delete clusterrolebindings/openshift-ingress-router
  oc delete customresourcedefinition.apiextensions.k8s.io/clusteringresses.ingress.openshift.io
fi
