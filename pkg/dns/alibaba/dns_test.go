package alibaba

import (
	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/stretchr/testify/assert"
	"testing"
)

type fakeService struct {
	// records for id+rr to target
	records map[string]string
	// lastAction records the last action performed
	// can be "add", "update" or "delete"
	lastAction string
}

func (p *fakeService) Add(id, rr, recordType, target string, ttl int64) error {
	p.records[id+rr] = target
	p.lastAction = "add"
	return nil
}

func (p *fakeService) Update(id, rr, recordType, target string, ttl int64) error {
	p.records[id+rr] = target
	p.lastAction = "update"
	return nil
}

func (p *fakeService) Delete(id, rr, target string) error {
	delete(p.records, id+rr)
	p.lastAction = "delete"
	return nil
}

// getLastAction returns lastAction and sets it to empty
func (p *fakeService) getLastAction() string {
	action := p.lastAction
	p.lastAction = ""
	return action
}

func newFakeService() *fakeService {
	return &fakeService{
		records: make(map[string]string),
	}
}

func newFakeProvider(public, private Service) dns.Provider {
	return &provider{
		services: map[zoneType]Service{
			zoneTypePublicZone:  public,
			zoneTypePrivateZone: private,
		},
	}
}

func TestGetRR(t *testing.T) {
	cases := []struct {
		dnsName    string
		domainName string
		excepted   string
	}{
		{
			dnsName:    "test.example.com.",
			domainName: "example.com",
			excepted:   "test",
		},
		{
			dnsName:    "test.subdomain.example.com.",
			domainName: "example.com",
			excepted:   "test.subdomain",
		},
		{
			dnsName:    "test.subdomain.example.com.",
			domainName: "subdomain.example.com",
			excepted:   "test",
		},
		{
			dnsName:    "without.domain.",
			domainName: "example.com",
			excepted:   "without.domain",
		},
	}

	for _, c := range cases {
		rr := getRR(c.dnsName, c.domainName)
		assert.Equal(t, c.excepted, rr)
	}
}

func TestParseZone(t *testing.T) {
	cases := []struct {
		id       string
		tags     map[string]string
		zoneType zoneType
		error    bool
	}{
		{
			id: "public.example.com",
			tags: map[string]string{
				"type": "public",
			},
			error:    false,
			zoneType: zoneTypePublicZone,
		},
		{
			id: "private.example.com",
			tags: map[string]string{
				"type": "private",
			},
			error:    false,
			zoneType: zoneTypePrivateZone,
		},
		{
			id:    "error.example.com",
			tags:  map[string]string{},
			error: true,
		},
	}

	p := &provider{}
	for _, c := range cases {
		info, err := p.parseZone(configv1.DNSZone{
			ID:   c.id,
			Tags: c.tags,
		})

		if c.error {
			assert.Error(t, err)
			continue
		}

		assert.NoError(t, err)
		assert.Equal(t, c.id, info.ID)
		assert.Equal(t, c.zoneType, info.Type)
	}
}

func TestProvider(t *testing.T) {
	servicePublic := newFakeService()
	servicePrivate := newFakeService()
	provider := newFakeProvider(servicePublic, servicePrivate)

	record := &iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName:    "test.example.com.",
			Targets:    []string{"123.123.123.123"},
			RecordType: "A",
			RecordTTL:  60,
		},
	}

	dnsZonePublic := configv1.DNSZone{
		ID: "example.com",
		Tags: map[string]string{
			"type": "public",
		},
	}

	dnsZonePrivate := configv1.DNSZone{
		ID: "example.com",
		Tags: map[string]string{
			"type": "private",
		},
	}

	assert.Equal(t, "", servicePublic.getLastAction())
	assert.Equal(t, "", servicePublic.getLastAction())

	// test public zone ensure
	assert.NoError(t, provider.Ensure(record, dnsZonePublic))
	assert.Equal(t, "add", servicePublic.getLastAction())
	assert.Equal(t, "", servicePrivate.getLastAction())

	// test private zone replace
	assert.NoError(t, provider.Replace(record, dnsZonePrivate))
	assert.Equal(t, "", servicePublic.getLastAction())
	assert.Equal(t, "update", servicePrivate.getLastAction())

	// test public zone delete
	assert.NoError(t, provider.Delete(record, dnsZonePublic))
	assert.Equal(t, "delete", servicePublic.getLastAction())
	assert.Equal(t, "", servicePrivate.getLastAction())

	// test zone type unknown, should return error
	dnsZoneUnknown := configv1.DNSZone{
		ID: "example.com",
		Tags: map[string]string{
			"type": "unknown",
		},
	}
	assert.Error(t, provider.Ensure(record, dnsZoneUnknown))

	// test zone without type, should return error
	dnsZoneNoType := configv1.DNSZone{
		ID:   "example.com",
		Tags: map[string]string{},
	}
	assert.Error(t, provider.Ensure(record, dnsZoneNoType))
}
