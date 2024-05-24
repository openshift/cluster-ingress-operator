package client

import (
	"fmt"

	iov1 "github.com/openshift/api/operatoringress/v1"

	configv1 "github.com/openshift/api/config/v1"
	machinev1 "github.com/openshift/api/machine/v1beta1"
	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	maistrav1 "github.com/maistra/istio-operator/pkg/apis/maistra/v1"
	maistrav2 "github.com/maistra/istio-operator/pkg/apis/maistra/v2"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	kscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

var (
	// scheme contains all the API types necessary for the operator's dynamic
	// clients to work. Any new non-core types must be added here.
	//
	// NOTE: The discovery mechanism used by the client won't automatically refresh,
	// so only add types here that are _guaranteed_ to exist before the operator
	// starts.
	scheme *runtime.Scheme
)

func init() {
	scheme = kscheme.Scheme
	if err := operatorv1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := configv1.Install(scheme); err != nil {
		panic(err)
	}
	if err := iov1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := routev1.Install(scheme); err != nil {
		panic(err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := gatewayapiv1beta1.Install(scheme); err != nil {
		panic(err)
	}
	if err := operatorsv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := maistrav1.SchemeBuilder.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := maistrav2.SchemeBuilder.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := machinev1.Install(scheme); err != nil {
		panic(err)
	}
}

func GetScheme() *runtime.Scheme {
	return scheme
}

// NewClient builds an operator-compatible kube client from the given REST config.
func NewClient(kubeConfig *rest.Config) (client.Client, error) {
	httpClient, err := rest.HTTPClientFor(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client: %v", err)
	}
	mapper, err := apiutil.NewDynamicRESTMapper(kubeConfig, httpClient)
	if err != nil {
		return nil, fmt.Errorf("failed to discover api rest mapper: %v", err)
	}
	kubeClient, err := client.New(kubeConfig, client.Options{
		Scheme: scheme,
		Mapper: mapper,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %v", err)
	}
	return kubeClient, nil
}
