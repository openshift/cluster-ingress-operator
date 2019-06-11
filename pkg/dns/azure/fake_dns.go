package azure

import (
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure/client"
)

func NewFakeProvider(config Config, client client.DNSClient) (dns.Provider, error) {
	return &provider{config: config, client: client}, nil
}
