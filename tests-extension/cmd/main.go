package main

import (
	"fmt"
	"os"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	"github.com/spf13/cobra"

	_ "github.com/openshift/cluster-ingress-operator/tests-extension/test"
)

func main() {
	registry := e.NewRegistry()
	ext := e.NewExtension("openshift", "payload", "cluster-ingress-operator")

	// Combined suites (all tests: Gateway API + Ingress Operator)
	ext.AddSuite(e.Suite{
		Name:    "parallel",
		Parents: []string{"openshift/conformance/parallel"},
		Qualifiers: []string{
			`!(name.contains("[Serial]") || name.contains("[Disruptive]") || name.contains("[Slow]"))`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "serial",
		Parents:     []string{"openshift/conformance/serial"},
		Parallelism: 1,
		Qualifiers: []string{
			`(name.contains("[Serial]") || name.contains("[Disruptive]")) && !name.contains("[Slow]")`,
		},
	})

	ext.AddSuite(e.Suite{
		Name:        "all",
		Parallelism: 1,
		Qualifiers: []string{
			`!name.contains("[Slow]")`,
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
