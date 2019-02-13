package dns

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
)

// Manager knows how to manage DNS zones only as pertains to routing.
type Manager interface {
	// Ensure will create or update record.
	Ensure(record *Record) error

	// Delete will delete record.
	Delete(record *Record) error
}

var _ Manager = &NoopManager{}

type NoopManager struct{}

func (_ *NoopManager) Ensure(record *Record) error { return nil }
func (_ *NoopManager) Delete(record *Record) error { return nil }

// Record represents a DNS record.
type Record struct {
	Zone configv1.DNSZone

	// Type is the DNS record type.
	Type RecordType

	// Alias is options for an ALIAS record.
	Alias *AliasRecord
}

// RecordType is a DNS record type.
type RecordType string

const (
	// ALIASRecord is a DNS ALIAS record.
	ALIASRecord RecordType = "ALIAS"
)

// AliasRecord is a DNS ALIAS record.
type AliasRecord struct {
	// Domain is the record name.
	Domain string

	// Target is the mapped destination name of Domain.
	Target string
}

func (r *AliasRecord) String() string {
	return fmt.Sprintf("%s -> %s", r.Domain, r.Target)
}
