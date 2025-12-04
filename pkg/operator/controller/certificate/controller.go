// The certificate controller is responsible for the following:
//
//  1. Managing a CA for minting self-signed certs.
//  2. Managing self-signed certificates for any ingresscontrollers which require them.
package certificate

import (
	"context"
	"fmt"
	"time"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"k8s.io/client-go/tools/record"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	operatorv1 "github.com/openshift/api/operator/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "certificate_controller"
)

var log = logf.Logger.WithName(controllerName)

func New(mgr manager.Manager, operatorNamespace string) (runtimecontroller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		client:            mgr.GetClient(),
		recorder:          mgr.GetEventRecorderFor(controllerName),
		operatorNamespace: operatorNamespace,
	}
	c, err := runtimecontroller.New(controllerName, mgr, runtimecontroller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1.IngressController{}, &handler.EnqueueRequestForObject{})); err != nil {
		return nil, err
	}
	return c, nil
}

type reconciler struct {
	client            client.Client
	recorder          record.EventRecorder
	operatorNamespace string
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling", "request", request)

	ca, err := r.ensureRouterCASecret()
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to ensure router CA: %v", err)
	}

	result := reconcile.Result{}
	errs := []error{}
	ingress := &operatorv1.IngressController{}
	if err := r.client.Get(ctx, request.NamespacedName, ingress); err != nil {
		if errors.IsNotFound(err) {
			// The ingress could have been deleted and we're processing a stale queue
			// item, so ignore and skip.
			log.Info("ingresscontroller not found; reconciliation will be skipped", "request", request)
		} else {
			errs = append(errs, fmt.Errorf("failed to get ingresscontroller: %v", err))
		}
	} else if !ingresscontroller.IsStatusDomainSet(ingress) {
		log.Info("ingresscontroller domain not set; reconciliation will be skipped", "request", request)
	} else {
		deployment := &appsv1.Deployment{}
		err = r.client.Get(ctx, controller.RouterDeploymentName(ingress), deployment)
		if err != nil {
			if errors.IsNotFound(err) {
				// All ingresses should have a deployment, so this one may not have been
				// created yet. Retry after a reasonable amount of time.
				log.Info("deployment not found; will retry default cert sync", "ingresscontroller", ingress.Name)
				result.RequeueAfter = 5 * time.Second
			} else {
				errs = append(errs, fmt.Errorf("failed to get deployment: %v", err))
			}
		} else {
			trueVar := true
			deploymentRef := metav1.OwnerReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deployment.Name,
				UID:        deployment.UID,
				Controller: &trueVar,
			}
			if _, err := r.ensureDefaultCertificateForIngress(ca, deployment.Namespace, deploymentRef, ingress); err != nil {
				errs = append(errs, fmt.Errorf("failed to ensure default cert for %s: %v", ingress.Name, err))
			}
			// The ingress canary verifies that the default ingress controller is functioning. Since it uses a
			// passthrough route, we mirror the default ingress controller's certificate in the canary to detect any
			// issues with that certificate, so if the default controller's cert is updated, update the canary's cert as
			// well.
			if ingress.Name == manifests.DefaultIngressControllerName {
				log.Info("Ensuring canary certificate")
				daemonset := &appsv1.DaemonSet{}
				err = r.client.Get(ctx, controller.CanaryDaemonSetName(), daemonset)
				if err != nil {
					if errors.IsNotFound(err) {
						// All ingresses should have a deployment, so this one may not have been
						// created yet. Retry after a reasonable amount of time.
						log.Info("Canary daemonset not found; will retry default cert sync")
						result.RequeueAfter = 5 * time.Second
					} else {
						errs = append(errs, fmt.Errorf("failed to get daemonset: %w", err))
					}
				} else {
					defaultCertName := controller.RouterEffectiveDefaultCertificateSecretName(ingress, deployment.Namespace)
					defaultCert := &corev1.Secret{}
					if err := r.client.Get(ctx, defaultCertName, defaultCert); err != nil {
						errs = append(errs, fmt.Errorf("failed to get certificate for canary: %w", err))
					}
					canaryCert := defaultCert.DeepCopy()
					canaryCertName := controller.RouterEffectiveDefaultCertificateSecretName(ingress, daemonset.Namespace)
					canaryRef := metav1.OwnerReference{
						APIVersion: "apps/v1",
						Kind:       "Daemonset",
						Name:       daemonset.Name,
						UID:        daemonset.UID,
						Controller: &trueVar,
					}
					canaryCert.ObjectMeta = metav1.ObjectMeta{
						Name:            canaryCertName.Name,
						Namespace:       canaryCertName.Namespace,
						OwnerReferences: []metav1.OwnerReference{canaryRef},
					}
					if err := r.client.Create(ctx, canaryCert); err != nil {
						errs = append(errs, fmt.Errorf("failed to ensure certificate for canary: %w", err))
					}
				}
			}
		}
	}

	return result, utilerrors.NewAggregate(errs)
}
