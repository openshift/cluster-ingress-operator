package dns

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"k8s.io/client-go/tools/record"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	awsdns "github.com/openshift/cluster-ingress-operator/pkg/dns/aws"
	azuredns "github.com/openshift/cluster-ingress-operator/pkg/dns/azure"
	gcpdns "github.com/openshift/cluster-ingress-operator/pkg/dns/gcp"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	operatorutil "github.com/openshift/cluster-ingress-operator/pkg/util"
	oputil "github.com/openshift/cluster-ingress-operator/pkg/util"
	awsutil "github.com/openshift/cluster-ingress-operator/pkg/util/aws"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilclock "k8s.io/apimachinery/pkg/util/clock"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	operatoringressv1 "github.com/openshift/api/operatoringress/v1"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "dns_controller"

	// cloudCredentialsSecretName is the name of the secret in the
	// operator's namespace that will hold the credentials that the operator
	// will use to authenticate with the cloud API.
	cloudCredentialsSecretName = "cloud-credentials"

	// kubeCloudConfigName is the name of the kube cloud config ConfigMap
	kubeCloudConfigName = "kube-cloud-config"
	// cloudCABundleKey is the key in the kube cloud config ConfigMap where the custom CA bundle is located
	cloudCABundleKey = "ca-bundle.pem"
)

var log = logf.Logger.WithName(controllerName)

func New(mgr manager.Manager, config Config) (runtimecontroller.Controller, error) {
	reconciler := &reconciler{
		config:   config,
		client:   mgr.GetClient(),
		cache:    mgr.GetCache(),
		recorder: mgr.GetEventRecorderFor(controllerName),
	}
	c, err := runtimecontroller.New(controllerName, mgr, runtimecontroller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &iov1.DNSRecord{}}, &handler.EnqueueRequestForObject{}, predicate.GenerationChangedPredicate{}); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &configv1.DNS{}}, handler.EnqueueRequestsFromMapFunc(reconciler.ToDNSRecords)); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &configv1.Infrastructure{}}, handler.EnqueueRequestsFromMapFunc(reconciler.ToDNSRecords)); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &corev1.Secret{}}, handler.EnqueueRequestsFromMapFunc(reconciler.ToDNSRecords), predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return e.Object.GetName() == cloudCredentialsSecretName },
		DeleteFunc: func(e event.DeleteEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectNew.GetName() != cloudCredentialsSecretName {
				return false
			}
			oldSecret := e.ObjectOld.(*corev1.Secret)
			newSecret := e.ObjectNew.(*corev1.Secret)
			return !reflect.DeepEqual(oldSecret.Data, newSecret.Data)
		},
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}); err != nil {
		return nil, err
	}
	return c, nil
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	Namespace              string
	OperatorReleaseVersion string
}

