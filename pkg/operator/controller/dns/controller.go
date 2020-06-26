package dns

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"k8s.io/client-go/tools/record"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilclock "k8s.io/apimachinery/pkg/util/clock"
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
	log.Info("reconciling", "request", request)

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
	if !dnsZoneStatusSlicesEqual(statuses, record.Status.Zones) {
		updated := record.DeepCopy()
		updated.Status.Zones = statuses
		updated.Status.ObservedGeneration = updated.Generation
		if err := r.client.Status().Update(context.TODO(), updated); err != nil {
			log.Error(err, "failed to update dnsrecord; will retry", "dnsrecord", updated)
			return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		} else {
			log.Info("updated dnsrecord", "dnsrecord", updated)
		}
	}
	return result, nil
}

func (r *reconciler) publishRecordToZones(zones []configv1.DNSZone, record *iov1.DNSRecord) ([]iov1.DNSZoneStatus, reconcile.Result) {
	result := reconcile.Result{}
	var statuses []iov1.DNSZoneStatus
	for i := range zones {
		zone := zones[i]

		// Only publish the record if the DNSRecord has been modified
		// (which would mean the target could have changed) or its
		// status does not indicate that it has already been published.
		if record.Generation == record.Status.ObservedGeneration && recordIsAlreadyPublishedToZone(record, &zone) {
			log.Info("skipping zone to which the DNS record is already published", "record", record.Spec, "dnszone", zone)
			continue
		}

		condition := iov1.DNSZoneCondition{
			Status:             string(operatorv1.ConditionUnknown),
			Type:               iov1.DNSRecordFailedConditionType,
			LastTransitionTime: metav1.Now(),
		}

		if err := r.dnsProvider.Ensure(record, zone); err != nil {
			log.Error(err, "failed to publish DNS record to zone", "record", record.Spec, "dnszone", zone)
			condition.Status = string(operatorv1.ConditionTrue)
			condition.Reason = "ProviderError"
			condition.Message = fmt.Sprintf("The DNS provider failed to ensure the record: %v", err)
			result.RequeueAfter = 30 * time.Second
		} else {
			log.Info("published DNS record to zone", "record", record.Spec, "dnszone", zone)
			condition.Status = string(operatorv1.ConditionFalse)
			condition.Reason = "ProviderSuccess"
			condition.Message = "The DNS provider succeeded in ensuring the record"
		}
		statuses = append(statuses, iov1.DNSZoneStatus{
			DNSZone:    zone,
			Conditions: []iov1.DNSZoneCondition{condition},
		})
	}
	return mergeStatuses(record.Status.Zones, statuses), result
}

// recordIsAlreadyPublishedToZone returns a Boolean value indicating whether the
// given DNSRecord is already published to the given zone, as determined from
// the DNSRecord's status conditions.
func recordIsAlreadyPublishedToZone(record *iov1.DNSRecord, zoneToPublish *configv1.DNSZone) bool {
	for _, zoneInStatus := range record.Status.Zones {
		if !reflect.DeepEqual(&zoneInStatus.DNSZone, zoneToPublish) {
			continue
		}

		for _, condition := range zoneInStatus.Conditions {
			if condition.Type == iov1.DNSRecordFailedConditionType {
				return condition.Status == string(operatorv1.ConditionFalse)
			}
		}
	}

	return false
}

func (r *reconciler) delete(record *iov1.DNSRecord) error {
	var errs []error
	for i := range record.Status.Zones {
		zone := record.Status.Zones[i].DNSZone
		err := r.dnsProvider.Delete(record, zone)
		if err != nil {
			errs = append(errs, err)
		} else {
			log.Info("deleted dnsrecord from DNS provider", "record", record.Spec, "zone", zone)
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

// mergeStatuses updates or extends the provided slice of statuses with the
// provided updates and returns the resulting slice.
func mergeStatuses(statuses, updates []iov1.DNSZoneStatus) []iov1.DNSZoneStatus {
	var additions []iov1.DNSZoneStatus
	for i, update := range updates {
		add := true
		for j, status := range statuses {
			if cmp.Equal(status.DNSZone, update.DNSZone) {
				add = false
				statuses[j].Conditions = mergeConditions(status.Conditions, update.Conditions)
			}
		}
		if add {
			additions = append(additions, updates[i])
		}
	}
	return append(statuses, additions...)
}

// clock is to enable unit testing
var clock utilclock.Clock = utilclock.RealClock{}

// mergeConditions adds or updates matching conditions, and updates
// the transition time if details of a condition have changed. Returns
// the updated condition array.
func mergeConditions(conditions, updates []iov1.DNSZoneCondition) []iov1.DNSZoneCondition {
	now := metav1.NewTime(clock.Now())
	var additions []iov1.DNSZoneCondition
	for i, update := range updates {
		add := true
		for j, cond := range conditions {
			if cond.Type == update.Type {
				add = false
				if conditionChanged(cond, update) {
					conditions[j].Status = update.Status
					conditions[j].Reason = update.Reason
					conditions[j].Message = update.Message
					conditions[j].LastTransitionTime = now
					break
				}
			}
		}
		if add {
			updates[i].LastTransitionTime = now
			additions = append(additions, updates[i])
		}
	}
	conditions = append(conditions, additions...)
	return conditions
}

func conditionChanged(a, b iov1.DNSZoneCondition) bool {
	return a.Status != b.Status || a.Reason != b.Reason || a.Message != b.Message
}

// dnsZoneStatusSlicesEqual compares two DNSZoneStatus slice values.  Returns
// true if the provided values should be considered equal for the purpose of
// determining whether an update is necessary, false otherwise.  The comparison
// is agnostic with respect to the ordering of status conditions but not with
// respect to zones.
func dnsZoneStatusSlicesEqual(a, b []iov1.DNSZoneStatus) bool {
	conditionCmpOpts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b iov1.DNSZoneCondition) bool {
			return a.Type < b.Type
		}),
	}
	if !cmp.Equal(a, b, conditionCmpOpts...) {
		return false
	}

	return true
}
