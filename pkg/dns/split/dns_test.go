package split_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	splitdns "github.com/openshift/cluster-ingress-operator/pkg/dns/split"
)

// TestSplitDNSProvider verifies that the split DNS provider dispatches to the
// public or private provider as appropriate for the DNS zone.
func TestSplitDNSProvider(t *testing.T) {
	var (
		// ch is a channel that is used in the fake public and private
		// providers to record which one is called.
		ch = make(chan string, 6)
		// getResult reads and returns one item from ch, or returns the
		// empty string if ch is empty.
		getResult = func() string {
			var result string
			select {
			case result = <-ch:
			default:
			}
			return result
		}
		// publicProvider is a fake dns.Provider for the public zone.
		publicProvider = newFakeProvider("public", ch)
		// privateProvider is a fake dns.Provider for the private zone.
		privateProvider = newFakeProvider("private", ch)
		// publicZoneWithID is a public zone that is defined by ID.
		publicZoneWithID = configv1.DNSZone{ID: "public_zone"}
		// privateZoneWithID is a private zone that is defined by ID.
		privateZoneWithID = configv1.DNSZone{ID: "private_zone"}
		// publicZoneWithTags is a public zone that is defined by tags.
		publicZoneWithTags = configv1.DNSZone{Tags: map[string]string{"zone": "public"}}
		// privateZoneWithID is a private zone that is defined by tags.
		privateZoneWithTags = configv1.DNSZone{Tags: map[string]string{"zone": "private"}}
	)
	testCases := []struct {
		name          string
		publicZone    configv1.DNSZone
		privateZone   configv1.DNSZone
		publishToZone configv1.DNSZone
		expect        string
	}{
		{
			name:          "publish to public zone specified by id",
			publicZone:    publicZoneWithID,
			privateZone:   privateZoneWithID,
			publishToZone: publicZoneWithID,
			expect:        "public",
		},
		{
			name:          "publish to private zone specified by id",
			publicZone:    publicZoneWithID,
			privateZone:   privateZoneWithID,
			publishToZone: privateZoneWithID,
			expect:        "private",
		},
		{
			name:          "publish to public zone specified by tags",
			publicZone:    publicZoneWithTags,
			privateZone:   privateZoneWithID,
			publishToZone: publicZoneWithTags,
			expect:        "public",
		},
		{
			name:          "publish to private zone specified by tags",
			publicZone:    publicZoneWithTags,
			privateZone:   privateZoneWithTags,
			publishToZone: privateZoneWithTags,
			expect:        "private",
		},
		{
			name:          "publish to other zone should fall back to the public zone",
			publicZone:    publicZoneWithID,
			privateZone:   privateZoneWithID,
			publishToZone: configv1.DNSZone{ID: "other_zone"},
			expect:        "public",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := splitdns.NewProvider(publicProvider, privateProvider, &tc.privateZone)
			assert.NoError(t, provider.Ensure(&iov1.DNSRecord{}, tc.publishToZone))
			assert.Equal(t, tc.expect, getResult())
			assert.NoError(t, provider.Replace(&iov1.DNSRecord{}, tc.publishToZone))
			assert.Equal(t, tc.expect, getResult())
			assert.NoError(t, provider.Delete(&iov1.DNSRecord{}, tc.publishToZone))
			assert.Equal(t, tc.expect, getResult())
			assert.Empty(t, ch)
		})
	}

}

var _ dns.Provider = &fakeProvider{}

type fakeProvider struct {
	name     string
	recorder chan string
}

func (p *fakeProvider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	p.recorder <- p.name
	return nil
}
func (p *fakeProvider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	p.recorder <- p.name
	return nil
}
func (p *fakeProvider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	p.recorder <- p.name
	return nil
}

// newFakeProvider returns a new dns.Provider that records invocations.
func newFakeProvider(name string, ch chan string) dns.Provider {
	return &fakeProvider{name, ch}
}
