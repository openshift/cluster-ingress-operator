package main

import (
	"fmt"

	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
)

const (
	// clusterConfigAPIGroup serves dnses.config.openshift.io and
	// ingresscontrollers, the resources these tests build on.
	clusterConfigAPIGroup = "config.openshift.io"

	// hyperShiftTopology is the --topology value a HyperShift hosted cluster
	// reports, its control plane being external to the cluster under test.
	hyperShiftTopology = "External"
)

// hyperShiftUnsupported lists the specs that carried the NonHyperShiftHOST
// token in openshift-tests-private. They read the ingress operator or the
// router deployment, which on HyperShift live on the management cluster in the
// hosted control plane namespace. A conformance run holds a guest kubeconfig
// only, so those objects are unreachable; 75909 reported this as "no
// ingress-operator pod found".
var hyperShiftUnsupported = []string{
	"OCP-22633",
	"OCP-63832",
	"OCP-64611",
	"OCP-75907",
	"OCP-75909",
}

// privateLBUnverified lists the specs whose internal LoadBalancerService does
// not converge on bare metal or vSphere.
//
// 75907 also creates a private LoadBalancer and is deliberately absent:
// OCPBUGS-132070 measured it at 100% on those platforms over 14 days, while
// 75909 timed out at ingress_operator.go:980 in all 24 runs. Excluding a spec
// that demonstrably passes would cost real coverage, so this list follows the
// measurements rather than the shared use of WithPrivateLB.
var privateLBUnverified = []string{
	"OCP-75909",
}

// internalRegistryRequired lists the specs that stand up the test web server,
// which is served from the integrated registry.
var internalRegistryRequired = []string{
	"OCP-22636",
	"OCP-22637",
	"OCP-60012",
	"OCP-60013",
}

// addEnvironmentSelectors restricts the environments in which the ported specs
// run. openshift-tests evaluates these CEL expressions when it lists the
// extension's tests, so an excluded spec is never scheduled and is reported as
// a clean skip rather than failing in setup.
//
// openshift-tests-private honoured tokens in the test name — NonHyperShiftHOST,
// ConnectedOnly — as skips. OTE does not parse test names, so those tokens were
// inert once the specs moved here and every spec ran on every lane
// (OCPBUGS-132070). The constraints are declared here instead, keyed on the
// OCP-<id> Ginkgo label so that rewording a title cannot drop one.
func addEnvironmentSelectors(specs et.ExtensionTestSpecs) {
	// Every ported spec reads dns.config/cluster or drives an
	// IngressController. MicroShift serves neither, so without this gate all
	// fourteen fail during setup on the MicroShift job.
	//
	// Gate on the API group, not the platform. Excluding PlatformEquals("none")
	// looks like the obvious way to skip MicroShift and is wrong: origin derives
	// the platform from ClusterConfiguration.ProviderName, which it sets only
	// for vSphere, AWS, GCP, Azure and IBM Cloud, defaulting otherwise to
	// "skeleton" and reporting that as "none". Assisted bare metal and Azure
	// Stack therefore report "none" too, and excluding it would silently
	// disable these specs on supported platforms.
	//
	// openshift-tests passes --api-group only when it can discover cluster
	// state, which it cannot on MicroShift, so the group is absent there and
	// present on every real cluster.
	selectPorted(specs, "[OTP]").Include(et.APIGroupEnabled(clusterConfigAPIGroup))

	mustSelectLabels(specs, hyperShiftUnsupported, "hyperShiftUnsupported").
		Exclude(et.TopologyEquals(hyperShiftTopology)).
		AddLabel("[Skipped:HyperShift]")

	// 63832 swaps an application router between public and private and checks
	// the health checks that follow, which needs the load balancer to be
	// reachable from outside. This spec carried the ConnectedOnly token.
	mustSelectLabels(specs, []string{"OCP-63832"}, "disconnectedUnsupported").
		Exclude(et.ExternalConnectivityEquals("Disconnected")).
		AddLabel("[Skipped:Disconnected]")

	// ImageRegistry is an optional capability. Following origin's convention
	// for registry-dependent tests, skip only where the cluster reports no
	// optional capabilities at all rather than requiring the capability
	// positively, so a lane that does not advertise its capabilities still runs
	// these.
	mustSelectLabels(specs, internalRegistryRequired, "internalRegistryRequired").
		Exclude(et.NoOptionalCapabilitiesExist()).
		AddLabel("[Skipped:NoOptionalCapabilities]")

	for _, platform := range []string{"baremetal", "vsphere"} {
		mustSelectLabels(specs, privateLBUnverified, "privateLBUnverified").
			Exclude(et.PlatformEquals(platform)).
			AddLabel(fmt.Sprintf("[Skipped:%s]", platform))
	}
}

// selectPorted returns the specs ported from openshift-tests-private.
func selectPorted(specs et.ExtensionTestSpecs, tag string) et.ExtensionTestSpecs {
	selected := specs.Select(et.NameContains(tag))
	if len(selected) == 0 {
		panic(fmt.Sprintf("no specs carry %s; the ported tests are not being selected", tag))
	}
	return selected
}

// mustSelectLabels selects specs by case label and fails the build-time
// registration if the list has drifted from the test file. Without this a
// renamed or deleted case silently loses its environment constraint and starts
// running where it cannot pass, which is the failure mode this whole file
// exists to prevent.
func mustSelectLabels(specs et.ExtensionTestSpecs, caseIDs []string, name string) et.ExtensionTestSpecs {
	selectFns := make([]et.SelectFunction, 0, len(caseIDs))
	for _, caseID := range caseIDs {
		selectFns = append(selectFns, et.HasLabel(caseID))
	}
	selected := specs.SelectAny(selectFns)
	if len(selected) != len(caseIDs) {
		panic(fmt.Sprintf("%s lists %d cases but matched %d specs; "+
			"it is out of sync with test/qe/ingress_operator.go",
			name, len(caseIDs), len(selected)))
	}
	return selected
}
