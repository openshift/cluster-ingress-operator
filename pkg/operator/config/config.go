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

	// CanaryImage is the ingress operator image, which runs a canary command.
	CanaryImage string

	// DisableDefaultIngressController indicates if the default ingress controller
	// should be created automatically if it doesn't already exist
	DisableDefaultIngressController bool

	Stop chan struct{}
}
