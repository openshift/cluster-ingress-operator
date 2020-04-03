package dns

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/tools/record"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

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

func New(mgr manager.Manager, dnsProvider dns.Provider) (runtimecontroller.Controller, error) {
	reconciler := &reconciler{
		client:      mgr.GetClient(),
		cache:       mgr.GetCache(),
		dnsProvider: dnsProvider,
		recorder:    mgr.GetEventRecorderFor(controllerName),
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
	client      client.Client
	cache       cache.Cache
	dnsProvider dns.Provider
	recorder    record.EventRecorder
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
		}
		log.Error(err, "failed to get dnsrecord; will retry", "dnsrecord", request.NamespacedName)
		return reconcile.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// If the DNS record was deleted, clean up and return.
	if record.DeletionTimestamp != nil {
		if err := r.delete(record); err != nil {
			log.Error(err, "failed to delete dnsrecord; will retry", "dnsrecord", record)
			return reconcile.Result{RequeueAfter: 15 * time.Second}, nil
		}
		return reconcile.Result{}, nil
	}

	// Only process records associated with ingresscontrollers, because this isn't
	// intended to be a general purpose DNS management system.
	ingressName, ok := record.Labels[manifests.OwningIngressControllerLabel]
	if !ok {
		log.V(2).Info("warning: dnsrecord is missing owner label", "dnsrecord", record, "ingresscontroller", ingressName)
		return reconcile.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// Existing 4.1 records will have a zero TTL. Instead of making all the client implementations guard against
	// zero TTLs, simply ignore the record until the TTL is updated by the ingresscontroller controller. Report
	// this through events so we can detect problems with our migration.
	if record.Spec.RecordTTL <= 0 {
		r.recorder.Eventf(record, "Warning", "ZeroTTL", "Record is missing TTL and will be temporarily ignored; the TTL will be automatically updated and the record will be retried.")
		return reconcile.Result{}, nil
	}

	if err := r.cache.Get(context.TODO(), types.NamespacedName{Namespace: record.Namespace, Name: ingressName}, &operatorv1.IngressController{}); err != nil {
		if errors.IsNotFound(err) {
			// TODO: what should we do here? something upstream could detect and delete the orphan? add new conditions?
			// is it safe to retry without verifying the owner isn't a new object?
			log.V(2).Info("warning: dnsrecord owner not found; the dnsrecord may be orphaned; will retry", "dnsrecord", record, "ingresscontroller", ingressName)
			return reconcile.Result{RequeueAfter: 1 * time.Minute}, nil
		} else {
			log.Error(err, "failed to get ingresscontroller for dnsrecord; will retry", "dnsrecord", record, "ingresscontroller", ingressName)
			return reconcile.Result{RequeueAfter: 15 * time.Second}, nil
		}
	}

	var zones []configv1.DNSZone
	if dnsConfig.Spec.PrivateZone != nil {
		zones = append(zones, *dnsConfig.Spec.PrivateZone)
	}
	if dnsConfig.Spec.PublicZone != nil {
		zones = append(zones, *dnsConfig.Spec.PublicZone)
	}
	statuses, result := r.publishRecordToZones(zones, record)
	// TODO: only update if status changed
	updated := record.DeepCopy()
	updated.Status.Zones = statuses
	if err := r.client.Status().Update(context.TODO(), updated); err != nil {
		log.Error(err, "failed to update dnsrecord; will retry", "dnsrecord", record)
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}
	return result, nil
}

func (r *reconciler) publishRecordToZones(zones []configv1.DNSZone, record *iov1.DNSRecord) ([]iov1.DNSZoneStatus, reconcile.Result) {
	// TODO: If we compare desired zones with zones from status (i.e. actual
	// zones), we can filter to list to those zones which haven't already been
	// successfully ensured. Then the DNS provider wouldn't need such aggressive
	// internal caching.
	result := reconcile.Result{}
	var statuses []iov1.DNSZoneStatus
	for i := range zones {
		zone := zones[i]
		condition := iov1.DNSZoneCondition{
			Status:             string(operatorv1.ConditionUnknown),
			Type:               iov1.DNSRecordFailedConditionType,
			LastTransitionTime: metav1.Now(),
		}

		if err := r.dnsProvider.Ensure(record, zone); err != nil {
			condition.Status = string(operatorv1.ConditionTrue)
			condition.Reason = "ProviderError"
			condition.Message = fmt.Sprintf("The DNS provider failed to ensure the record: %v", err)
			result.RequeueAfter = 30 * time.Second
		} else {
			condition.Status = string(operatorv1.ConditionFalse)
			condition.Reason = "ProviderSuccess"
			condition.Message = "The DNS provider succeeded in ensuring the record"
		}
		statuses = append(statuses, iov1.DNSZoneStatus{
			DNSZone:    zone,
			Conditions: []iov1.DNSZoneCondition{condition},
		})
	}
	return statuses, result
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
