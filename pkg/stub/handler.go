package stub

import (
	"context"
	"fmt"

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
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("couldn't create router namespace: %v", err)
	}

	sa, err := h.manifestFactory.RouterServiceAccount()
	if err != nil {
		return fmt.Errorf("couldn't build router service account: %v", err)
	}
	err = sdk.Create(sa)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("couldn't create router service account: %v", err)
	}

	cr, err := h.manifestFactory.RouterClusterRole()
	if err != nil {
		return fmt.Errorf("couldn't build router cluster role: %v", err)
	}
	err = sdk.Create(cr)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("couldn't create router cluster role: %v", err)
	}

	crb, err := h.manifestFactory.RouterClusterRoleBinding()
	if err != nil {
		return fmt.Errorf("couldn't build router cluster role binding: %v", err)
	}
	err = sdk.Create(crb)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("couldn't create router cluster role binding: %v", err)
	}

	ds, err := h.manifestFactory.RouterDaemonSet(ci)
	if err != nil {
		return fmt.Errorf("couldn't build daemonset: %v", err)
	}
	err = sdk.Create(ds)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create daemonset: %v", err)
	}

	service, err := h.manifestFactory.RouterServiceCloud(ci)
	if err != nil {
		return fmt.Errorf("couldn't build service: %v", err)
	}
	err = sdk.Create(service)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create service: %v", err)
	}

	return nil
}
