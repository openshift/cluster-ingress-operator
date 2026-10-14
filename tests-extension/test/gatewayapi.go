package test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = describeGatewayAPICRDTests("GatewayAPI", false)
var _ = describeGatewayAPICRDTests("GatewayAPIManagementMode", true)

// describeGatewayAPICRDTests registers protection checks separately to preserve the other CRD test names.
func describeGatewayAPICRDTests(featureGate string, protection bool) bool {
	title := fmt.Sprintf("[sig-network][OCPFeatureGate:%s][Feature:Router][apigroup:gateway.networking.k8s.io]", featureGate)
	if protection {
		title += "[apigroup:operator.openshift.io]"
	}
	return g.Describe(title, func() {
		var (
			apiExtClient apiextensionsclient.Interface
			crdNames     = []string{
				"gatewayclasses.gateway.networking.k8s.io",
				"gateways.gateway.networking.k8s.io",
				"httproutes.gateway.networking.k8s.io",
				"grpcroutes.gateway.networking.k8s.io",
				"referencegrants.gateway.networking.k8s.io",
				"backendtlspolicies.gateway.networking.k8s.io",
				"listenersets.gateway.networking.k8s.io",
				"tlsroutes.gateway.networking.k8s.io",
			}
			errorMessage = "ValidatingAdmissionPolicy 'openshift-ingress-operator-gatewayapi-crd-admission' with binding 'openshift-ingress-operator-gatewayapi-crd-admission' denied request: Gateway API Custom Resource Definitions are managed by the Ingress Operator and may not be modified"
		)

		g.BeforeEach(func() {
			config, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to build REST config from KUBECONFIG")
			apiExtClient, err = apiextensionsclient.NewForConfig(config)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create apiextensions client")
		})

		g.Describe("Verify Gateway API CRDs", func() {
			if !protection {
				g.It("and ensure required CRDs should already be installed", func() {
					g.By("Get and check the installed CRDs")
					for _, name := range crdNames {
						crd, err := apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(
							context.Background(), name, metav1.GetOptions{})
						o.Expect(err).NotTo(o.HaveOccurred())
						fmt.Fprintf(g.GinkgoWriter, "Found the Gateway API CRD named: %v\n", crd.Name)
					}
				})
			}

			if protection {
				g.BeforeEach(func(ctx g.SpecContext) {
					cl, err := newClients()
					o.Expect(err).NotTo(o.HaveOccurred(), "failed to build clients for Gateway API CRD protection prerequisites")
					reason, err := gatewayAPIProtectionSkipReason(ctx, cl.client)
					o.Expect(err).NotTo(o.HaveOccurred(), "failed to determine Gateway API CRD management state")
					if reason != "" {
						g.Skip(reason)
					}
				})

				g.It("and ensure existing CRDs can not be deleted", func() {
					g.By("Try to delete the CRDs and fail")
					for _, name := range crdNames {
						err := apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Delete(
							context.Background(), name, metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}})
						o.Expect(err).To(o.HaveOccurred(), "expected dry-run deletion of CRD %q to be denied", name)
						o.Expect(err.Error()).To(o.ContainSubstring(errorMessage), "unexpected dry-run deletion error for CRD %q", name)
					}
				})

				g.It("and ensure existing CRDs can not be updated", func() {
					g.By("Get the CRDs firstly, add spec.names.shortNames then update CRD")
					for _, name := range crdNames {
						crd, err := apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(
							context.Background(), name, metav1.GetOptions{})
						o.Expect(err).NotTo(o.HaveOccurred(), "failed to get CRD %q before dry-run update", name)
						// some CRDs have a shortName but some not, just trying to add one for all
						crd.Spec.Names.ShortNames = append(crd.Spec.Names.ShortNames, "fakename")
						_, err = apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Update(
							context.Background(), crd, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
						o.Expect(err).To(o.HaveOccurred(), "expected dry-run update of CRD %q to be denied", name)
						o.Expect(err.Error()).To(o.ContainSubstring(errorMessage), "unexpected dry-run update error for CRD %q", name)
					}
				})

				g.It("and ensure CRD of standard group can not be created", func() {
					fakeCRDName := "fakeroutes.gateway.networking.k8s.io"
					g.By("Try to create CRD of standard group and fail")
					fakeCRD := buildGWAPICRDFromName(fakeCRDName)
					_, err := apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Create(
						context.Background(), fakeCRD, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
					o.Expect(err).To(o.HaveOccurred(), "expected dry-run creation of CRD %q to be denied", fakeCRDName)
					o.Expect(err.Error()).To(o.ContainSubstring(errorMessage), "unexpected dry-run creation error for CRD %q", fakeCRDName)
				})
			}

			if !protection {
				g.It("and ensure CRD of experimental group is not installed", func() {
					g.By("Ensure no CRD of experimental group is installed")
					crdList, err := apiExtClient.ApiextensionsV1().CustomResourceDefinitions().List(
						context.Background(), metav1.ListOptions{})
					o.Expect(err).NotTo(o.HaveOccurred())
					for _, crd := range crdList.Items {
						if crd.Spec.Group == "gateway.networking.x-k8s.io" {
							g.Fail(fmt.Sprintf("Found unexpected CRD named: %v", crd.Name))
						}
						// Check standard group GWAPI CRDs to ensure they are not in the experimental channel
						if slices.Contains(crdNames, crd.Name) {
							if channel, ok := crd.Annotations["gateway.networking.k8s.io/channel"]; ok && channel == "experimental" {
								g.Fail(fmt.Sprintf("Found experimental channel CRD: %v (expected standard channel)", crd.Name))
							}
						}
					}
				})
			}
		})
	})
}

