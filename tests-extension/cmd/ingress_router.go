package main

import (
	"fmt"
	"strings"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
)

// Use canonical case labels for registration rather than title fragments.
const ingressRouterSelector = `labels.exists(label, label in ["OCP-33960", "OCP-41109", "OCP-51148", "OCP-51429", "OCP-51437", "OCP-56228", "OCP-57089", "OCP-57370", "OCP-73619", "OCP-85280", "OCP-85950"])`

// Preserve the first OTE names used before adopting the naming convention in
// openshift/router#871. Later renames must retain these original identities.
var legacyIngressRouterNames = map[string]string{
	"OCP-33960": "Author:mjoseph-ROSA-OSD_CCS-ARO-Critical-33960-Setting route.openshift.io/termination annotation to Edge/Passthrough/Reencrypt in ingress resource",
	"OCP-41109": "Author:hongli-Critical-41109-use IngressClass controller for ingress-to-route",
	"OCP-51148": "Author:mjoseph-Critical-51148-host name of the route depends on the subdomain if provided",
	"OCP-51429": "Author:mjoseph-High-51429-different router deployment with same route using subdomain",
	"OCP-51437": "Author:mjoseph-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-High-51437-Router deployment using different shard with same subdomain",
	"OCP-56228": "Author:shudili-NonPreRelease-Longduration-High-56228-Deletion of default router service is recovered [Disruptive]",
	"OCP-57089": "ARO-Author:shudili-High-57089-Azure external and internal load balancer services receive IPs",
	"OCP-57370": "Author:mjoseph-NonHyperShiftHOST-High-57370-component route hostname is RFC compliant [Serial]",
	"OCP-73619": "Author:mjoseph-ROSA-OSD_CCS-ARO-Critical-73619-Ingress operator is enabled as an optional component",
	"OCP-85280": "Author:shudili-ROSA-OSD_CCS-ARO-High-85280-router rejects invalid certificate content on ingress [Serial]",
	"OCP-85950": "Author:shudili-ROSA-OSD_CCS-ARO-High-85950-Propagate ingress labels to generated routes",
}

func ingressRouterCaseID(spec *et.ExtensionTestSpec) string {
	for caseID := range legacyIngressRouterNames {
		if spec.Labels.Has(caseID) {
			return caseID
		}
	}
	return ""
}

func registerIngressRouterSuites(ext *e.Extension) {
	for _, suite := range []e.Suite{
		{
			Name:        "ingress-router/conformance/parallel",
			Parents:     []string{"openshift/conformance/parallel"},
			Description: "Level0 ingress/router QE tests without serial or disruptive execution tags",
			Qualifiers:  []string{ingressRouterSelector + ` && name.contains("[Level0]") && !(name.contains("[Serial]") || name.contains("[Disruptive]"))`},
		},
		{
			Name:        "ingress-router/conformance/serial",
			Parents:     []string{"openshift/conformance/serial"},
			Parallelism: 1,
			Description: "Level0 ingress/router QE serial tests without intentional disruptions",
			Qualifiers:  []string{ingressRouterSelector + ` && name.contains("[Level0]") && name.contains("[Serial]") && !name.contains("[Disruptive]")`},
		},
		{
			Name:        "ingress-router/disruptive",
			Parallelism: 1,
			Description: "Disruptive ingress/router QE tests; intentionally has no conformance parent",
			Qualifiers:  []string{ingressRouterSelector + ` && name.contains("[Disruptive]")`},
		},
		{
			Name:       "ingress-router/non-disruptive",
			Qualifiers: []string{ingressRouterSelector + ` && !name.contains("[Disruptive]")`},
		},
		{
			Name:        "ingress-router/all",
			Parallelism: 1,
			Qualifiers:  []string{ingressRouterSelector},
		},
	} {
		ext.AddSuite(suite)
	}
}

func configureIngressRouterSpecs(specs et.ExtensionTestSpecs) {
	seen := make(map[string]bool, len(legacyIngressRouterNames))
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		// Keep generated metadata independent of the build checkout directory.
		for i, location := range spec.CodeLocations {
			if idx := strings.LastIndex(location, "/tests-extension/"); idx != -1 {
				spec.CodeLocations[i] = location[idx+len("/tests-extension/"):]
			}
		}
		caseID := ingressRouterCaseID(spec)
		if caseID == "" {
			return
		}
		if seen[caseID] {
			panic(fmt.Sprintf("duplicate canonical ingress/router case label %s", caseID))
		}
		seen[caseID] = true
		if spec.OriginalName == "" {
			title := legacyIngressRouterNames[caseID]
			suffix := " [Suite:openshift/conformance/parallel]"
			if strings.Contains(title, "[Serial]") || strings.Contains(title, "[Disruptive]") {
				suffix = " [Suite:openshift/conformance/serial]"
			}
			spec.OriginalName = "[sig-network-edge][Feature:Router][Feature:IngressOperator] QE ingress and router cases " + title + suffix
		}
		spec.Lifecycle = et.LifecycleInforming
		// API availability distinguishes MicroShift from supported platforms that
		// also report platform=none (including bare metal).
		include := et.APIGroupEnabled("config.openshift.io")
		if caseID == "OCP-57089" {
			// Include calls are ORed by OTE, so combine requirements explicitly.
			include = et.And(include, et.PlatformEquals("azure"))
		}
		spec.Include(include)
		// Current fixtures pull an external image. Retain connected/proxied IPv6
		// coverage; remove this exclusion after validating configured mirrors.
		spec.Exclude(et.ExternalConnectivityEquals("Disconnected"))
		if caseID == "OCP-51437" || caseID == "OCP-57370" {
			spec.Exclude(et.TopologyEquals("External"))
		}
		if strings.Contains(spec.Name, "[Serial]") || strings.Contains(spec.Name, "[Disruptive]") {
			spec.Resources.Isolation.Taint = append(spec.Resources.Isolation.Taint, "disruptive")
			spec.Resources.Isolation.Conflict = append(spec.Resources.Isolation.Conflict, "disruptive")
		} else {
			spec.Resources.Isolation.Taint = append(spec.Resources.Isolation.Taint, "parallel-running")
			spec.Resources.Isolation.Toleration = append(spec.Resources.Isolation.Toleration, "parallel-running")
		}
	})
	if len(seen) != len(legacyIngressRouterNames) {
		panic(fmt.Sprintf("expected %d canonical ingress/router case labels, found %d", len(legacyIngressRouterNames), len(seen)))
	}
}
