package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	"github.com/spf13/cobra"

	_ "github.com/openshift/cluster-ingress-operator/tests-extension/test"
	_ "github.com/openshift/cluster-ingress-operator/tests-extension/test/qe"
)

// defaultTestTimeout mirrors the default timeout SpawnProcessToRunTest applies
// when a spec has no explicit Timeout.
const defaultTestTimeout = 90 * time.Minute

func main() {
	registry := e.NewRegistry()
	ext := e.NewExtension("openshift", "payload", "cluster-ingress-operator")

	// Combined suites (all tests: Gateway API + Ingress Operator).
	//
	// Suite names must be unique across every extension in the payload, and
	// openshift-tests itself declares internal suites such as "all",
	// "openshift/conformance/parallel", etc. Unprefixed names collide with
	// those and make test discovery fail for the whole payload, so every
	// suite declared here is namespaced with a component prefix.
	ext.AddSuite(e.Suite{
		Name:    "ingress/parallel",
		Parents: []string{"openshift/conformance/parallel"},
		Qualifiers: []string{
			`!(name.contains("[Serial]") || name.contains("[Disruptive]") || name.contains("[Slow]"))`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "ingress/serial",
		Parents:     []string{"openshift/conformance/serial"},
		Parallelism: 1,
		Qualifiers: []string{
			`name.contains("[Serial]") && !name.contains("[Disruptive]") && !name.contains("[Slow]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "ingress/all",
		Parallelism: 1,
		Qualifiers: []string{
			`!name.contains("[Slow]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:    "ingress/slow",
		Parents: []string{"openshift/optional/slow"},
		Qualifiers: []string{
			`name.contains("[Slow]")`,
		},
	})

	// Gateway API suites.
	//
	// These filter positively on [OCPFeatureGate:GatewayAPI] rather than merely
	// excluding [Feature:IngressOperator]. An exclusion-based filter also swept in
	// the AWS LB security-group tests, which are not Gateway API tests, and would
	// mis-bin every future test that is not explicitly excluded.
	ext.AddSuite(e.Suite{
		Name:    "gateway-api/parallel",
		Parents: []string{"openshift/conformance/parallel"},
		Qualifiers: []string{
			`name.contains("[OCPFeatureGate:GatewayAPI]") && !(name.contains("[Serial]") || name.contains("[Disruptive]") || name.contains("[Slow]"))`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "gateway-api/serial",
		Parents:     []string{"openshift/conformance/serial"},
		Parallelism: 1,
		Qualifiers: []string{
			`name.contains("[OCPFeatureGate:GatewayAPI]") && (name.contains("[Serial]") || name.contains("[Disruptive]")) && !name.contains("[Slow]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:    "gateway-api/slow",
		Parents: []string{"openshift/optional/slow"},
		Qualifiers: []string{
			`name.contains("[OCPFeatureGate:GatewayAPI]") && name.contains("[Slow]")`,
		},
	})

	// AWS LB security group suites (only LB security group tests).
	//
	// These tests are AWS-only and feature-gated. Each one provisions its own
	// IngressController and NLB, so lb-security-groups/all pins Parallelism to 1
	// to avoid standing up five load balancers at once.
	ext.AddSuite(e.Suite{
		Name:        "lb-security-groups/all",
		Parallelism: 1,
		Qualifiers: []string{
			`name.contains("[OCPFeatureGate:IngressControllerLBSecurityGroupsAWS]") && !name.contains("[Slow]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:    "lb-security-groups/parallel",
		Parents: []string{"openshift/conformance/parallel"},
		Qualifiers: []string{
			`name.contains("[OCPFeatureGate:IngressControllerLBSecurityGroupsAWS]") && !(name.contains("[Serial]") || name.contains("[Disruptive]") || name.contains("[Slow]"))`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "lb-security-groups/serial",
		Parents:     []string{"openshift/conformance/serial"},
		Parallelism: 1,
		Qualifiers: []string{
			`name.contains("[OCPFeatureGate:IngressControllerLBSecurityGroupsAWS]") && (name.contains("[Serial]") || name.contains("[Disruptive]")) && !name.contains("[Slow]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:    "lb-security-groups/slow",
		Parents: []string{"openshift/optional/slow"},
		Qualifiers: []string{
			`name.contains("[OCPFeatureGate:IngressControllerLBSecurityGroupsAWS]") && name.contains("[Slow]")`,
		},
	})

	// Ingress Operator suites.
	//
	// These select on [OTP], the tag carried by every spec ported from
	// openshift-tests-private, and gate conformance membership on [Level0].
	// The shape matches the router and cluster-dns-operator extensions.
	ext.AddSuite(e.Suite{
		Name:             "ingress-operator/all",
		Description:      "All ported ingress operator tests",
		Parallelism:      1,
		ClusterStability: e.ClusterStabilityDisruptive,
		Qualifiers: []string{
			`name.contains("[OTP]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "ingress-operator/conformance/parallel",
		Parents:     []string{"openshift/conformance/parallel"},
		Description: "Parallel conformance tests (non-serial, non-disruptive)",
		Qualifiers: []string{
			`name.contains("[OTP]") && name.contains("[Level0]") && !(name.contains("[Serial]") || name.contains("[Disruptive]") || name.contains("[Slow]"))`,
		},
	})

	// Keep disruptive specs out of conformance jobs, whose availability monitors
	// can fail during the intentional cluster changes those specs make.
	ext.AddSuite(e.Suite{
		Name:             "ingress-operator/conformance/serial",
		Parents:          []string{"openshift/conformance/serial"},
		Description:      "Non-disruptive serial conformance tests",
		Parallelism:      1,
		ClusterStability: e.ClusterStabilityDisruptive,
		Qualifiers: []string{
			`name.contains("[OTP]") && name.contains("[Level0]") && name.contains("[Serial]") && !name.contains("[Disruptive]") && !name.contains("[Slow]")`,
		},
	})

	// Run these specs explicitly on a cluster that can tolerate the disruption.
	// They have no conformance parent, so ordinary payload jobs do not select them.
	ext.AddSuite(e.Suite{
		Name:             "ingress-operator/disruptive",
		Description:      "Disruptive tests (may affect cluster state)",
		Parallelism:      1,
		ClusterStability: e.ClusterStabilityDisruptive,
		Qualifiers: []string{
			`name.contains("[OTP]") && name.contains("[Disruptive]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "ingress-operator/non-disruptive",
		Description: "All non-disruptive ported tests (safe for development clusters)",
		Qualifiers: []string{
			`name.contains("[OTP]") && !name.contains("[Disruptive]")`,
		},
	})

	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		fmt.Fprintf(os.Stderr, "couldn't build extension test specs from ginkgo: %v\n", err)
		os.Exit(1)
	}

	normalizeCodeLocations(specs)

	// Ported specs land informing: routine failures stay non-fatal while they
	// build up the reliability record the conformance suites require. See the
	// promotion criteria in test/qe/README.md.
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		if strings.Contains(spec.Name, "[OTP]") {
			spec.Lifecycle = et.LifecycleInforming
		}
	})

	// Record the cluster environments individual specs cannot run in. This
	// runs before the suite names are appended so the selectors match on the
	// spec names as written in the test files.
	addEnvironmentSelectors(specs)

	// Append suite names to test names to match the naming convention used
	// by origin's openshift-tests binary. This preserves test name continuity
	// for downstream systems (Sippy, CI test mapping) when tests are migrated
	// from origin to OTE.
	appendSuiteNames(specs)
	// Retire the three titles previously recorded in conformance metadata.
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		if spec.Labels.Has("OCP-75907") || spec.Labels.Has("OCP-75908") || spec.Labels.Has("OCP-75909") {
			ext.IgnoreObsoleteTests(spec.Name + " [Serial] [Suite:openshift/conformance/serial]")
		}
	})

	ext.AddSpecs(specs)
	registry.Register(ext)

	root := &cobra.Command{
		Long: "OpenShift Tests Extension for Cluster Ingress Operator",
	}
	root.AddCommand(cmd.DefaultExtensionCommands(registry)...)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// moduleDirSegment is the path segment ending at this module's root, used to
// trim the build directory out of the source paths Ginkgo records.
const moduleDirSegment = "/tests-extension/"

// normalizeCodeLocations rewrites the source path Ginkgo records for each spec
// to be relative to this module's root. Ginkgo captures absolute paths, which
// differ between a developer's checkout and the CI build image; left absolute
// they would make the .openshift-tests-extension metadata depend on where the
// binary happened to be compiled, so it could never be verified.
func normalizeCodeLocations(specs et.ExtensionTestSpecs) {
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		for i, location := range spec.CodeLocations {
			if idx := strings.LastIndex(location, moduleDirSegment); idx != -1 {
				spec.CodeLocations[i] = location[idx+len(moduleDirSegment):]
			}
		}
	})
}

// appendSuiteNames appends suite names to the end of test names to match the
// naming convention used by origin's openshift-tests binary. Disruptive tests
// have no conformance parent, so they do not get a conformance suite suffix.
func appendSuiteNames(specs et.ExtensionTestSpecs) {
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		if strings.Contains(spec.Name, "[Suite:") {
			return
		}
		isDisruptive := strings.Contains(spec.Name, "[Disruptive]") || spec.Labels.Has("[Disruptive]")
		isSerial := strings.Contains(spec.Name, "[Serial]") || spec.Labels.Has("[Serial]")
		isConformance := strings.Contains(spec.Name, "[Conformance]") || spec.Labels.Has("[Conformance]")
		var suite string
		switch {
		case isDisruptive:
			// This spec is available only through an explicit disruptive suite.
		case isSerial && isConformance:
			suite = " [Suite:openshift/conformance/serial/minimal]"
		case isSerial:
			suite = " [Suite:openshift/conformance/serial]"
		case isConformance:
			suite = " [Suite:openshift/conformance/parallel/minimal]"
		default:
			suite = " [Suite:openshift/conformance/parallel]"
		}
		spec.Name += suite

		// Rebind RunParallel so it re-execs with the renamed spec.
		//
		// BuildExtensionTestSpecsFromOpenShiftGinkgoSuite builds RunParallel as a
		// closure over the original ginkgo spec text (see pkg/ginkgo/util.go). That
		// captured name goes stale the moment we rename the spec above, so
		// `run-suite` spawns `run-test <original name>` while the child process only
		// resolves specs by their current Name (Extension.FindSpecsByName matches
		// Name exactly), and every test fails with "no such tests".
		//
		// Reading spec.Name and spec.Timeout inside the closure keeps this correct:
		// Timeout is populated from Suite.TestTimeout after this walk runs.
		spec.RunParallel = func(ctx context.Context) *et.ExtensionTestResult {
			timeout := defaultTestTimeout
			if spec.Timeout > 0 {
				timeout = spec.Timeout
			}
			return g.SpawnProcessToRunTest(ctx, spec.Name, timeout)
		}
	})
}
