package gatewayclass

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/istio-ecosystem/sail-operator/pkg/install"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	ControllerInstalledConditionType = "ControllerInstalled"
	CRDsReadyConditionType           = "CRDsReady"

	subscriptionPrefix = "operators.coreos.com/"
)

// SubscriptionExists is used as a predicate function to allow the library to
// query CIO if a subscription exists before taking an action over a CRD
func (r *reconciler) overwriteOLMManagedCRDFunc(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) bool {
	// defensive measure
	if ctx == nil || crd == nil {
		return false
	}

	logOverwrite := log.WithValues("crd", crd.GetName())

	labels := crd.GetLabels()
	if labels == nil {
		// no labels, we can override
		return true
	}

	// This is not a OLM managed CRD
	if val, ok := labels["olm.managed"]; !ok || val != "true" {
		return true
	}

	var subscriptionName, subscriptionNamespace, foundLabel string
	// we just care about the label name
	for label := range labels {
		if after, ok := strings.CutPrefix(label, subscriptionPrefix); ok {
			// Check if label is on the format we want
			nameNamespace := strings.SplitN(after, ".", 2)
			if len(nameNamespace) != 2 {
				logOverwrite.Info("ignoring invalid OLM label", "label", label)
				continue
			}
			foundLabel = label
			subscriptionName, subscriptionNamespace = nameNamespace[0], nameNamespace[1]
			break
		}
	}

	if foundLabel == "" || subscriptionName == "" || subscriptionNamespace == "" {
		logOverwrite.Info("no subscription label found")
		return true
	}

	// Check for InstallPlan, which effectively installs CRDs. Even if invalid it
	// may become valid at some point and overwrite CRDs
	installPlanList := operatorsv1alpha1.InstallPlanList{}
	if err := r.cache.List(ctx, &installPlanList, client.HasLabels([]string{foundLabel})); err != nil {
		log.Error(err, "error trying to find install plans")
		return false
	}
	if len(installPlanList.Items) > 0 {
		logOverwrite.Info("CRD has valid installPlans, not overwriting")
		return false
	}

	// Next check for subscriptions.
	subscription := operatorsv1alpha1.Subscription{}
	subscription.SetNamespace(subscriptionNamespace)
	subscription.SetName(subscriptionName)
	err := r.cache.Get(ctx, client.ObjectKeyFromObject(&subscription), &subscription)
	if err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "error trying to find subscription")
			return false
		}
		// No subscription was found, the CRD can be overwritten
		return true
	}
	// If we are here means we don't have installplans, but we do have subscriptions
	// which means we may be on an intermediate state
	return false
}

// ensureIstio installs or updates Istio using the Sail Library.
// It returns an error if the installation fails.
func (r *reconciler) ensureIstio(ctx context.Context, gatewayclass *gatewayapiv1.GatewayClass, istioVersion string) error {
	if r.config.SailOperatorReconciler == nil || r.config.SailOperatorReconciler.Installer == nil {
		return fmt.Errorf("internal error: sail operator is null")
	}
	sailInstaller := r.config.SailOperatorReconciler.Installer

	enableInferenceExtension, err := r.inferencepoolCrdExists(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for InferencePool CRD: %w", err)
	}

	// Build options from current state
	opts, err := r.buildInstallerOptions(enableInferenceExtension, istioVersion)
	if err != nil {
		return err
	}

	opts.OverwriteOLMManagedCRD = r.overwriteOLMManagedCRDFunc
	// Enqueue the request to reconcile. This does not return
	// any error or status. We validate the status again next, and set
	// the right conditions on the GatewayClass for it
	sailInstaller.Apply(opts)

	status := sailInstaller.Status()
	log.V(1).Info("CRD management result", "status", status.CRDs)

	mapStatusToConditions(status, gatewayclass.Generation, &gatewayclass.Status.Conditions)

	return nil
}

func (r *reconciler) buildInstallerOptions(enableInferenceExtension bool, istioVersion string) (install.Options, error) {
	// Start with Gateway API defaults
	values := install.GatewayAPIDefaults()

	// Apply OpenShift-specific overrides
	openshiftOverrides := openshiftValues(enableInferenceExtension)
	values = install.MergeValues(values, openshiftOverrides)

	return install.Options{
		Namespace:      controller.DefaultOperandNamespace,
		Revision:       controller.IstioName("").Name,
		Values:         values,
		Version:        istioVersion,
		ManageCRDs:     ptr.To(true),
		IncludeAllCRDs: ptr.To(true),
	}, nil
}

// mapStatusToConditions translates the library Status into GatewayClass conditions.
func mapStatusToConditions(status install.Status, generation int64, conditions *[]metav1.Condition) {
	installed := metav1.Condition{
		Type:               ControllerInstalledConditionType,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}
	if status.Installed {
		installed.Status = metav1.ConditionTrue
		installed.Reason = "Installed"
		if status.Error != nil {
			installed.Message = fmt.Sprintf("istiod %s installed (with warning: %v)", status.Version, status.Error)
		} else {
			installed.Message = fmt.Sprintf("istiod %s installed", status.Version)
		}
	} else if status.Error != nil {
		installed.Status = metav1.ConditionFalse
		installed.Reason = "InstallFailed"
		installed.Message = status.Error.Error()
	} else {
		installed.Status = metav1.ConditionUnknown
		installed.Reason = "Pending"
		installed.Message = "waiting for first reconciliation"
	}

	meta.SetStatusCondition(conditions, installed)

	// CRD condition: reflects CRD ownership state.
	crd := metav1.Condition{
		Type:               CRDsReadyConditionType,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}
	switch status.CRDState {
	case install.CRDManagedByCIO:
		crd.Status = metav1.ConditionTrue
		crd.Reason = "ManagedByCIO"
		crd.Message = status.CRDMessage
	case install.CRDManagedByOLM:
		crd.Status = metav1.ConditionTrue
		crd.Reason = "ManagedByOLM"
		crd.Message = status.CRDMessage
	case install.CRDNoneExist:
		crd.Status = metav1.ConditionUnknown
		crd.Reason = "NoneExist"
		crd.Message = "CRDs not yet installed"
	case install.CRDMixedOwnership:
		crd.Status = metav1.ConditionFalse
		crd.Reason = "MixedOwnership"
		crd.Message = getCRDStatusMessage(status)
	default:
		crd.Status = metav1.ConditionFalse
		crd.Reason = "UnknownManagement"
		crd.Message = getCRDStatusMessage(status)
	}
	meta.SetStatusCondition(conditions, crd)
}

func removeSailOperatorConditions(conditions *[]metav1.Condition) {
	meta.RemoveStatusCondition(conditions, ControllerInstalledConditionType)
	meta.RemoveStatusCondition(conditions, CRDsReadyConditionType)
}

func getCRDStatusMessage(status install.Status) string {
	var message bytes.Buffer
	if _, err := message.WriteString(status.CRDMessage); err != nil {
		log.Error(err, "error writing CRD status", "status", status.CRDMessage)
	}
	for _, info := range status.CRDs {
		crdMsg := fmt.Sprintf("\n- %s: %s", info.Name, info.State)
		_, err := message.WriteString(crdMsg)
		if err != nil {
			log.Error(err, "error writing CRD message", "crd", info.Name)
		}
	}
	return message.String()
}
