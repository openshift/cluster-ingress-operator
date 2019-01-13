package config

import (
	configv1 "github.com/openshift/api/config/v1"
)

// Config is configuration for the operator and should include things like
// operated images, scheduling configuration, etc.
type Config struct {
	// Namespace is the operator namespace.
	Namespace string
	// RouterImage is the router image to manage.
	RouterImage string
	// DefaultIngressDomain is the value that the operator will use for
	// IngressDomain when creating a default ClusterIngress.
	DefaultIngressDomain string
	// Platform is the underlying infrastructure provider for the cluster.
	Platform configv1.PlatformType
}
