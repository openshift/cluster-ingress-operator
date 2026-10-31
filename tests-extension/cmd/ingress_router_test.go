package main

import (
	"strings"
	"testing"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	"github.com/openshift-eng/openshift-tests-extension/pkg/flags"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
)

// Verify real Ginkgo discovery and CEL registration without running cluster specs.
func TestIngressRouterRegistration(t *testing.T) {
	all, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		t.Fatal(err)
	}
	appendSuiteNames(all)
	configureIngressRouterSpecs(all)
	specs, err := all.Filter([]string{ingressRouterSelector})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 11 {
		t.Fatalf("discovered %d migrated ingress/router cases, want 11", len(specs))
	}
	for _, spec := range specs {
		if !strings.HasPrefix(spec.Name, "[sig-network-edge][OTP][Level0] Network_Edge Component_Router ") {
			t.Errorf("incorrect title shape: %s", spec.Name)
		}
		for _, token := range []string{"Author:", "Critical", "High-", "Medium", "OCP-", "[Jira:", "[Suite:"} {
			if strings.Contains(spec.Name, token) {
				t.Errorf("title contains registration-only token %q: %s", token, spec.Name)
			}
		}
		if spec.Labels.Has("IngressRouterQE") || spec.Labels.Has("OCP-41117") {
			t.Errorf("unexpected label on %s: %v", spec.Name, spec.Labels)
		}
		if spec.Lifecycle != et.LifecycleInforming || spec.OriginalName == "" {
			t.Errorf("missing informing lifecycle or original identity: %s", spec.Name)
		}
		if strings.Contains(spec.Name, "[Serial]") || strings.Contains(spec.Name, "[Disruptive]") {
			if len(spec.Resources.Isolation.Conflict) == 0 || len(spec.Resources.Isolation.Taint) == 0 {
				t.Errorf("missing isolation: %s", spec.Name)
			}
		}
		for _, location := range spec.CodeLocations {
			if strings.Contains(location, "/tests-extension/") {
				t.Errorf("source location was not normalized: %s", location)
			}
		}
	}

	ext := e.NewExtension("openshift", "payload", "cluster-ingress-operator")
	registerIngressRouterSuites(ext)
	ext.AddSpecs(all)
	for name, want := range map[string]int{
		"ingress-router/conformance/parallel": 8,
		"ingress-router/conformance/serial":   2,
		"ingress-router/disruptive":           1,
		"ingress-router/non-disruptive":       10,
		"ingress-router/all":                  11,
	} {
		t.Run(name, func(t *testing.T) {
			suite, err := ext.GetSuite(name)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := all.Filter(suite.Qualifiers)
			if err != nil {
				t.Fatal(err)
			}
			if len(selected) != want {
				t.Errorf("selected %d cases, want %d", len(selected), want)
			}
			if strings.Contains(name, "/conformance/") {
				for _, spec := range selected {
					if spec.Labels.Has("OCP-56228") {
						t.Error("disruptive case entered conformance")
					}
					withoutLevel := *spec
					withoutLevel.Name = strings.ReplaceAll(spec.Name, "[Level0]", "")
					gated, err := (et.ExtensionTestSpecs{&withoutLevel}).Filter(suite.Qualifiers)
					if err != nil || len(gated) != 0 {
						t.Errorf("Level0 gate was not enforced: %v", err)
					}
				}
			}
			if name == "ingress-router/disruptive" && len(suite.Parents) != 0 {
				t.Error("disruptive suite must not contribute to conformance")
			}
		})
	}

	for _, platform := range []string{"azure", "aws", "none"} {
		for _, topology := range []string{"HighlyAvailable", "SingleReplica", "External"} {
			for _, connectivity := range []string{"Direct", "Proxied", "Disconnected"} {
				for _, stack := range []string{"ipv4", "ipv6", "dual"} {
					t.Run(platform+"/"+topology+"/"+connectivity+"/"+stack, func(t *testing.T) {
						env := flags.EnvironmentalFlags{APIGroups: []string{"config.openshift.io"}, Platform: platform, Topology: topology, ExternalConnectivity: connectivity, NetworkStack: stack}
						selected, err := specs.FilterByEnvironment(env)
						if err != nil {
							t.Fatal(err)
						}
						want := 11
						if platform != "azure" {
							want--
						}
						if topology == "External" {
							want -= 2
						}
						if connectivity == "Disconnected" {
							want = 0
						}
						if len(selected) != want {
							t.Errorf("selected %d cases, want %d", len(selected), want)
						}
					})
				}
			}
		}
	}
	selected, err := specs.FilterByEnvironment(flags.EnvironmentalFlags{Platform: "none", ExternalConnectivity: "Direct"})
	if err != nil || len(selected) != 0 {
		t.Errorf("missing config.openshift.io must exclude all migrated cases: selected=%d, err=%v", len(selected), err)
	}
}