// gatewayAPIProtectionSkipReason skips deliberate non-management and errors on
// indeterminate state or stale managed or takeover-blocked status.
func gatewayAPIProtectionSkipReason(ctx context.Context, cl crclient.Client) (string, error) {
	ingress := &unstructured.Unstructured{}
	ingress.SetGroupVersionKind(schema.GroupVersionKind{Group: "operator.openshift.io", Version: "v1alpha1", Kind: "Ingress"})
	if err := cl.Get(ctx, types.NamespacedName{Name: "cluster"}, ingress); err != nil {
		return "", fmt.Errorf("get operator Ingress: %w", err)
	}
	mode, _, err := unstructured.NestedString(ingress.Object, "spec", "gatewayAPI", "managementMode")
	if err != nil {
		return "", err
	}
	if mode == "Unmanaged" {
		return "Gateway API CRDs are unmanaged", nil
	}
	conditions, _, err := unstructured.NestedSlice(ingress.Object, "status", "conditions")
	if err != nil {
		return "", err
	}
	for _, item := range conditions {
		condition, ok := item.(map[string]interface{})
		if !ok || condition["type"] != "GatewayAPICRDsManaged" {
			continue
		}
		if condition["status"] == "False" && condition["reason"] == "TakeoverBlocked" {
			observedGeneration, _, err := unstructured.NestedInt64(condition, "observedGeneration")
			if err != nil {
				return "", err
			}
			if observedGeneration != ingress.GetGeneration() {
				return "", fmt.Errorf("GatewayAPICRDsManaged has stale TakeoverBlocked status")
			}
			return "Gateway API CRD takeover is blocked", nil
		}
		if condition["status"] == "True" {
			observedGeneration, found, err := unstructured.NestedInt64(condition, "observedGeneration")
			if err != nil {
				return "", fmt.Errorf("read GatewayAPICRDsManaged observedGeneration: %w", err)
			}
			if !found {
				return "", fmt.Errorf("GatewayAPICRDsManaged=True is missing observedGeneration")
			}
			if observedGeneration != ingress.GetGeneration() {
				return "", fmt.Errorf("GatewayAPICRDsManaged=True has stale observedGeneration %d, expected %d", observedGeneration, ingress.GetGeneration())
			}
			return "", nil
		}
	}
	return "", fmt.Errorf("GatewayAPICRDsManaged is not True for management mode %q", mode)
}

// buildGWAPICRDFromName initializes a fake GatewayAPI CRD deducing most of its
// required fields from the given name (format: "<plural>.<group>").
// singular is derived by removing the trailing 's' from plural, and kind by
// capitalizing the first letter of singular. This is intentionally simplistic
// and only correct for regular plurals; it is sufficient because this function
// is only called with "fakeroutes.gateway.networking.k8s.io".
func buildGWAPICRDFromName(name string) *apiextensionsv1.CustomResourceDefinition {
	plural := strings.Split(name, ".")[0]
	group, _ := strings.CutPrefix(name, plural+".")
	// remove trailing "s" to get singular
	singular := plural[:len(plural)-1]
	kind := strings.ToUpper(string(singular[0])) + singular[1:]

	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: plural + "." + group,
			Annotations: map[string]string{
				"api-approved.kubernetes.io": "https://github.com/kubernetes-sigs/gateway-api/pull/2466",
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: singular,
				Plural:   plural,
				Kind:     kind,
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Storage: true,
					Served:  true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
				{
					Name:    "v1beta1",
					Storage: false,
					Served:  true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
			},
		},
	}
}
