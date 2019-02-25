package controller

import (
	"fmt"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	"k8s.io/apimachinery/pkg/types"
)

const (
	// GlobalMachineSpecifiedConfigNamespace is the location for global
	// config.  In particular, the operator will put the configmap with the
	// CA certificate in this namespace.
	GlobalMachineSpecifiedConfigNamespace = "openshift-config-managed"

	// caCertSecretName is the name of the secret that holds the CA certificate
	// that the operator will use to create default certificates for
	// clusteringresses.
	caCertSecretName = "router-ca"

	// caCertConfigMapName is the name of the config map with the public key
	// for the CA certificate, which the operator publishes for other
	// operators to use.
	caCertConfigMapName = "router-ca"

	// routerCertsGlobalSecretName is the name of the secret with the
	// default certificates and their keys, which the operator publishes for
	// other operators to use.
	routerCertsGlobalSecretName = "router-certs"
)

// RouterDeploymentName returns the namespaced name for the router deployment.
func RouterDeploymentName(ci *ingressv1alpha1.ClusterIngress) types.NamespacedName {
	return types.NamespacedName{
		Namespace: "openshift-ingress",
		Name:      "router-" + ci.Name,
	}
}

// RouterCASecretName returns the namespaced name for the router CA secret.
func RouterCASecretName(operatorNamespace string) types.NamespacedName {
	return types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      caCertSecretName,
	}
}

// RouterCAConfigMapName returns the namespaced name for the router CA configmap.
func RouterCAConfigMapName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: GlobalMachineSpecifiedConfigNamespace,
		Name:      caCertConfigMapName,
	}
}

// RouterCertsGlobalSecretName returns the namespaced name for the router certs
// secret.
func RouterCertsGlobalSecretName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: GlobalMachineSpecifiedConfigNamespace,
		Name:      routerCertsGlobalSecretName,
	}
}

// RouterDefaultCertificateSecretName returns the namespaced name for the router
// default certificate secret.
func RouterDefaultCertificateSecretName(ci *ingressv1alpha1.ClusterIngress, namespace string) types.NamespacedName {
	return types.NamespacedName{
		Namespace: namespace,
		Name:      fmt.Sprintf("router-certs-%s", ci.Name),
	}
}
