package clientcaconfigmap

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Test_Reconcile verifies that the controller's Reconcile method behaves
// correctly.  The controller is expected to do the following:
//
//   - Add a finalizer to the IngressController if it specifies a client CA
//     configmap.  This finalizer gives the controller the opportunity to delete
//     any configmap that it has created when the IngressController is deleted.
//
//   - Copy the user-provided source configmap from the "openshift-config"
//     namespace into a target configmap in the "openshift-ingress" namespace.
//
//   - Update the target configmap if the source configmap changes.
//
//   - Delete the target configmap and remove the finalizer if the
//     IngressController is marked for deletion.
//
// The test calls the controller's Reconcile method with a fake client that has
// suitable fake IngressController and configmap objects for the various test
// cases to verify that the controller correctly performs the above tasks.
func Test_Reconcile(t *testing.T) {
	exampleTime, err := time.Parse(time.RFC3339, "2006-01-02T15:04:05Z")
	if err != nil {
		t.Fatal(err)
	}
	ic := func(deleted bool, configmapName string, additionalFinalizers ...string) *operatorv1.IngressController {
		var ts *metav1.Time
		if deleted {
			ts = &metav1.Time{Time: exampleTime}
		}
		return &operatorv1.IngressController{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: ts,
				Finalizers:        append([]string{manifests.IngressControllerFinalizer}, additionalFinalizers...),
				Namespace:         "openshift-ingress-operator",
				Name:              "test",
			},
			Spec: operatorv1.IngressControllerSpec{
				ClientTLS: operatorv1.ClientTLS{
					ClientCA: configv1.ConfigMapNameReference{
						Name: configmapName,
					},
				},
			},
		}
	}
	cm := func(namespace, name string, data map[string]string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
			},
			Data: data,
		}
	}
	var (
		expectedData = map[string]string{
			"ca-bundle.pem": "certificate",
		}
		unexpectedData = map[string]string{
			"garbage": "garbage",
		}
	)
	tests := []struct {
		name            string
		existingObjects []runtime.Object
		expectCreate    []client.Object
		expectUpdate    []client.Object
		expectDelete    []client.Object
	}{
		{
			name:            "do nothing if the ingresscontroller is absent",
			existingObjects: []runtime.Object{},
			expectCreate:    []client.Object{},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
		},
		{
			name: "do nothing if no configmap is specified",
			existingObjects: []runtime.Object{
				ic(false, ""),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "add the finalizer if it is missing, and do nothing else if the source configmap and target configmap are both absent",
			existingObjects: []runtime.Object{
				ic(false, "ca-bundle"),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				ic(false, "ca-bundle", "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"),
			},
			expectDelete: []client.Object{},
		},
		{
			name: "do nothing if the finalizer is present and the source configmap and target configmap are both absent",
			existingObjects: []runtime.Object{
				ic(false, "ca-bundle", "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "add the finalizer if it is missing, and delete the target configmap if it exists but the source configmap is absent",
			existingObjects: []runtime.Object{
				ic(false, "ca-bundle"),
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				ic(false, "ca-bundle", "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"),
			},
			expectDelete: []client.Object{
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
		},
		{
			name: "delete the target configmap if it exists but the source configmap is absent",
			existingObjects: []runtime.Object{
				ic(false, "ca-bundle", "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"),
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
		},
		{
			name: "add the finalizer if it is missing, and create the target configmap if it is absent and the source configmap is present",
			existingObjects: []runtime.Object{
				ic(false, "ca-bundle"),
				cm("openshift-config", "ca-bundle", expectedData),
			},
			expectCreate: []client.Object{
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
			expectUpdate: []client.Object{
				ic(false, "ca-bundle", "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"),
			},
			expectDelete: []client.Object{},
		},
		{
			name: "create the target configmap if it is absent and the source configmap is present",
			existingObjects: []runtime.Object{
				ic(false, "ca-bundle", "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"),
				cm("openshift-config", "ca-bundle", expectedData),
			},
			expectCreate: []client.Object{
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "add the finalizer if it is missing, and do nothing else if the source configmap and target configmap are both present",
			existingObjects: []runtime.Object{
				ic(false, "ca-bundle"),
				cm("openshift-config", "ca-bundle", expectedData),
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				ic(false, "ca-bundle", "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"),
			},
			expectDelete: []client.Object{},
		},
		{
			name: "update the target configmap if the source configmap and target configmap are both present but inconsistent",
			existingObjects: []runtime.Object{
				ic(false, "ca-bundle", "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"),
				cm("openshift-config", "ca-bundle", expectedData),
				cm("openshift-ingress", "router-client-ca-test", unexpectedData),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
			expectDelete: []client.Object{},
		},
		{
			name: "do nothing if the finalizer and target configmap are absent and the ingresscontroller is deleted",
			existingObjects: []runtime.Object{
				ic(true, "ca-bundle"),
				cm("openshift-config", "ca-bundle", expectedData),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "delete the target configmap if it is present and the ingresscontroller is deleted and is missing the finalizer",
			existingObjects: []runtime.Object{
				ic(true, "ca-bundle"),
				cm("openshift-config", "ca-bundle", expectedData),
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{
				cm("openshift-ingress", "router-client-ca-test", expectedData),
			},
		},
		{
			name: "delete the target configmap if the source configmap and target configmap are both present but inconsistent when the ingresscontroller is deleted",
			existingObjects: []runtime.Object{
				ic(true, "ca-bundle", "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"),
				cm("openshift-config", "ca-bundle", expectedData),
				cm("openshift-ingress", "router-client-ca-test", unexpectedData),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				ic(true, "ca-bundle"),
			},
			expectDelete: []client.Object{
				cm("openshift-ingress", "router-client-ca-test", unexpectedData),
			},
		},
	}

	scheme := runtime.NewScheme()
	operatorv1.Install(scheme)
	corev1.AddToScheme(scheme)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				Build()
			cl := &fakeClientRecorder{fakeClient, t, []client.Object{}, []client.Object{}, []client.Object{}}
			reconciler := &reconciler{
				client: cl,
				config: Config{
					SourceNamespace: "openshift-config",
					TargetNamespace: "openshift-ingress",
				},
			}
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: "openshift-ingress-operator",
					Name:      "test",
				},
			}
			res, err := reconciler.Reconcile(context.Background(), req)
			assert.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, res)
			cmpOpts := []cmp.Option{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Annotations", "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(apiextensionsv1.CustomResourceDefinition{}, "Spec"),
			}
			if diff := cmp.Diff(tc.expectCreate, cl.added, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual creates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectUpdate, cl.updated, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual updates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectDelete, cl.deleted, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual deletes: %s", diff)
			}
		})
	}
}

// fakeClientRecorder is a fake client that records adds, updates, and deletes.
type fakeClientRecorder struct {
	client.Client
	*testing.T

	added   []client.Object
	updated []client.Object
	deleted []client.Object
}

func (c *fakeClientRecorder) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.added = append(c.added, obj)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.deleted = append(c.deleted, obj)
	return c.Client.Delete(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updated = append(c.updated, obj)
	return c.Client.Update(ctx, obj, opts...)
}
