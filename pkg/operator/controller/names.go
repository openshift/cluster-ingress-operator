package controller

import (
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// GlobalMachineSpecifiedConfigNamespace is the location for global
	// config.  In particular, the operator will put the configmap with the
	// CA certificate in this namespace.
	GlobalMachineSpecifiedConfigNamespace = "openshift-config-managed"

	// caCertSecretName is the name of the secret that holds the CA certificate
	// that the operator will use to create default certificates for
	// ingresscontrollers.
	caCertSecretName = "router-ca"

	// caCertConfigMapName is the name of the config map with the public key
	// for the CA certificate, which the operator publishes for other
	// operators to use.
	caCertConfigMapName = "router-ca"

	// routerCertsGlobalSecretName is the name of the secret with the
	// default certificates and their keys, which the operator publishes for
	// other operators to use.
	routerCertsGlobalSecretName = "router-certs"

	// controllerDeploymentLabel identifies a deployment as an ingress controller
	// deployment, and the value is the name of the owning ingress controller.
	controllerDeploymentLabel = "ingresscontroller.operator.openshift.io/deployment-ingresscontroller"
)

// RouterDeploymentName returns the namespaced name for the router deployment.
func RouterDeploymentName(ci *operatorv1.IngressController) types.NamespacedName {
	return types.NamespacedName{
		Namespace: "openshift-ingress",
		Name:      "router-" + ci.Name,
	}
}

// RouterPodDisruptionBudgetName returns the namespaced name for the router
// deployment's pod disruption budget.
func RouterPodDisruptionBudgetName(ci *operatorv1.IngressController) types.NamespacedName {
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

// RouterOperatorGeneratedDefaultCertificateSecretName returns the namespaced name for
// the operator-generated router default certificate secret.
func RouterOperatorGeneratedDefaultCertificateSecretName(ci *operatorv1.IngressController, namespace string) types.NamespacedName {
	return types.NamespacedName{
		Namespace: namespace,
		Name:      fmt.Sprintf("router-certs-%s", ci.Name),
	}
}

// RouterEffectiveDefaultCertificateSecretName returns the namespaced name for
// the in-use router default certificate secret.
func RouterEffectiveDefaultCertificateSecretName(ci *operatorv1.IngressController, namespace string) types.NamespacedName {
	if cert := ci.Spec.DefaultCertificate; cert != nil {
		return types.NamespacedName{Namespace: namespace, Name: cert.Name}
	}
	return RouterOperatorGeneratedDefaultCertificateSecretName(ci, namespace)
}

func IngressControllerDeploymentLabel(ic *operatorv1.IngressController) string {
	return ic.Name
}

func IngressControllerDeploymentPodSelector(ic *operatorv1.IngressController) *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{
			controllerDeploymentLabel: IngressControllerDeploymentLabel(ic),
		},
	}
}

func HealthzServiceName(ic *operatorv1.IngressController) types.NamespacedName {
	// TODO: remove hard-coded namespace
	return types.NamespacedName{Namespace: "openshift-ingress", Name: "router-healthz-" + ic.Name}
}

func InternalIngressControllerServiceName(ic *operatorv1.IngressController) types.NamespacedName {
	// TODO: remove hard-coded namespace
	return types.NamespacedName{Namespace: "openshift-ingress", Name: "router-internal-" + ic.Name}
}

func LoadBalancerServiceName(ci *operatorv1.IngressController) types.NamespacedName {
	// TODO: remove hard-coded namespace
	return types.NamespacedName{Namespace: "openshift-ingress", Name: "router-" + ci.Name}
}

func IngressControllerServiceMonitorName(ic *operatorv1.IngressController) types.NamespacedName {
	return types.NamespacedName{
		Namespace: "openshift-ingress",
		Name:      "router-" + ic.Name,
	}
}
