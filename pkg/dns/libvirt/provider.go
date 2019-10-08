package libvirt

import (
	"encoding/xml"
	"fmt"
	"net"
	"strings"

	libvirt "github.com/libvirt/libvirt-go"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/cluster-ingress-operator/pkg/api/v1"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
)

var (
	// _ dns.Provider = &Provider{}
	log = logf.Logger.WithName("entrypoint")
)

type Provider struct {
	// config is required input.
	config Config
}

type Config struct {
	Cluster string
	Domain  string
	Url     string
}

type Host struct {
	XMLName   xml.Name `xml:"host"`
	Ip        string   `xml:"ip,attr"`
	DnsRecord string   `xml:"hostname"`
}

type action = libvirt.NetworkUpdateCommand

const (
	upsertAction action = 3
	deleteAction action = 2
)

// New creates (but does not start) a new operator from configuration.
func New(config Config) (*Provider, error) {
	provider := &Provider{
		config: config,
	}
	return provider, nil
}

func (p *Provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return p.change(record, zone, upsertAction)
}

func (p *Provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return p.change(record, zone, deleteAction)
}

// Change methods will perform an action on a record.
func (p *Provider) change(record *iov1.DNSRecord, zone configv1.DNSZone, action action) error {
	// Lookup apiserver's ip to connect with
	ips, err := net.LookupIP(p.config.Url)
	if err != nil || len(ips) == 0 {
		log.Error(err, "failed to lookup network IPs")
		return nil
	}
	log.Info("master domain lookup", "IPs", ips)

	// Create a new connections to qemu
	qemuUrl := fmt.Sprintf("qemu+ssh://%s/system", ips[0])
	log.Info("connecting to qemu...", "qemu url", qemuUrl)
	conn, err := libvirt.NewConnect(qemuUrl)
	if err != nil {
		log.Error(err, "failed to connect qemu")
	}

	// List all domains
	domains, err := conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_ACTIVE)
	if err != nil {
		log.Error(err, "failed to get domains")
	}
	for _, domain := range domains {
		name, err := domain.GetName()
		if err != nil {
			log.Error(err, "failed to get domain name")
		}
		// Find the worker domains in cluster, because Routers run on workers
		if strings.Index(name, p.config.Cluster) == -1 || strings.Index(name, "master") != -1 {
			continue
		}
		// Lookup the worker domain's ip
		interfaceInfo, err := domain.ListAllInterfaceAddresses(0)
		domainIp := interfaceInfo[0].Addrs[0].Addr
		log.Info("found the domain in use", name, domainIp)

		// List all networks
		networks, err := conn.ListAllNetworks(2)
		if err != nil {
			log.Error(err, "failed to get networks")
		}

		// Find the correct network and update a DNS record
		// TODO: There are too much nest here, the correct network can be find by the prefix of domain's name
		for _, network := range networks {
			name, err := network.GetName()
			if err != nil {
				log.Error(err, "failed to get network name")
			}
			// Find the network in use whose name contains cluster ID
			if strings.Index(name, p.config.Cluster) == -1 {
				continue
			}
			log.Info("found network in use", "network", name)
			// Generate a XML for DNS host
			v := &Host{Ip: domainIp, DnsRecord: record.Spec.DNSName}
			output, err := xml.MarshalIndent(v, "  ", "    ")
			if err != nil {
				log.Error(err, "failed to generate a network XML")
			}
			log.Info("DNS record is updating", "XML", string(output))
			// Update the network
			// TODO: If the same ip is in the section already, it'll fail with a error:
			// Requested operation is not valid: there is already at least one DNS HOST
			// record with a matching field in network test1-bqhsq')"}
			err = network.Update(action, 10, -1, string(output), 0)
			if err != nil {
				log.Error(err, "failed to update network")
			}
			log.Info("DNS record is updated", "network info", action)
			network.Free()
		}
	}
	conn.Close()
	return nil
}
