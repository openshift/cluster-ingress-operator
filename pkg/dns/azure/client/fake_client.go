package client

import (
	"context"
	"fmt"
)

type FakeDNSClient struct {
	fakeARM map[string]string
	// addresses records the addresses written by the most recent Put for
	// each record set, so tests can assert on record set contents and not
	// just on which calls were made.
	addresses map[string][]string
}

func NewFake(config Config) (*FakeDNSClient, error) {
	return &FakeDNSClient{
		fakeARM:   map[string]string{},
		addresses: map[string][]string{},
	}, nil
}

// key identifies a record set. The record type is part of the key because the
// A and AAAA record sets for one name are distinct Azure resources.
func key(rg, zone, rel string, recordType RecordType) string {
	return fmt.Sprintf("%s/%s/%s/%s", rg, zone, rel, recordType)
}

func (c *FakeDNSClient) Put(ctx context.Context, zone Zone, rs RecordSet, metadata map[string]*string) error {
	k := key(zone.ResourceGroup, zone.Name, rs.Name, rs.Type)
	c.fakeARM[k] = "PUT"
	c.addresses[k] = append([]string(nil), rs.Addresses...)
	return nil
}

func (c *FakeDNSClient) Delete(ctx context.Context, zone Zone, rs RecordSet) error {
	k := key(zone.ResourceGroup, zone.Name, rs.Name, rs.Type)
	c.fakeARM[k] = "DELETE"
	delete(c.addresses, k)
	return nil
}

// RecordedCall returns the last call made against the given record set.
func (c *FakeDNSClient) RecordedCall(rg, zone, rel string, recordType RecordType) (string, bool) {
	call, ok := c.fakeARM[key(rg, zone, rel, recordType)]
	return call, ok
}

// RecordedAddresses returns the addresses written by the last Put against the
// given record set.
func (c *FakeDNSClient) RecordedAddresses(rg, zone, rel string, recordType RecordType) ([]string, bool) {
	addrs, ok := c.addresses[key(rg, zone, rel, recordType)]
	return addrs, ok
}
