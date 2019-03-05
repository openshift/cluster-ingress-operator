package config

// Config is configuration for the operator and should include things like
// operated images, scheduling configuration, etc.
type Config struct {
	// OperatorReleaseVersion is the current version of operator.
	OperatorReleaseVersion string

	// Namespace is the operator namespace.
	Namespace string

	// RouterImage is the router image to manage.
	RouterImage string
}