type reconciler struct {
	config Config

	client           client.Client
	cache            cache.Cache
	dnsProvider      dns.Provider
	infraConfig      *configv1.Infrastructure
	cloudCredentials *corev1.Secret
	recorder         record.EventRecorder
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// TODO: If the new revision has changed, undefined stuff happens (resource leaks?)
	dnsConfig := &configv1.DNS{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get dns 'cluster': %v", err)
	}

	if err := r.createDNSProviderIfNeeded(dnsConfig); err != nil {
		return reconcile.Result{}, err
	}

	record := &iov1.DNSRecord{}
	if err := r.client.Get(ctx, request.NamespacedName, record); err != nil {
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

	if err := r.cache.Get(ctx, types.NamespacedName{Namespace: record.Namespace, Name: ingressName}, &operatorv1.IngressController{}); err != nil {
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
		if err := r.client.Status().Update(ctx, updated); err != nil {
			log.Error(err, "failed to update dnsrecord; will retry", "dnsrecord", updated)
			return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		} else {
			log.Info("updated dnsrecord", "dnsrecord", updated)
		}
	}
	return result, nil
}

// createDNSProviderIfNeeded creates a new DNS provider if none has yet been
// created or if the infrastructure platform status or cloud credentials have
// changed since the current provider was created.  After creating a new
// provider, createDNSProviderIfNeeded updates the reconciler state
// with the new provider and current platform status and cloud credentials.
func (r *reconciler) createDNSProviderIfNeeded(dnsConfig *configv1.DNS) error {
	var needUpdate bool

	infraConfig := &configv1.Infrastructure{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return fmt.Errorf("failed to get infrastructure 'config': %v", err)
	}

	platformStatus, err := oputil.GetPlatformStatus(r.client, infraConfig)
	if err != nil {
		return fmt.Errorf("failed to get platform status: %v", err)
	}

	creds := &corev1.Secret{}
	switch platformStatus.Type {
	case configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.GCPPlatformType:
		name := types.NamespacedName{Namespace: r.config.Namespace, Name: cloudCredentialsSecretName}
		if err := r.cache.Get(context.TODO(), name, creds); err != nil {
			return fmt.Errorf("failed to get cloud credentials from secret %s: %v", name, err)
		}

		if r.cloudCredentials == nil || !reflect.DeepEqual(creds.Data, r.cloudCredentials.Data) {
			needUpdate = true
		}
	}

	if r.infraConfig == nil || !reflect.DeepEqual(infraConfig.Status, r.infraConfig.Status) {
		needUpdate = true
	}

	if needUpdate {
		dnsProvider, err := r.createDNSProvider(dnsConfig, platformStatus, creds)
		if err != nil {
			return fmt.Errorf("failed to create DNS provider: %v", err)
		}

		r.dnsProvider, r.infraConfig, r.cloudCredentials = dnsProvider, infraConfig, creds
	}

	return nil
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

		if recordIsAlreadyPublishedToZone(record, &zone) {
			log.Info("replacing DNS record", "record", record.Spec, "dnszone", zone)

			if err := r.dnsProvider.Replace(record, zone); err != nil {
				log.Error(err, "failed to replace DNS record in zone", "record", record.Spec, "dnszone", zone)
				condition.Status = string(operatorv1.ConditionTrue)
				condition.Reason = "ProviderError"
				condition.Message = fmt.Sprintf("The DNS provider failed to replace the record: %v", err)
				result.RequeueAfter = 30 * time.Second
			} else {
				log.Info("replaced DNS record in zone", "record", record.Spec, "dnszone", zone)
				condition.Status = string(operatorv1.ConditionFalse)
				condition.Reason = "ProviderSuccess"
				condition.Message = "The DNS provider succeeded in replacing the record"
			}
		} else {
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
		}
		statuses = append(statuses, iov1.DNSZoneStatus{
			DNSZone:    zone,
			Conditions: []iov1.DNSZoneCondition{condition},
		})
	}
	return mergeStatuses(record.Status.DeepCopy().Zones, statuses), result
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
		// If the record is currently not published in a zone,
		// skip deleting it for that zone.
		if !recordIsAlreadyPublishedToZone(record, &zone) {
			continue
		}
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

// ToDNSRecords returns reconciliation requests for all DNSRecords.
func (r *reconciler) ToDNSRecords(o client.Object) []reconcile.Request {
	var requests []reconcile.Request
	records := &operatoringressv1.DNSRecordList{}
	if err := r.cache.List(context.Background(), records, client.InNamespace(r.config.Namespace)); err != nil {
		log.Error(err, "failed to list dnsrecords", "related", o.GetSelfLink())
		return requests
	}
	for _, record := range records.Items {
		log.Info("queueing dnsrecord", "name", record.Name, "related", o.GetSelfLink())
		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: record.Namespace,
				Name:      record.Name,
			},
		}
		requests = append(requests, request)
	}
	return requests
}

