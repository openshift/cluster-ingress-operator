#!/bin/bash
set -uo pipefail

# Disable the CVO
oc patch -n openshift-cluster-version daemonsets/cluster-version-operator --patch '{"spec": {"template": {"spec": {"nodeSelector": {"node-role.kubernetes.io/fake": ""}}}}}'

# Uninstall tectonic ingress
oc delete namespaces/openshift-ingress

# Uninstall the cluster-ingress-operator
oc delete --force --grace-period 0 -n openshift-cluster-ingress-operator clusteringresses/default
oc delete namespaces/openshift-cluster-ingress-operator
oc delete namespaces/openshift-cluster-ingress-router
oc delete clusterroles/cluster-ingress-operator:operator
oc delete clusterroles/cluster-ingress:router
oc delete clusterrolebindings/cluster-ingress-operator:operator
oc delete clusterrolebindings/cluster-ingress:router
oc delete customresourcedefinition.apiextensions.k8s.io/clusteringresses.ingress.openshift.io
