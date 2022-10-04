package common

import (
	"testing"

	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"
	"github.com/stretchr/testify/assert"
)

func TestGetServiceEndpointURL(t *testing.T) {
	var customServiceEndpoints []ServiceEndpoint

	// with empty customServiceEndpoint
	endpoint, _ := GetServiceEndpointURL(customServiceEndpoints, DNSCustomEndpointName)
	assert.Equal(t, dnssvcsv1.DefaultServiceURL, endpoint)

	// without cis endpoint in customServiceEndpoint
	customServiceEndpoints = append(customServiceEndpoints, ServiceEndpoint{
		Name: "rc",
		URL:  "https://test.resource-controller.cloud.ibm.com",
	})
	endpoint, _ = GetServiceEndpointURL(customServiceEndpoints, DNSCustomEndpointName)
	assert.Equal(t, dnssvcsv1.DefaultServiceURL, endpoint)

	// with valid dns customServiceEndpoint
	customServiceEndpoints = append(customServiceEndpoints, ServiceEndpoint{
		Name: DNSCustomEndpointName,
		URL:  "https://api.dns-svcs.test.cloud.ibm.com/",
	})
	endpoint, _ = GetServiceEndpointURL(customServiceEndpoints, DNSCustomEndpointName)
	assert.Equal(t, "https://api.dns-svcs.test.cloud.ibm.com/", endpoint)

	// without cis endpoint in customServiceEndpoint
	endpoint, _ = GetServiceEndpointURL(customServiceEndpoints, CISCustomEndpointName)
	assert.Equal(t, dnsrecordsv1.DefaultServiceURL, endpoint)

	// with valid cis customServiceEndpoint
	customServiceEndpoints = append(customServiceEndpoints, ServiceEndpoint{
		Name: CISCustomEndpointName,
		URL:  "https://api.cis.test.cloud.ibm.com/",
	})
	endpoint, _ = GetServiceEndpointURL(customServiceEndpoints, CISCustomEndpointName)
	assert.Equal(t, "https://api.cis.test.cloud.ibm.com/", endpoint)

	// without iam endpoint in customServiceEndpoint
	endpoint, _ = GetServiceEndpointURL(customServiceEndpoints, IAMCustomEndpointName)
	assert.Equal(t, defaultIamTokenServerEndpoint, endpoint)

	// with valid iam customServiceEndpoint
	customServiceEndpoints = append(customServiceEndpoints, ServiceEndpoint{
		Name: IAMCustomEndpointName,
		URL:  "https://test.iam.cloud.ibm.com",
	})
	endpoint, _ = GetServiceEndpointURL(customServiceEndpoints, IAMCustomEndpointName)
	assert.Equal(t, "https://test.iam.cloud.ibm.com", endpoint)

}
