package aws

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/client/metadata"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go/service/route53"

	configv1 "github.com/openshift/api/config/v1"

	iov1 "github.com/openshift/api/operatoringress/v1"

	"k8s.io/apimachinery/pkg/util/sets"
	"testing"
)

func buildFakeResponses(record iov1.DNSRecord, dnsZone configv1.DNSZone) []interface{} {
	var elbDescriptions []*elb.LoadBalancerDescription
	var responses []interface{}

	for _, target := range record.Spec.Targets {
		elbDescription := &elb.LoadBalancerDescription{
			DNSName:                   aws.String(target),
			CanonicalHostedZoneNameID: aws.String(dnsZone.ID),
		}
		elbDescriptions = append(elbDescriptions, elbDescription)
	}

	for i := 0; i < len(record.Spec.Targets); i++ {
		elbResponse := &elb.DescribeLoadBalancersOutput{
			LoadBalancerDescriptions: elbDescriptions,
		}

		responses = append(responses, elbResponse)
	}

	route53Response := &route53.ChangeResourceRecordSetsInput{}
	responses = append(responses, route53Response)

	return responses
}

func fakeAWSProvider(record iov1.DNSRecord, dnsZone configv1.DNSZone) *Provider {
	resps := buildFakeResponses(record, dnsZone)

	reqNum := 0
	sess := session.Must(session.NewSession(&aws.Config{
		DisableSSL: aws.Bool(true),
	}))
	sess.Handlers.Send.Clear()
	sess.Handlers.Unmarshal.Clear()
	sess.Handlers.UnmarshalMeta.Clear()
	sess.Handlers.ValidateResponse.Clear()
	sess.Handlers.Unmarshal.PushBack(func(r *request.Request) {
		r.Data = resps[reqNum]
		reqNum++
	})

	config := &aws.Config{
		MaxRetries: aws.Int(1),
		Region:     aws.String("ap-southeast-2"),
	}

	route53Config := sess.ClientConfig("route53", config)
	route53 := &route53.Route53{
		Client: client.New(
			*route53Config.Config,
			metadata.ClientInfo{
				ServiceName:   "route53",
				ServiceID:     "Route 53",
				SigningName:   "",
				SigningRegion: route53Config.SigningRegion,
				Endpoint:      route53Config.Endpoint + "/route53",
				APIVersion:    "2013-04-01",
				JSONVersion:   "1.1",
				TargetPrefix:  "MockServer",
			},
			route53Config.Handlers,
		),
	}

	elbConfig := sess.ClientConfig("elasticloadbalancing", config)
	elb := &elb.ELB{
		Client: client.New(
			*elbConfig.Config,
			metadata.ClientInfo{
				ServiceName:   "elasticloadbalancing",
				ServiceID:     "Elastic Load Balancing",
				SigningName:   "",
				SigningRegion: elbConfig.SigningRegion,
				Endpoint:      elbConfig.Endpoint + "/elb",
				APIVersion:    "2012-06-01",
				JSONVersion:   "1.1",
				TargetPrefix:  "MockServer",
			},
			elbConfig.Handlers,
		),
	}

	taggingConfig := sess.ClientConfig("resourcegroupstaggingapi", config)
	tagging := &resourcegroupstaggingapi.ResourceGroupsTaggingAPI{
		Client: client.New(
			*taggingConfig.Config,
			metadata.ClientInfo{
				ServiceName:   "tagging",
				ServiceID:     "Resource Groups Tagging API",
				SigningName:   "",
				SigningRegion: taggingConfig.SigningRegion,
				Endpoint:      taggingConfig.Endpoint + "/tagging",
				APIVersion:    "2017-01-26",
				JSONVersion:   "1.1",
				TargetPrefix:  "ResourceGroupsTaggingAPI_20170126",
			},
			taggingConfig.Handlers,
		),
	}

	return &Provider{
		elb:            elb,
		route53:        route53,
		tags:           tagging,
		idsToTags:      map[string]map[string]string{},
		lbZones:        map[string]string{},
		updatedRecords: sets.NewString(),
	}
}

func TestEnsureAWSDNS(t *testing.T) {
	record := iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName:    "subdomain.dnszone.io.",
			RecordType: iov1.CNAMERecordType,
			Targets:    []string{"afcfa1e69205711e99e9906f0636dbcb-2044172706.ap-southeast-2.elb.amazonaws.com"},
			RecordTTL:  120,
		},
	}

	dnsZone := configv1.DNSZone{
		ID: "Z1GM3OXH4ZPM65",
	}

	mgr := fakeAWSProvider(record, dnsZone)

	if err := mgr.Ensure(&record, dnsZone); err != nil {
		t.Fatalf("unexpected error from Ensure: %v", err)
	}
}

func TestEnsureAWSDNSWithMultipleTargets(t *testing.T) {
	record := iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName:    "subdomain.dnszone.io.",
			RecordType: iov1.CNAMERecordType,
			Targets:    []string{"afcfa1e69205711e99e9906f0636dbcb-2044172707.ap-southeast-2.elb.amazonaws.com", "afcfa1e69205711e99e9906f0636dbcb-2044172708.ap-southeast-2.elb.amazonaws.com"},
			RecordTTL:  120,
		},
	}

	dnsZone := configv1.DNSZone{
		ID: "Z1GM3OXH4ZPM66",
	}

	mgr := fakeAWSProvider(record, dnsZone)

	if err := mgr.Ensure(&record, dnsZone); err != nil {
		t.Fatalf("unexpected error from Ensure: %v", err)
	}
}

func TestDeleteAWSDNS(t *testing.T) {
	record := iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName:    "subdomain.dnszone.io.",
			RecordType: iov1.CNAMERecordType,
			Targets:    []string{"afcfa1e69205711e99e9906f0636dbcb-2044172706.ap-southeast-2.elb.amazonaws.com"},
			RecordTTL:  120,
		},
	}

	dnsZone := configv1.DNSZone{
		ID: "Z1GM3OXH4ZPM65",
	}

	mgr := fakeAWSProvider(record, dnsZone)

	if err := mgr.Delete(&record, dnsZone); err != nil {
		t.Fatalf("unexpected error from Delete: %v", err)
	}
}

func TestDeleteAWSDNSWithMultipleTargets(t *testing.T) {
	record := iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName:    "subdomain.dnszone.io.",
			RecordType: iov1.CNAMERecordType,
			Targets:    []string{"afcfa1e69205711e99e9906f0636dbcb-2044172707.ap-southeast-2.elb.amazonaws.com", "afcfa1e69205711e99e9906f0636dbcb-2044172708.ap-southeast-2.elb.amazonaws.com"},
			RecordTTL:  120,
		},
	}

	dnsZone := configv1.DNSZone{
		ID: "Z1GM3OXH4ZPM66",
	}

	mgr := fakeAWSProvider(record, dnsZone)

	if err := mgr.Delete(&record, dnsZone); err != nil {
		t.Fatalf("unexpected error from Delete: %v", err)
	}
}
