package config

// Config is configuration for the operator and should include things like
// operated images, scheduling configuration, etc.
type Config struct {
	// OperatorReleaseVersion is the current version of operator.
	OperatorReleaseVersion string

	// Namespace is the operator namespace.
	Namespace string

	// IngressControllerImage is the ingress controller image to manage.
	IngressControllerImage string

	// TrustedCABundle is the fully qualified path of the trusted CA certificate
	// bundle that is watched.
	TrustedCABundle string
}
