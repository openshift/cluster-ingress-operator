/*
This command is used to run the OLMv1 tests extension for OpenShift.
It registers the OLMv1 tests with the OpenShift Tests Extension framework
and provides a command-line interface to execute them.

For further information, please refer to the documentation at:
https://github.com/openshift-eng/openshift-tests-extension/blob/main/cmd/example-tests/main.go
*/
package main

import (
	"fmt"
	"os"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	"github.com/spf13/cobra"

	"github.com/openshift/cluster-ingress-operator/test/extended/framework"
	_ "github.com/openshift/cluster-ingress-operator/test/extended/specs"
)

func main() {
	registry := e.NewRegistry()
	ext := e.NewExtension("openshift", "payload", "cluster-ingress-operator")

	ext.AddSuite(
		e.Suite{
			Name:    "cluster-ingress-operator/parallel",
			Parents: []string{"openshift/conformance/parallel"},
			Qualifiers: []string{
				`!(name.contains("[Serial]") || name.contains("[Slow]"))`,
			},
		})

	ext.AddSuite(
		e.Suite{
			Name:    "cluster-ingress-operator/serial",
			Parents: []string{"openshift/conformance/serial"},
			Qualifiers: []string{
				`(name.contains("[Serial]") || name.contains("[Slow]"))`,
			},
		})

	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		panic(fmt.Sprintf("couldn't build extension test specs from ginkgo: %+v", err.Error()))
	}

	// Initialize the environment before running any tests.
	specs.AddBeforeAll(func() {
		framework.Init()
	})

	// TODO: Remove this, it is just to verify that openshift-origin is able to fetch OTE tests
	specs = specs.Walk(func(spec *et.ExtensionTestSpec) {
		spec.Name = spec.Name + " using OTE"
	})

	ext.AddSpecs(specs)
	registry.Register(ext)

	root := &cobra.Command{
		Long: "Cluster Ingress Operator Tests Extension",
	}

	root.AddCommand(cmd.DefaultExtensionCommands(registry)...)

	if err := func() error {
		return root.Execute()
	}(); err != nil {
		os.Exit(1)
	}
}
