package test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	g "github.com/onsi/ginkgo/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type gatewayAPIIngressClient struct {
	crclient.Client
	ingress *unstructured.Unstructured
}

func (cl gatewayAPIIngressClient) Get(_ context.Context, _ crclient.ObjectKey, obj crclient.Object, _ ...crclient.GetOption) error {
	if cl.ingress == nil {
		return fmt.Errorf("operator Ingress unavailable")
	}
	obj.(*unstructured.Unstructured).Object = cl.ingress.DeepCopy().Object
	return nil
}

func TestGatewayAPIProtectionSkipReason(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name, mode, status, reason          string
		skip, wantError                     bool
		omitMode, omitConditions            bool
		stale                               bool
		omitGeneration, malformedGeneration bool
		zeroGeneration                      bool
		errorContains                       string
	}{
		{name: "enabled and managed", mode: "Managed", status: "True"},
		{name: "default managed", status: "True", omitMode: true},
		{name: "stale managed must fail", mode: "Managed", status: "True", stale: true, wantError: true, errorContains: "stale observedGeneration 1, expected 2"},
		{name: "missing managed generation must fail", mode: "Managed", status: "True", omitGeneration: true, wantError: true, errorContains: "missing observedGeneration"},
		{name: "missing managed generation with zero ingress generation must fail", mode: "Managed", status: "True", omitGeneration: true, zeroGeneration: true, wantError: true, errorContains: "missing observedGeneration"},
		{name: "malformed managed generation must fail", mode: "Managed", status: "True", malformedGeneration: true, wantError: true, errorContains: "read GatewayAPICRDsManaged observedGeneration"},
		{name: "unmanaged", mode: "Unmanaged", skip: true},
		{name: "unmanaged with stale managed status", mode: "Unmanaged", status: "True", skip: true},
		{name: "takeover blocked", mode: "Managed", status: "False", reason: "TakeoverBlocked", skip: true},
		{name: "stale takeover must not skip", mode: "Managed", status: "False", reason: "TakeoverBlocked", stale: true, wantError: true},
		{name: "missing status must not skip", mode: "Managed", omitConditions: true, wantError: true},
		{name: "unknown status must not skip", mode: "Managed", status: "Unknown", wantError: true},
		{name: "unexpected false status must not skip", mode: "Managed", status: "False", wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ingress := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "operator.openshift.io/v1alpha1", "kind": "Ingress",
				"metadata": map[string]interface{}{"name": "cluster", "generation": int64(1)},
				"spec":     map[string]interface{}{"gatewayAPI": map[string]interface{}{"managementMode": testCase.mode}},
				"status": map[string]interface{}{"conditions": []interface{}{map[string]interface{}{
					"type": "GatewayAPICRDsManaged", "status": testCase.status, "reason": testCase.reason,
					"observedGeneration": int64(1),
				}}},
			}}
			if testCase.omitMode {
				delete(ingress.Object, "spec")
			}
			if testCase.omitConditions {
				delete(ingress.Object, "status")
			}
			if testCase.stale {
				ingress.SetGeneration(2)
			}
			if testCase.zeroGeneration {
				ingress.SetGeneration(0)
			}
			if testCase.omitGeneration || testCase.malformedGeneration {
				conditions := ingress.Object["status"].(map[string]interface{})["conditions"].([]interface{})
				condition := conditions[0].(map[string]interface{})
				if testCase.omitGeneration {
					delete(condition, "observedGeneration")
				} else {
					condition["observedGeneration"] = "invalid"
				}
			}
			cl := gatewayAPIIngressClient{ingress: ingress}
			reason, err := gatewayAPIProtectionSkipReason(context.Background(), cl)
			if (reason != "") != testCase.skip || (err != nil) != testCase.wantError {
				t.Fatalf("got reason %q, error %v; want skip=%v, error=%v", reason, err, testCase.skip, testCase.wantError)
			}
			if testCase.errorContains != "" && (err == nil || !strings.Contains(err.Error(), testCase.errorContains)) {
				t.Errorf("got error %v; want diagnostic containing %q", err, testCase.errorContains)
			}
		})
	}
	t.Run("missing ingress must not skip", func(t *testing.T) {
		cl := gatewayAPIIngressClient{}
		reason, err := gatewayAPIProtectionSkipReason(context.Background(), cl)
		if reason != "" || err == nil {
			t.Fatalf("got reason %q, error %v; want error without skip", reason, err)
		}
	})
}

func TestGatewayAPIProtectionFeatureGateTags(t *testing.T) {
	report := g.PreviewSpecs("Gateway API feature gates")
	protection, other := 0, 0
	for _, spec := range report.SpecReports {
		name := spec.FullText()
		if !strings.Contains(name, "Verify Gateway API CRDs") {
			continue
		}
		if strings.Contains(name, "can not be") {
			protection++
			if !strings.Contains(name, "[apigroup:operator.openshift.io]") || !strings.Contains(name, "[apigroup:gateway.networking.k8s.io]") {
				t.Errorf("protection test omits a required API group: %s", name)
			}
			if !strings.Contains(name, "[OCPFeatureGate:GatewayAPIManagementMode]") || strings.Contains(name, "[OCPFeatureGate:GatewayAPI]") {
				t.Errorf("protection test has incorrect feature gates: %s", name)
			}
		} else {
			other++
			if !strings.Contains(name, "[OCPFeatureGate:GatewayAPI]") || strings.Contains(name, "[OCPFeatureGate:GatewayAPIManagementMode]") {
				t.Errorf("non-protection test gating changed: %s", name)
			}
			prefix := "[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io] Verify Gateway API CRDs and ensure "
			if name != prefix+"required CRDs should already be installed" && name != prefix+"CRD of experimental group is not installed" {
				t.Errorf("non-protection test was renamed: %s", name)
			}
		}
	}
	if protection != 3 || other != 2 {
		t.Fatalf("found %d protection and %d other tests, want 3 and 2", protection, other)
	}
}
