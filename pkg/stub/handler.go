package stub

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	"github.com/operator-framework/operator-sdk/pkg/sdk"

	"k8s.io/apimachinery/pkg/api/errors"
)

func NewHandler() sdk.Handler {
	return &Handler{
		manifestFactory: manifests.NewFactory(),
	}
}

type Handler struct {
	manifestFactory *manifests.Factory
}

func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	if event.Deleted {
		return nil
	}
	switch o := event.Object.(type) {
	case *ingressv1alpha1.ClusterIngress:
		return h.syncIngressUpdate(o)
	}
	return nil
}

func (h *Handler) syncIngressUpdate(ci *ingressv1alpha1.ClusterIngress) error {
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
