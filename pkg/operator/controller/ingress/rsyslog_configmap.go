package ingress

import (
	"context"
	"fmt"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// rsyslogConfiguration is the contents for rsyslog.conf.
	rsyslogConfiguration = `$ModLoad imuxsock
$SystemLogSocketName /var/lib/rsyslog/rsyslog.sock
$ModLoad omstdout.so
*.* :omstdout:
`
)

// ensureRsyslogConfigMap ensures the rsyslog configmap exists for a given
// ingresscontroller if the access logging is enabled.  Returns a Boolean
// indicating whether the configmap exists, the configmap if it does exist, and
// an error value.
func (r *reconciler) ensureRsyslogConfigMap(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference, ingressConfig *configv1.Ingress) (bool, *corev1.ConfigMap, error) {
	wantCM, desired, err := desiredRsyslogConfigMap(ic, deploymentRef, ingressConfig)
	if err != nil {
		return false, nil, fmt.Errorf("failed to build configmap: %v", err)
	}

	haveCM, current, err := r.currentRsyslogConfigMap(ic)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !wantCM && !haveCM:
		return false, nil, nil
	case !wantCM && haveCM:
		if deleted, err := r.deleteRsyslogConfigMap(current); err != nil {
			return true, current, fmt.Errorf("failed to delete configmap: %v", err)
		} else if deleted {
			log.Info("deleted configmap", "configmap", current)
		}
	case wantCM && !haveCM:
		if created, err := r.createRsyslogConfigMap(desired); err != nil {
			return false, nil, fmt.Errorf("failed to create configmap: %v", err)
		} else if created {
			log.Info("created configmap", "configmap", desired)
		}
	case wantCM && haveCM:
		if updated, err := r.updateRsyslogConfigMap(current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update configmap: %v", err)
		} else if updated {
			log.Info("updated configmap", "configmap", desired)
		}
	}

	return r.currentRsyslogConfigMap(ic)
}

// desiredRsyslogConfigMap returns the desired rsyslog configmap.  Returns a
// Boolean indicating whether a configmap is desired, as well as the configmap
// if one is desired.
func desiredRsyslogConfigMap(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference, ingressConfig *configv1.Ingress) (bool, *corev1.ConfigMap, error) {
	accessLogging := accessLoggingForIngressController(ic)
	if accessLogging == nil || accessLogging.Destination.Type != operatorv1.ContainerLoggingDestinationType {
		return false, nil, nil
	}

	name := controller.RsyslogConfigMapName(ic)
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Data: map[string]string{
			"rsyslog.conf": rsyslogConfiguration,
		},
	}
	cm.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})

	return true, &cm, nil
}

// currentRsyslogConfigMap returns the current rsyslog configmap.  Returns a
// Boolean indicating whether the configmap existed, the configmap if it did
// exist, and an error value.
func (r *reconciler) currentRsyslogConfigMap(ic *operatorv1.IngressController) (bool, *corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	if err := r.client.Get(context.TODO(), controller.RsyslogConfigMapName(ic), cm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, cm, nil
}

// createRsyslogConfigMap creates a configmap.  Returns a Boolean indicating
// whether the configmap was created, and an error value.
func (r *reconciler) createRsyslogConfigMap(cm *corev1.ConfigMap) (bool, error) {
	if err := r.client.Create(context.TODO(), cm); err != nil {
		return false, err
	}
	return true, nil
}

// deleteRsyslogConfigMap deletes a configmap.  Returns a Boolean indicating
// whether the configmap was deleted, and an error value.
func (r *reconciler) deleteRsyslogConfigMap(cm *corev1.ConfigMap) (bool, error) {
	if err := r.client.Delete(context.TODO(), cm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// updateRsyslogConfigMap updates a configmap.  Returns a Boolean indicating
// whether the configmap was updated, and an error value.
func (r *reconciler) updateRsyslogConfigMap(current, desired *corev1.ConfigMap) (bool, error) {
	if rsyslogConfigmapsEqual(current, desired) {
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

// rsyslogConfigmapsEqual compares two rsyslog configmaps.  Returns true if the
// configmaps should be considered equal for the purpose of determining whether
// an update is necessary, false otherwise
func rsyslogConfigmapsEqual(a, b *corev1.ConfigMap) bool {
	if !reflect.DeepEqual(a.Data, b.Data) {
		return false
	}
	return true
}
