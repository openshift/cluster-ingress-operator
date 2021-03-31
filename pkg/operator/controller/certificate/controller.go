// The certificate controller is responsible for:
//
//   1. Managing a CA for minting self-signed certs
//   2. Managing self-signed certificates for any ingresscontrollers which require them
//   3. Publishing the CA to `openshift-config-managed`
package certificate

import (
	"context"
	"fmt"
	"time"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
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
	reconciler := &reconciler{
		client:            mgr.GetClient(),
		recorder:          mgr.GetEventRecorderFor(controllerName),
		operatorNamespace: operatorNamespace,
	}
	c, err := runtimecontroller.New(controllerName, mgr, runtimecontroller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &operatorv1.IngressController{}}, &handler.EnqueueRequestForObject{}); err != nil {
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
		}
	}

	// We need to construct the CA bundle that can be used to verify the ingress used to serve the console and the oauth-server.
	// In an operator maintained cluster, this is always `oc get -n openshift-ingress-operator ingresscontroller/default`, skip the rest and return here.
	// TODO if network-edge wishes to expand the scope of the CA bundle (and you could legitimately see a need/desire to have one CA that verifies all ingress traffic).
	// TODO this could be accomplished using union logic similar to the kube-apiserver's join of multiple CAs.
	if ingress == nil || ingress.Namespace != operatorcontroller.DefaultOperatorNamespace || ingress.Name != "default" {
		return result, utilerrors.NewAggregate(errs)
	}

	wildcardServingCertKeySecret := &corev1.Secret{}
	if err := r.client.Get(ctx, controller.RouterEffectiveDefaultCertificateSecretName(ingress, operatorcontroller.DefaultOperandNamespace), wildcardServingCertKeySecret); err != nil {
		errs = append(errs, fmt.Errorf("failed to lookup wildcard cert: %v", err))
		return result, utilerrors.NewAggregate(errs)
	}
	caBundle := string(wildcardServingCertKeySecret.Data["tls.crt"])
	if err := r.ensureDefaultIngressCertConfigMap(caBundle); err != nil {
		errs = append(errs, fmt.Errorf("failed to publish router CA: %v", err))
	}

	return result, utilerrors.NewAggregate(errs)
}
