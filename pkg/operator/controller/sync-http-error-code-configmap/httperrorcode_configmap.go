package sync_http_error_code_configmap

import (
	"context"
	"fmt"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"reflect"
	//"sigs.k8s.io/controller-runtime/pkg/log"

	operatorv1 "github.com/openshift/api/operator/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ensureHttpErrorCodeConfigMap ensures the http error code configmap exists for a given
// ingresscontroller and sync them between openshift-config and openshift-ingress.  Returns a Boolean
// indicating whether the configmap exists, the configmap if it does exist, and
// an error value.
func (r *reconciler) ensureHttpErrorCodeConfigMap(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *corev1.ConfigMap, error) {
	log.Info("In ensureHttpErrorCodeConfigMap")
	haveCMInOpenShiftConfig, currentConfigMapInOpenShiftConfig, err := r.currentHttpErrorCodeConfigMap(ic, "openshift-config")
	log.Info(fmt.Sprintf(" currentConfigMapInOpenShiftConfig %v", currentConfigMapInOpenShiftConfig))
	if err != nil {
		return false, nil, err
	}

	haveCMInOpenShiftIngress, currentConfigMapInOpenShiftIngress, err := r.currentHttpErrorCodeConfigMap(ic, "openshift-ingress")
	log.Info(fmt.Sprintf(" currentConfigMapInOpenShiftIngress %v", currentConfigMapInOpenShiftIngress))

	if err != nil {
		return false, nil, err
	}
	switch {
	case ic.Spec.HttpErrorCodePage != "" && !haveCMInOpenShiftConfig && !haveCMInOpenShiftIngress:
		log.Info("In case !haveCMInOpenShiftConfig && !haveCMInOpenShiftIngress:")
		return false, nil, nil
	case ic.Spec.HttpErrorCodePage != "" && !haveCMInOpenShiftConfig && haveCMInOpenShiftIngress:
		log.Info("In case !haveCMInOpenShiftConfig && haveCMInOpenShiftIngress:")
		if err := r.client.Delete(context.TODO(), currentConfigMapInOpenShiftIngress); err != nil {
			if !errors.IsNotFound(err) {
				return true, currentConfigMapInOpenShiftIngress, fmt.Errorf("failed to delete configmap: %v", err)
			}
		} else {
			log.Info("deleted configmap", "configmap", currentConfigMapInOpenShiftIngress)
		}
		return false, nil, nil
	case ic.Spec.HttpErrorCodePage != "" && haveCMInOpenShiftConfig && !haveCMInOpenShiftIngress:
		log.Info("In case haveCMInOpenShiftConfig && !haveCMInOpenShiftIngress:")
		_, currentConfigMapInOpenShiftIngress, err = desiredHttpErrorCodeConfigMap(currentConfigMapInOpenShiftConfig, deploymentRef)
		log.Info(fmt.Sprintf("currentConfigMapInOpenShiftIngress: %v", currentConfigMapInOpenShiftIngress))
		if err := r.client.Create(context.TODO(), currentConfigMapInOpenShiftIngress); err != nil {
			return false, nil, fmt.Errorf("failed to create configmap: %v", err)
		}
		log.Info("created configmap", "configmap", currentConfigMapInOpenShiftIngress)
		return r.currentHttpErrorCodeConfigMap(ic, "openshift-ingress")
	case ic.Spec.HttpErrorCodePage != "" && haveCMInOpenShiftConfig && haveCMInOpenShiftIngress:
		log.Info("In case haveCMInOpenShiftConfig && haveCMInOpenShiftIngress:")
		if updated, err := r.updateHttpErrorCodeConfigMap(currentConfigMapInOpenShiftIngress, currentConfigMapInOpenShiftConfig); err != nil {
			return true, currentConfigMapInOpenShiftIngress, fmt.Errorf("failed to update configmap: %v", err)
		} else if updated {
			log.Info("updated configmap in openshift-ingress namespace", "configmap", currentConfigMapInOpenShiftIngress)
			return r.currentHttpErrorCodeConfigMap(ic, "openshift-ingress")
		}
	}

	return true, currentConfigMapInOpenShiftIngress, nil
}

// desiredRsyslogConfigMap returns the desired  httpErrorCodePageconfigmap.  Returns a
// Boolean indicating whether a configmap is desired, as well as the configmap
// if one is desired.
func desiredHttpErrorCodeConfigMap(currentConfigMapInOpenShiftConfig *corev1.ConfigMap, deploymentRef metav1.OwnerReference) (bool, *corev1.ConfigMap, error) {
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      currentConfigMapInOpenShiftConfig.Name,
			Namespace: "openshift-ingress",
		},
		Data: currentConfigMapInOpenShiftConfig.Data,
	}
	cm.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})

	log.Info(fmt.Sprintf("desiredHttpErrorCodeConfigMap %v", cm))
	return true, &cm, nil
}

// CurrentHttpErrorCodeConfigMap returns the current httpErrorCodePage configmap.  Returns a
// Boolean indicating whether the configmap existed, the configmap if it did
// exist, and an error value.
func (r *reconciler) CurrentHttpErrorCodeConfigMap(ic *operatorv1.IngressController, namespace string) (bool, *corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	log.Info(fmt.Sprintf("currentHttpErrorCodeConfigMap IC %v", ic))
	if err := r.client.Get(context.TODO(), controller.HttpErrorCodePageConfigMapName(ic, namespace), cm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, cm, nil
}

// updateHttpErrorCodeConfigMap updates a httpErrorCodePage configmap.  Returns a Boolean indicating
// whether the configmap was updated, and an error value.
func (r *reconciler) updateHttpErrorCodeConfigMap(current, desired *corev1.ConfigMap) (bool, error) {
	if httperrorcodeConfigmapsEqual(current, desired) {
		return false, nil
	}
	updated := current.DeepCopy()
	updated.Data = desired.Data
	if err := r.client.Update(context.TODO(), updated); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

//  httperrorcodeConfigmapsEqual compares two httpErrorCodePage configmaps between openshift-config and openshift-ingress.  Returns true if the
// configmaps should be considered equal for the purpose of determining whether
// an update is necessary, false otherwise
func httperrorcodeConfigmapsEqual(a, b *corev1.ConfigMap) bool {
	if !reflect.DeepEqual(a.Data, b.Data) {
		return false
	}
	return true
}
