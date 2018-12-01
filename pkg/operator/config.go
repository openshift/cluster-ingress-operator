package operator

// Config is configuration for the operator and should include things like
// operated images, scheduling configuration, etc.
type Config struct {
	// RouterImage is the router image to manage.
	RouterImage string
	// DefaultIngressDomain is the value that the operator will use for
	// IngressDomain when creating a default ClusterIngress.
	DefaultIngressDomain string
}
