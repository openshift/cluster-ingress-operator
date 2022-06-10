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

	// AutoCreateDefaultIngressController indicates if the controller should create the default
	// controller if none exists
	AutoCreateDefaultIngressController bool

	Stop chan struct{}
}
