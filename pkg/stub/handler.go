package stub

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	"github.com/operator-framework/operator-sdk/pkg/sdk"

	"k8s.io/apimachinery/pkg/api/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1 "k8s.io/api/apps/v1"
)

func NewHandler() sdk.Handler {
	return &Handler{
		manifestFactory: manifests.NewFactory(),
	}
}

type Handler struct {
	manifestFactory *manifests.Factory
}

var ci_md5 map[string][16]byte

func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	if ci_md5 == nil {
		ci_md5 = make(map[string][16]byte)
	}
	switch o := event.Object.(type) {
	case *ingressv1alpha1.ClusterIngress:
		if event.Deleted {
			logrus.Infof("Deleting ClusterIngress object: %s", o.Name)
			err := h.deleteIngress(o)
			if err != nil {
				return fmt.Errorf("error deleting ClusterIngress %s", err)
			}
			return nil
		} else {
			return h.syncIngressUpdate(o)
		}
	}
	return nil
}

func (h *Handler) syncIngressUpdate(ci *ingressv1alpha1.ClusterIngress) error {

	// Get current ClusterIngress objetc md5
	arrBytes := []byte{}
	jsonBytes, _ := json.Marshal(ci)
	arrBytes = append(arrBytes, jsonBytes...)
	curr_ci_md5 := md5.Sum(arrBytes)

	// if md5 are equal no update needed
	if ci_md5[ci.Name] == curr_ci_md5 {
		return nil

	}
	logrus.Infof("Changes for ClusterIngress %s detected, updating", ci.Name)
	ci_md5[ci.Name] = curr_ci_md5

	// NO UPDATES YET JUST ILLUSTRATING THE PATTERN

	ns, err := h.manifestFactory.RouterNamespace()
	if err != nil {
		return fmt.Errorf("couldn't build router namespace: %v", err)
	}
	err = sdk.Create(ns)
	if err == nil {
		logrus.Infof("created router namespace %q", ns.Name)
	} else if !errors.IsAlreadyExists(err) {
		return fmt.Errorf("couldn't create router namespace %q: %v", ns.Name, err)
	}

	sa, err := h.manifestFactory.RouterServiceAccount()
	if err != nil {
		return fmt.Errorf("couldn't build router service account: %v", err)
	}
	err = sdk.Create(sa)
	if err == nil {
		logrus.Infof("created router service account %s/%s", sa.Namespace, sa.Name)
	} else if !errors.IsAlreadyExists(err) {
		return fmt.Errorf("couldn't create router service account %s/%s: %v", sa.Namespace, sa.Name, err)
	}

	cr, err := h.manifestFactory.RouterClusterRole()
	if err != nil {
		return fmt.Errorf("couldn't build router cluster role: %v", err)
	}
	err = sdk.Create(cr)
	if err == nil {
		logrus.Infof("created router cluster role %q", cr.Name)
	} else if !errors.IsAlreadyExists(err) {
		return fmt.Errorf("couldn't create router cluster role: %v", err)
	}

	crb, err := h.manifestFactory.RouterClusterRoleBinding()
	if err != nil {
		return fmt.Errorf("couldn't build router cluster role binding: %v", err)
	}
	err = sdk.Create(crb)
	if err == nil {
		logrus.Infof("created router cluster role binding %q", crb.Name)
	} else if !errors.IsAlreadyExists(err) {
		return fmt.Errorf("couldn't create router cluster role binding: %v", err)
	}

	ds, err := h.manifestFactory.RouterDaemonSet(ci)
	if err != nil {
		return fmt.Errorf("couldn't build daemonset: %v", err)
	}
	err = sdk.Create(ds)
	if err == nil {
		logrus.Infof("created router daemonset %s/%s", ds.Namespace, ds.Name)
	} else if !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create daemonset %s/%s: %v", ds.Namespace, ds.Name, err)
	}

	if ci.Spec.HighAvailability != nil {
		switch ci.Spec.HighAvailability.Type {
		case ingressv1alpha1.CloudClusterIngressHA:
			service, err := h.manifestFactory.RouterServiceCloud(ci)
			if err != nil {
				return fmt.Errorf("couldn't build service: %v", err)
			}
			dsRef, err := getDaemonSetOwnerRef(ds)
			// Don't create the service unless the DaemonSet is ready
			if err != nil {
				return fmt.Errorf("failed to create service, could not get DaemonSet ownerReference: %s", err)
			}
			service.SetOwnerReferences(append(service.GetOwnerReferences(), dsRef))
			err = sdk.Create(service)
			if err == nil {
				logrus.Infof("created router service %s/%s", service.Namespace, service.Name)
			} else if !errors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create service %s/%s: %v", service.Namespace, service.Name, err)
			}
		}
	}

	return nil
}

func (h *Handler) deleteIngress(ci *ingressv1alpha1.ClusterIngress) error {
	ds, err := h.manifestFactory.RouterDaemonSet(ci)
	if err != nil {
		return fmt.Errorf("couldn't build DaemonSet object for deletion: %v", err)
	}
	return sdk.Delete(ds)
}

// getDaemonSetOwnerRef returns an object as OwnerReference
func getDaemonSetOwnerRef(ds *appsv1.DaemonSet) (metav1.OwnerReference, error) {
	var or metav1.OwnerReference
	d := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ds.Name,
			Namespace: ds.Namespace,
		},
	}

	err := sdk.Get(d)
	if err != nil {
		return or, fmt.Errorf("couldn't get DaemonSet %s Namespace %s : %s", ds.Name, ds.Namespace, err)
	}
	trueVar := true
	or = metav1.OwnerReference{
		APIVersion: d.APIVersion,
		Kind:       d.Kind,
		Name:       d.Name,
		UID:        d.UID,
		Controller: &trueVar,
	}
	return or, nil
}
