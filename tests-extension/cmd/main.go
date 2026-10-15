package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	"github.com/spf13/cobra"

	_ "github.com/openshift/cluster-ingress-operator/tests-extension/test"
)

func main() {
	registry := e.NewRegistry()
	ext := e.NewExtension("openshift", "payload", "cluster-ingress-operator")

	ext.AddSuite(e.Suite{
		Name:    "ingress/parallel",
		Parents: []string{"openshift/conformance/parallel"},
		Qualifiers: []string{
			`!(name.contains("[Serial]") || name.contains("[Slow]"))`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:    "ingress/serial",
		Parents: []string{"openshift/conformance/serial"},
		Qualifiers: []string{
			`name.contains("[Serial]") && !name.contains("[Slow]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:    "ingress/slow",
		Parents: []string{"openshift/optional/slow"},
		Qualifiers: []string{
			`name.contains("[Slow]")`,
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
// origin/pkg/test/extensions/suites.go.
func appendSuiteNames(specs et.ExtensionTestSpecs) {
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		if strings.Contains(spec.Name, "[Suite:") {
			return
		}
		isSerial := strings.Contains(spec.Name, "[Serial]") || spec.Labels.Has("[Serial]")
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
	})
}
