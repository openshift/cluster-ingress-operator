package gateway_service_dns

import (
	"context"
	"testing"

	iov1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestClusterIPManagedProfileDoesNotCreateDNS(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, iov1.Install(scheme))
	require.NoError(t, gatewayapiv1.Install(scheme))
	gateway := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: operatorcontroller.DefaultOperandNamespace}, Spec: gatewayapiv1.GatewaySpec{GatewayClassName: operatorcontroller.OpenShiftClusterIPGatewayClassName, Listeners: []gatewayapiv1.Listener{{Hostname: new(gatewayapiv1.Hostname("example.com"))}}}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "gateway-service", Namespace: gateway.Namespace, Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: gateway.Name}}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway, service).Build()
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	r := &reconciler{client: cl, cache: &testutil.FakeCache{Informers: &informertest.FakeInformers{Scheme: scheme}, Reader: cl}, config: Config{OperandNamespace: gateway.Namespace}, modeAccessor: mode}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: service.Name, Namespace: service.Namespace}})
	require.NoError(t, err)
	var records iov1.DNSRecordList
	require.NoError(t, cl.List(context.Background(), &records))
	require.Empty(t, records.Items)
	stale := &iov1.DNSRecord{ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: gateway.Namespace, Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: gateway.Name}}, Spec: iov1.DNSRecordSpec{DNSName: "example.com.", Targets: []string{"old.example.com"}}}
	unrelated := &iov1.DNSRecord{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: gateway.Namespace, Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: "other-gateway"}}}
	require.NoError(t, cl.Create(context.Background(), stale))
	require.NoError(t, cl.Create(context.Background(), unrelated))
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: service.Name, Namespace: service.Namespace}})
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(cl.Get(context.Background(), types.NamespacedName{Name: stale.Name, Namespace: stale.Namespace}, &iov1.DNSRecord{})))
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: unrelated.Name, Namespace: unrelated.Namespace}, &iov1.DNSRecord{}))
}
