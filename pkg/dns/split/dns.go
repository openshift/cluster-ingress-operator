package split

import (
	"reflect"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	configv1 "github.com/openshift/api/config/v1"
)

var (
	_   dns.Provider = &Provider{}
	log              = logf.Logger.WithName("dns")
)

// Provider is a dns.Provider that wraps two other providers.  The first
// provider is used for public hosted zones, and the second provider is used for
// private hosted zones.
type Provider struct {
	private, public dns.Provider
	privateZone     *configv1.DNSZone
}

// NewProvider returns a new Provider that wraps the provided wrappers, using
// the first for the public zone and the second for the private zone.
func NewProvider(public, private dns.Provider, privateZone *configv1.DNSZone) *Provider {
	return &Provider{
		public:      public,
		private:     private,
		privateZone: privateZone,
	}
}

// Ensure calls the Ensure method of one of the wrapped DNS providers.
func (p *Provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if reflect.DeepEqual(zone, *p.privateZone) {
		return p.private.Ensure(record, zone)
	}
	return p.public.Ensure(record, zone)
}

// Delete calls the Delete method of one of the wrapped DNS providers.
func (p *Provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if reflect.DeepEqual(zone, *p.privateZone) {
		return p.private.Delete(record, zone)
	}
	return p.public.Delete(record, zone)
}

// Replace calls the Replace method of one of the wrapped DNS providers.
func (p *Provider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if reflect.DeepEqual(zone, *p.privateZone) {
		return p.private.Replace(record, zone)
	}
	return p.public.Replace(record, zone)
}
