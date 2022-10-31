package unit

import (
	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	"k8s.io/client-go/kubernetes/scheme"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Fake Cache struct that implements the cache.Cache interface.
type fakeCache struct {
	cache.Informers
	client.Reader
}

// NewFakeClientBuilder creates a new fake client builder with the schema installed to support cluster-ingress-operator
// unit testing.
func NewFakeClientBuilder() *fake.ClientBuilder {
	clientBuilder := fake.NewClientBuilder()
	s := scheme.Scheme
	routev1.Install(s)
	operatorv1.Install(s)

	return clientBuilder
}

// NewFakeClient creates a fake client for cluster-ingress-operator unit testing.
func NewFakeClient(initObjs ...client.Object) client.Client {
	clientBuilder := NewFakeClientBuilder()
	clientBuilder.WithObjects(initObjs...)
	return clientBuilder.Build()
}

// NewFakeCache creates a fake cache object that abides by the controller runtime cache interface so that it can be
// populated into a reconciler object. The cache is essentially just the fake client with a fake informer.
func NewFakeCache(client client.Client) fakeCache {
	informer := informertest.FakeInformers{
		Scheme: client.Scheme(),
	}
	return fakeCache{Informers: &informer, Reader: client}
}