// createDNSProvider creates a DNS manager compatible with the given cluster
// configuration.
func (r *reconciler) createDNSProvider(dnsConfig *configv1.DNS, platformStatus *configv1.PlatformStatus, creds *corev1.Secret) (dns.Provider, error) {
	// If no DNS configuration is provided, don't try to set up provider clients.
	// TODO: the provider configuration can be refactored into the provider
	// implementations themselves, so this part of the code won't need to
	// know anything about the provider setup beyond picking the right implementation.
	// Then, it would be safe to always use the appropriate provider for the platform
	// and let the provider surface configuration errors if DNS records are actually
	// created to exercise the provider.
	if dnsConfig.Spec.PrivateZone == nil && dnsConfig.Spec.PublicZone == nil {
		log.Info("using fake DNS provider because no public or private zone is defined in the cluster DNS configuration")
		return &dns.FakeProvider{}, nil
	}

	var dnsProvider dns.Provider
	userAgent := fmt.Sprintf("OpenShift/%s (ingress-operator)", r.config.OperatorReleaseVersion)

	switch platformStatus.Type {
	case configv1.AWSPlatformType:
		cfg := awsdns.Config{
			Region: platformStatus.AWS.Region,
		}

		sharedCredsFile, err := awsutil.SharedCredentialsFileFromSecret(creds)
		if err != nil {
			return nil, fmt.Errorf("failed to create shared credentials file from Secret: %v", err)
		}
		// since at the end of this function the aws dns provider will be initialized with aws clients, the AWS SDK no
		// longer needs access to the file and therefore it can be removed.
		defer os.Remove(sharedCredsFile)
		cfg.SharedCredentialFile = sharedCredsFile

		if len(platformStatus.AWS.ServiceEndpoints) > 0 {
			cfg.ServiceEndpoints = []awsdns.ServiceEndpoint{}
			route53Found := false
			elbFound := false
			tagFound := false
			for _, ep := range platformStatus.AWS.ServiceEndpoints {
				switch {
				case route53Found && elbFound && tagFound:
					break
				case ep.Name == awsdns.Route53Service:
					route53Found = true
					scheme, err := operatorutil.URI(ep.URL)
					if err != nil {
						return nil, fmt.Errorf("failed to validate URI %s: %v", ep.URL, err)
					}
					if scheme != operatorutil.SchemeHTTPS {
						return nil, fmt.Errorf("invalid scheme for URI %s; must be %s", ep.URL, operatorutil.SchemeHTTPS)
					}
					cfg.ServiceEndpoints = append(cfg.ServiceEndpoints, awsdns.ServiceEndpoint{Name: ep.Name, URL: ep.URL})
				case ep.Name == awsdns.ELBService:
					elbFound = true
					scheme, err := operatorutil.URI(ep.URL)
					if err != nil {
						return nil, fmt.Errorf("failed to validate URI %s: %v", ep.URL, err)
					}
					if scheme != operatorutil.SchemeHTTPS {
						return nil, fmt.Errorf("invalid scheme for URI %s; must be %s", ep.URL, operatorutil.SchemeHTTPS)
					}
					cfg.ServiceEndpoints = append(cfg.ServiceEndpoints, awsdns.ServiceEndpoint{Name: ep.Name, URL: ep.URL})
				case ep.Name == awsdns.TaggingService:
					tagFound = true
					scheme, err := operatorutil.URI(ep.URL)
					if err != nil {
						return nil, fmt.Errorf("failed to validate URI %s: %v", ep.URL, err)
					}
					if scheme != operatorutil.SchemeHTTPS {
						return nil, fmt.Errorf("invalid scheme for URI %s; must be %s", ep.URL, operatorutil.SchemeHTTPS)
					}
					cfg.ServiceEndpoints = append(cfg.ServiceEndpoints, awsdns.ServiceEndpoint{Name: ep.Name, URL: ep.URL})
				}
			}
		}

		cfg.CustomCABundle, err = r.customCABundle()
		if err != nil {
			return nil, fmt.Errorf("failed to get the custom CA bundle: %w", err)
		}

		provider, err := awsdns.NewProvider(cfg, r.config.OperatorReleaseVersion)
		if err != nil {
			return nil, fmt.Errorf("failed to create AWS DNS manager: %v", err)
		}
		dnsProvider = provider
	case configv1.AzurePlatformType:
		environment := platformStatus.Azure.CloudName
		if environment == "" {
			environment = configv1.AzurePublicCloud
		}
		provider, err := azuredns.NewProvider(azuredns.Config{
			Environment:    string(environment),
			ClientID:       string(creds.Data["azure_client_id"]),
			ClientSecret:   string(creds.Data["azure_client_secret"]),
			TenantID:       string(creds.Data["azure_tenant_id"]),
			SubscriptionID: string(creds.Data["azure_subscription_id"]),
		}, r.config.OperatorReleaseVersion)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure DNS manager: %v", err)
		}
		dnsProvider = provider
	case configv1.GCPPlatformType:
		provider, err := gcpdns.New(gcpdns.Config{
			Project:         platformStatus.GCP.ProjectID,
			CredentialsJSON: creds.Data["service_account.json"],
			UserAgent:       userAgent,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create GCP DNS provider: %v", err)
		}
		dnsProvider = provider
	default:
		dnsProvider = &dns.FakeProvider{}
	}
	return dnsProvider, nil
}

// customCABundle will get the custom CA bundle, if present, configured in the kube cloud config.
func (r *reconciler) customCABundle() (string, error) {
	cm := &corev1.ConfigMap{}
	switch err := r.client.Get(
		context.Background(),
		client.ObjectKey{Namespace: controller.GlobalMachineSpecifiedConfigNamespace, Name: kubeCloudConfigName},
		cm,
	); {
	case errors.IsNotFound(err):
		// no cloud config ConfigMap, so no custom CA bundle
		return "", nil
	case err != nil:
		return "", fmt.Errorf("failed to get kube-cloud-config ConfigMap: %w", err)
	}
	caBundle, ok := cm.Data[cloudCABundleKey]
	if !ok {
		// no "ca-bundle.pem" key in the ConfigMap, so no custom CA bundle
		return "", nil
	}
	return caBundle, nil
}
