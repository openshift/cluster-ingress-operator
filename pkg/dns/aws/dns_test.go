package aws

import (
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/route53"
)

type candidateZone struct {
	public bool
	name   string
}

func (cz candidateZone) ToHostedZone() *route53.HostedZone {
	return &route53.HostedZone{Name: aws.String(cz.name), Config: &route53.HostedZoneConfig{PrivateZone: aws.Bool(!cz.public)}}
}

func Test_findNearestPublicParentZone(t *testing.T) {
	tests := []struct {
		domain string
		last   candidateZone
		zones  []candidateZone

		exp string
	}{{
		domain: "team-1.staging.20190205.dev.openshift.com.",

		exp: "",
	}, {
		domain: "team-1.staging.20190205.dev.openshift.com.",
		zones: []candidateZone{{
			name: "team-1.staging.20190205.dev.openshift.com.",
		}},

		exp: "",
	}, {
		domain: "team-1.staging.20190205.dev.openshift.com.",
		zones: []candidateZone{{
			public: true,
			name:   "team-1.staging.20190205.dev.openshift.com.",
		}},

		exp: "team-1.staging.20190205.dev.openshift.com.",
	}, {
		domain: "team-1.staging.20190205.dev.openshift.com.",
		zones: []candidateZone{{
			name: "team-1.staging.20190205.dev.openshift.com.",
		}, {
			public: true,
			name:   "dev.openshift.com.",
		}},

		exp: "dev.openshift.com.",
	}, {
		domain: "team-1.staging.20190205.dev.openshift.com.",
		zones: []candidateZone{{
			name: "team-1.staging.20190205.dev.openshift.com.",
		}, {
			public: true,
			name:   "ging.20190205.dev.openshift.com.",
		}},

		exp: "",
	}, {
		domain: "team-1.staging.20190205.dev.openshift.com.",
		zones: []candidateZone{{
			name: "team-1.staging.20190205.dev.openshift.com.",
		}, {
			public: true,
			name:   "staging.20190205.dev.openshift.com.",
		}},

		exp: "staging.20190205.dev.openshift.com.",
	}, {
		domain: "team-1.staging.20190205.dev.openshift.com.",
		zones: []candidateZone{{
			name: "team-1.staging.20190205.dev.openshift.com.",
		}, {
			public: true,
			name:   "dev.openshift.com.",
		}, {
			public: true,
			name:   "staging.20190205.dev.openshift.com.",
		}},

		exp: "staging.20190205.dev.openshift.com.",
	}, {
		domain: "team-1.staging.20190205.dev.openshift.com.",
		zones: []candidateZone{{
			name: "team-1.staging.20190205.dev.openshift.com.",
		}, {
			public: true,
			name:   "dev.openshift.com.",
		}, {
			name: "staging.20190205.dev.openshift.com.",
		}},

		exp: "dev.openshift.com.",
	}, {
		domain: "team-1.staging.20190205.dev.openshift.com.",
		last: candidateZone{
			public: true,
			name:   "staging.20190205.dev.openshift.com.",
		},
		zones: []candidateZone{{
			name: "team-1.staging.20190205.dev.openshift.com.",
		}, {
			public: true,
			name:   "dev.openshift.com.",
		}, {
			public: true,
			name:   "20190205.dev.openshift.com.",
		}},

		exp: "staging.20190205.dev.openshift.com.",
	}, {
		domain: "team-1.staging.20190205.dev.openshift.com.",
		last: candidateZone{
			public: true,
			name:   "20190205.dev.openshift.com.",
		},
		zones: []candidateZone{{
			name: "team-1.staging.20190205.dev.openshift.com.",
		}, {
			public: true,
			name:   "dev.openshift.com.",
		}, {
			public: true,
			name:   "staging.20190205.dev.openshift.com.",
		}},

		exp: "staging.20190205.dev.openshift.com.",
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("#%d", idx), func(t *testing.T) {
			var zones []*route53.HostedZone
			for _, z := range test.zones {
				zones = append(zones, z.ToHostedZone())
			}
			var last *route53.HostedZone
			if test.last.name != "" {
				last = test.last.ToHostedZone()
			}
			got := findNearestPublicParentZone(test.domain, zones, last)
			if test.exp != aws.StringValue(got.Name) {
				t.Fatalf("exp: %s got: %s", test.exp, aws.StringValue(got.Name))
			}
		})
	}
}
