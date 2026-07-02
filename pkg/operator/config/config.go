package config

import (
	operatorv1 "github.com/openshift/api/operator/v1"
)

// Config is configuration for the operator and should include things like
// operated images, scheduling configuration, etc.
type Config struct {
	// OperatorReleaseVersion is the current version of operator.
	OperatorReleaseVersion string

	// Namespace is the operator namespace.
	Namespace string

	// IngressControllerImage is the ingress controller image to manage.
	IngressControllerImage string

	// HAProxyImages is the map of HAProxy images to be managed.
	HAProxyImages map[operatorv1.HAProxyVersion]string

	// DefaultHAProxyVersion defines the default HAProxy version.
	DefaultHAProxyVersion operatorv1.HAProxyVersion

	// CanaryImage is the ingress operator image, which runs a canary command.
	CanaryImage string

	// GatewayAPIOperatorCatalog is the catalog source to use for the Gateway API implementation.
	GatewayAPIOperatorCatalog string

	// GatewayAPIOperatorChannel is the release channel of the Gateway API implementation to install.
	GatewayAPIOperatorChannel string

	// GatewayAPIOperatorVersion is the name and release of the Gateway API implementation to install.
	GatewayAPIOperatorVersion string

	// IstioVersion is the version of Istio to install.
	IstioVersion string

	Stop chan struct{}
}
