package canarycertificate

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	canaryCertControllerName = "canary_certificate_controller"
)

var (
	log = logf.Logger.WithName(canaryCertControllerName)
)

// Config holds all the things necessary for the controller to run.
type Config struct {
	OperatorNamespace string
	OperandNamespace  string
}

// reconciler handles the actual canary certificate reconciliation logic in
// response to events.
type reconciler struct {
	config   Config
	client   client.Client
	recorder record.EventRecorder
}

// New creates the canary certificate controller
//
// The canary certificate controller mirrors the default ingress controller's
// certificate into the canary's namespace. It watches the canary's certificate
// and the default ingress controller's certificate.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		config:   config,
		client:   mgr.GetClient(),
		recorder: mgr.GetEventRecorderFor(canaryCertControllerName),
	}
	c, err := controller.New(canaryCertControllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	// Watch for updates on the canary's certificate.
	isCanaryCert := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == operatorcontroller.CanaryCertificateName().Name && o.GetNamespace() == operatorcontroller.CanaryCertificateName().Namespace
	})
	// Also watch for updates on the default ingress controller's certificate.
	// Because the default ingress controller's certificate name can be set at
	// runtime, a Get() needs to be done to determine the correct certificate.
	isDefaultIngressCert := predicate.NewPredicateFuncs(func(o client.Object) bool {
		if o.GetNamespace() != config.OperandNamespace {
			return false
		}

		defaultICName := types.NamespacedName{
			Namespace: config.OperatorNamespace,
			Name:      manifests.DefaultIngressControllerName,
		}
		defaultIC := &operatorv1.IngressController{}
		if err := reconciler.client.Get(context.Background(), defaultICName, defaultIC); err != nil {
			log.Error(err, "Failed to get default IngressController")
			return false
		}

		defaultCertName := operatorcontroller.RouterEffectiveDefaultCertificateSecretName(defaultIC, config.OperandNamespace)

		return o.GetName() == defaultCertName.Name
	})
	// Regardless of which certificate changed, this controller only has one
	// reconcile target: the canary certificate's secret.
	enqueueRequestForCanaryCertificate := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, a client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: operatorcontroller.CanaryCertificateName()}}
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.Secret{}, enqueueRequestForCanaryCertificate, predicate.Or(isCanaryCert, isDefaultIngressCert))); err != nil {
		return nil, err
	}
	return c, nil
}

// Reconcile ensures the canary's certificate mirrors the default ingress
// controller's certificate.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling", "request", request)
	result := reconcile.Result{}

	if _, _, err := r.ensureCanaryCertificate(ctx); err != nil {
		return result, fmt.Errorf("failed to ensure canary certificate: %w", err)
	}

	return result, nil
}

// ensureCanaryCertificate ensures the canary certificate secret exists, and
// that it uses the same certificate as the default ingress controller.
func (r *reconciler) ensureCanaryCertificate(ctx context.Context) (bool, *corev1.Secret, error) {
	defaultIngressControllerName := types.NamespacedName{
		Namespace: r.config.OperatorNamespace,
		Name:      manifests.DefaultIngressControllerName,
	}
	defaultIngressController := &operatorv1.IngressController{}
	if err := r.client.Get(ctx, defaultIngressControllerName, defaultIngressController); err != nil {
		return false, nil, err
	}
	defaultCertName := operatorcontroller.RouterEffectiveDefaultCertificateSecretName(defaultIngressController, r.config.OperandNamespace)
	defaultCert := &corev1.Secret{}
	if err := r.client.Get(ctx, defaultCertName, defaultCert); err != nil {
		return false, nil, err
	}
	canaryDaemonSet := &appsv1.DaemonSet{}
	if err := r.client.Get(ctx, operatorcontroller.CanaryDaemonSetName(), canaryDaemonSet); err != nil {
		return false, nil, err
	}
	haveCert, current, err := r.currentCanaryCertificate(ctx)
	if err != nil {
		return false, nil, err
	}
	trueVar := true
	ownerRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "DaemonSet",
		Name:       canaryDaemonSet.Name,
		UID:        canaryDaemonSet.UID,
		Controller: &trueVar,
	}
	desired := desiredCanaryCertificate(ownerRef, defaultCert)

	switch {
	case !haveCert:
		if err := r.createCanaryCertificate(ctx, desired); err != nil {
			return false, nil, err
		}
		return r.currentCanaryCertificate(ctx)
	case haveCert:
		if updated, err := r.updateCanaryCertificate(ctx, current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentCanaryCertificate(ctx)
		}
	}
	return true, current, nil
}

// currentCanaryCertificate returns the current canary certificate secret, if it exists.
func (r *reconciler) currentCanaryCertificate(ctx context.Context) (bool, *corev1.Secret, error) {
	currentCanaryCert := &corev1.Secret{}
	if err := r.client.Get(ctx, operatorcontroller.CanaryCertificateName(), currentCanaryCert); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, currentCanaryCert, nil

}

// desiredCanaryCertificate returns the desired canary certificate secret, based
// on the default ingress controller's certificate.
func desiredCanaryCertificate(canaryOwnerRef metav1.OwnerReference, defaultIngressCertificate *corev1.Secret) *corev1.Secret {
	canaryCertName := operatorcontroller.CanaryCertificateName()
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            canaryCertName.Name,
			Namespace:       canaryCertName.Namespace,
			OwnerReferences: []metav1.OwnerReference{canaryOwnerRef},
		},
		// No validation should be done here on Data or Type. The canary should
		// accurately reflect the state of the default ingress controller, so
		// either validation needs to be done at the ingresscontroller level, or
		// invalid certificates will be detected by the canary at runtime.
		Data: defaultIngressCertificate.Data,
		Type: defaultIngressCertificate.Type,
	}
}

// createCanaryCertificate creates the canary certificate, or returns an error
// if it cannot.
func (r *reconciler) createCanaryCertificate(ctx context.Context, certificate *corev1.Secret) error {
	if err := r.client.Create(ctx, certificate); err != nil {
		return err
	}

	r.recorder.Event(certificate, "Normal", "CreatedCanaryCertificate", "created canary certificate")
	return nil
}

// updateCanaryCertificate updates the canary certificate if the desired version
// differs from the current one, and returns an error if it cannot.
func (r *reconciler) updateCanaryCertificate(ctx context.Context, current, desired *corev1.Secret) (bool, error) {
	changed, updated := canaryCertificateChanged(current, desired)
	if !changed {
		return false, nil
	}
	if err := r.client.Update(ctx, updated); err != nil {
		return false, err
	}
	r.recorder.Event(updated, "Normal", "UpdatedCanaryCertificate", "updated canary certificate")
	return true, nil
}

// canaryCertificateChanged returns an updated certificate secret if the current
// secret differs from the desired one.
func canaryCertificateChanged(current, desired *corev1.Secret) (bool, *corev1.Secret) {
	changed := false
	updated := current.DeepCopy()

	if !cmp.Equal(current.OwnerReferences, desired.OwnerReferences) {
		updated.OwnerReferences = desired.OwnerReferences
		changed = true
	}
	if !cmp.Equal(current.Data, desired.Data) {
		updated.Data = desired.Data
		changed = true
	}
	if current.Type != desired.Type {
		updated.Type = desired.Type
		changed = true
	}
	if !cmp.Equal(current.Annotations, desired.Annotations, cmpopts.EquateEmpty()) {
		updated.Annotations = desired.Annotations
		changed = true
	}
	if !cmp.Equal(current.Labels, desired.Labels, cmpopts.EquateEmpty()) {
		updated.Labels = desired.Labels
		changed = true
	}

	return changed, updated
}
