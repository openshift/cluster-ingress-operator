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
			`(name.contains("[Serial]") || name.contains("[Disruptive]")) && !name.contains("[Slow]")`,
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

	// Gateway API suites (exclude IngressOperator tests)
	ext.AddSuite(e.Suite{
		Name:    "gateway-api/parallel",
		Parents: []string{"openshift/conformance/parallel"},
		Qualifiers: []string{
			`!(name.contains("[Serial]") || name.contains("[Disruptive]") || name.contains("[Slow]") || name.contains("[Feature:IngressOperator]"))`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "gateway-api/serial",
		Parents:     []string{"openshift/conformance/serial"},
		Parallelism: 1,
		Qualifiers: []string{
			`(name.contains("[Serial]") || name.contains("[Disruptive]")) && !name.contains("[Slow]") && !name.contains("[Feature:IngressOperator]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:    "gateway-api/slow",
		Parents: []string{"openshift/optional/slow"},
		Qualifiers: []string{
			`name.contains("[Slow]") && !name.contains("[Feature:IngressOperator]")`,
		},
	})

	// Ingress Operator suites (only IngressOperator tests)
	ext.AddSuite(e.Suite{
		Name:        "ingress-operator/all",
		Parallelism: 1,
		Qualifiers: []string{
			`name.contains("[Feature:IngressOperator]") && !name.contains("[Slow]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:    "ingress-operator/parallel",
		Parents: []string{"openshift/conformance/parallel"},
		Qualifiers: []string{
			`name.contains("[Feature:IngressOperator]") && !(name.contains("[Serial]") || name.contains("[Disruptive]") || name.contains("[Slow]"))`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "ingress-operator/serial",
		Parents:     []string{"openshift/conformance/serial"},
		Parallelism: 1,
		Qualifiers: []string{
			`name.contains("[Feature:IngressOperator]") && (name.contains("[Serial]") || name.contains("[Disruptive]")) && !name.contains("[Slow]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:    "ingress-operator/slow",
		Parents: []string{"openshift/optional/slow"},
		Qualifiers: []string{
			`name.contains("[Feature:IngressOperator]") && name.contains("[Slow]")`,
		},
	})

	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		fmt.Fprintf(os.Stderr, "couldn't build extension test specs from ginkgo: %v\n", err)
		os.Exit(1)
	}

	// Append suite names to test names to match the naming convention used
	// by origin's openshift-tests binary. This preserves test name continuity
	// for downstream systems (Sippy, CI test mapping) when tests are migrated
	// from origin to OTE.
	appendSuiteNames(specs)

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

// appendSuiteNames appends suite names to the end of test names to match the
// naming convention used by origin's openshift-tests binary. This logic mirrors
// origin/pkg/test/extensions/suites.go, except that [Disruptive] is classified
// as serial (see below).
func appendSuiteNames(specs et.ExtensionTestSpecs) {
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		if strings.Contains(spec.Name, "[Suite:") {
			return
		}
		// [Disruptive] counts as serial here. Origin's version only checks
		// [Serial], but the suites declared above route [Serial] and
		// [Disruptive] to the same place, so treating them alike keeps the
		// appended name consistent with the suite a test actually runs in.
		isSerial := strings.Contains(spec.Name, "[Serial]") || spec.Labels.Has("[Serial]") ||
			strings.Contains(spec.Name, "[Disruptive]") || spec.Labels.Has("[Disruptive]")
		isConformance := strings.Contains(spec.Name, "[Conformance]") || spec.Labels.Has("[Conformance]")
		var suite string
		switch {
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
