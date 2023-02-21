package crl

import (
	"context"
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ensureCRLConfigmap ensures the client CA certificate revocation list
// configmap exists for a given ingresscontroller if the ingresscontroller
// specifies a client CA certificate bundle in which any certificates specify
// any CRL distribution points.  Returns a Boolean indicating whether the
// configmap exists, the configmap if it does exist, and an error value.
func (r *reconciler) ensureCRLConfigmap(ctx context.Context, ic *operatorv1.IngressController, namespace string, ownerRef metav1.OwnerReference, haveClientCA bool, clientCAConfigmap *corev1.ConfigMap) (bool, *corev1.ConfigMap, context.Context, error) {
	haveCM, current, err := r.currentCRLConfigMap(ctx, ic)
	if err != nil {
		return false, nil, ctx, err
	}

	// The CRL management code has been moved into the router, so the CRL configmap is no longer necessary.
	// TODO: Remove this whole controller after 4.14
	wantCM := false

	switch {
	case !wantCM && !haveCM:
		return false, nil, ctx, nil
	case !wantCM && haveCM:
		if err := r.client.Delete(ctx, current); err != nil {
			if !errors.IsNotFound(err) {
				return true, current, ctx, fmt.Errorf("failed to delete configmap: %w", err)
			}
		} else {
			log.Info("deleted configmap", "namespace", current.Namespace, "name", current.Name)
		}
		return false, nil, ctx, nil
	}
	return false, nil, ctx, nil
}

// currentCRLConfigMap returns the current CRL configmap.  Returns a Boolean
// indicating whether the configmap existed, the configmap if it did exist, and
// an error value.
func (r *reconciler) currentCRLConfigMap(ctx context.Context, ic *operatorv1.IngressController) (bool, *corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, controller.CRLConfigMapName(ic), cm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, cm, nil
}
