package dns

import (
	"context"
	"fmt"
	"time"

	iov1 "github.com/openshift/cluster-ingress-operator/pkg/api/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	"k8s.io/client-go/tools/record"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "dns_controller"
)

var log = logf.Logger.WithName(controllerName)

func New(mgr manager.Manager, operatorNamespace string, dnsProvider dns.Provider) (runtimecontroller.Controller, error) {
	reconciler := &reconciler{
		client:            mgr.GetClient(),
		cache:             mgr.GetCache(),
		recorder:          mgr.GetEventRecorderFor(controllerName),
		operatorNamespace: operatorNamespace,
		dnsProvider:       dnsProvider,
	}
	c, err := runtimecontroller.New(controllerName, mgr, runtimecontroller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &iov1.DNSRecord{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}
	return c, nil
}

type reconciler struct {
	client            client.Client
	cache             cache.Cache
	recorder          record.EventRecorder
	operatorNamespace string
	dnsProvider       dns.Provider
	dnsConfig         *configv1.DNS
}

func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// TODO: If the new revision has changed, undefined stuff happens (resource leaks?)
	dnsConfig := &configv1.DNS{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get dns 'cluster': %v", err)
	}

	record := &iov1.DNSRecord{}
	if err := r.client.Get(context.TODO(), request.NamespacedName, record); err != nil {
		if errors.IsNotFound(err) {
			log.Info("dnsrecord not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		} else {
			return reconcile.Result{RequeueAfter: 15 * time.Second}, fmt.Errorf("failed to get dnsrecord: %v", err)
		}
	}

	// If the DNS record was deleted, clean up and return.
	if record.DeletionTimestamp != nil {
		if err := r.delete(record); err != nil {
			return reconcile.Result{RequeueAfter: 15 * time.Second}, err
		}
		return reconcile.Result{}, nil
	}

	ingressName, ok := record.Labels[manifests.OwningIngressControllerLabel]
	if !ok {
		return reconcile.Result{}, fmt.Errorf("missing owner label")
	}
	ingress := &operatorv1.IngressController{}
	if err := r.cache.Get(context.TODO(), types.NamespacedName{Namespace: record.Namespace, Name: ingressName}, ingress); err != nil {
		if errors.IsNotFound(err) {
			// TODO: what should we do here? something upstream could detect and delete the orphan? add new conditions?
			return reconcile.Result{RequeueAfter: 1 * time.Minute}, fmt.Errorf("ingresscontroller %s not found for record %s", ingressName, record.Name)
		} else {
			return reconcile.Result{RequeueAfter: 15 * time.Second}, fmt.Errorf("failed to get ingresscontroller %s for record %s: %v", ingressName, record.Name, err)
		}
	}

	// TODO: Will soon need to honor scope of the ingresscontroller's LoadBalancer
	// strategy.
	zones := []configv1.DNSZone{}
	if dnsConfig.Spec.PrivateZone != nil {
		zones = append(zones, *dnsConfig.Spec.PrivateZone)
	}
	if dnsConfig.Spec.PublicZone != nil {
		zones = append(zones, *dnsConfig.Spec.PublicZone)
	}

	// TODO: If we compare desired zones with zones from status (i.e. actual
	// zones), we can filter to list to those zones which haven't already been
	// successfully ensured. Then the DNS provider wouldn't need such aggressive
	// internal caching.
	result := reconcile.Result{}
	statuses := []iov1.DNSZoneStatus{}
	for i := range zones {
		zone := zones[i]
		conditions := []iov1.DNSZoneCondition{}
		err := r.dnsProvider.Ensure(record, zone)
		if err != nil {
			conditions = append(conditions, iov1.DNSZoneCondition{
				Type:               iov1.DNSRecordFailedConditionType,
				Status:             string(operatorv1.ConditionTrue),
				Reason:             "ProviderError",
				Message:            fmt.Sprintf("The DNS provider failed to ensure the record: %v", err),
				LastTransitionTime: metav1.Now(),
			})
			result.RequeueAfter = 30 * time.Second
		}
		statuses = append(statuses, iov1.DNSZoneStatus{
			DNSZone:    zone,
			Conditions: conditions,
		})
	}

	// TODO: only update if status changed
	updated := record.DeepCopy()
	updated.Status.Zones = statuses
	if err := r.client.Status().Update(context.TODO(), updated); err != nil {
		return reconcile.Result{RequeueAfter: 10 * time.Second}, fmt.Errorf("failed to update dnsrecord status: %v", err)
	}

	return result, nil
}

func (r *reconciler) delete(record *iov1.DNSRecord) error {
	var errs []error
	for i := range record.Status.Zones {
		zone := record.Status.Zones[i].DNSZone
		err := r.dnsProvider.Delete(record, zone)
		if err != nil {
			errs = append(errs, err)
		} else {
			log.Info("deleted dnsrecord from DNS provider", "dnsrecord", record, "zone", zone)
		}
	}
	if len(errs) == 0 {
		updated := record.DeepCopy()
		if slice.ContainsString(updated.Finalizers, manifests.DNSRecordFinalizer) {
			updated.Finalizers = slice.RemoveString(updated.Finalizers, manifests.DNSRecordFinalizer)
			if err := r.client.Update(context.TODO(), updated); err != nil {
				errs = append(errs, fmt.Errorf("failed to remove finalizer from dnsrecord %s: %v", record.Name, err))
			}
		}
	}
	return utilerrors.NewAggregate(errs)
}
