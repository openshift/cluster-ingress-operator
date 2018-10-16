#!/bin/bash
set -euo pipefail

# Disable the CVO
oc patch -n openshift-cluster-version daemonsets/cluster-version-operator --patch '{"spec": {"template": {"spec": {"nodeSelector": {"node-role.kubernetes.io/fake": ""}}}}}' || true

# Uninstall tectonic ingress
oc delete namespaces/openshift-ingress || true

# Uninstall cluster-dns-operator
oc delete --force --grace-period 0 -n openshift-cluster-ingress-operator clusteringresses/default || true
oc delete namespaces/openshift-cluster-ingress-operator || true
oc delete namespaces/openshift-cluster-ingress-router || true
oc delete clusterroles/cluster-ingress-operator:operator || true
oc delete clusterroles/cluster-ingress:router || true
oc delete clusterrolebindings/cluster-ingress-operator:operator || true
oc delete clusterrolebindings/cluster-ingress:router || true
oc delete customresourcedefinition.apiextensions.k8s.io/clusteringresses.ingress.openshift.io || true
