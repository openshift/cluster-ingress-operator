package client

import (
	"context"
)

type FakeDNSClient struct {
	fakeARM map[string]string
}

func NewFake(config Config) (*FakeDNSClient, error) {
	return &FakeDNSClient{fakeARM: map[string]string{}}, nil
}

func (c *FakeDNSClient) Put(ctx context.Context, zone Zone, arec ARecord) error {
	c.fakeARM[zone.ResourceGroup+zone.Name+arec.Name] = "PUT"
	return nil
}

func (c *FakeDNSClient) Delete(ctx context.Context, zone Zone, arec ARecord) error {
	c.fakeARM[zone.ResourceGroup+zone.Name+arec.Name] = "DELETE"
	return nil
}

func (c *FakeDNSClient) RecordedCall(rg, zone, rel string) (string, bool) {
	call, ok := c.fakeARM[rg+zone+rel]
	return call, ok
}
