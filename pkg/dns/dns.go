package dns

// Manager knows how to manage DNS zones only as pertains to routing.
type Manager interface {
	// EnsureAlias creates or updates an A record.
	EnsureAlias(domain, target string) error
}

var _ Manager = &NoopManager{}

type NoopManager struct{}

func (_ *NoopManager) EnsureAlias(domain, target string) error { return nil }
