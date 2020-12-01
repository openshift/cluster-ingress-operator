package dns

import (
	iov1 "github.com/openshift/api/operatoringress/v1"

	configv1 "github.com/openshift/api/config/v1"
)

// Provider knows how to manage DNS zones only as pertains to routing.
type Provider interface {
	// Ensure will create or update record.
	Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error

	// Delete will delete record.
	Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error

	// Replace will replace the record
	Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error
}

var _ Provider = &FakeProvider{}

type FakeProvider struct{}

func (_ *FakeProvider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error  { return nil }
func (_ *FakeProvider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error  { return nil }
func (_ *FakeProvider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error { return nil }
