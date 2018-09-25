package stub

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorversion "github.com/openshift/cluster-ingress-operator/version"
	osv1 "github.com/openshift/cluster-version-operator/pkg/apis/operatorstatus.openshift.io/v1"
	k8sutil "github.com/operator-framework/operator-sdk/pkg/util/k8sutil"

	"github.com/operator-framework/operator-sdk/pkg/sdk"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewHandler() sdk.Handler {
	operatorNamespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		logrus.Fatalf("Failed to get watch namespace: %v", err)
	}

	operatorName, err := k8sutil.GetOperatorName()
	if err != nil {
		logrus.Fatalf("Failed to get operator name: %v", err)
	}

	return &Handler{
		manifestFactory:   manifests.NewFactory(),
		operatorNamespace: operatorNamespace,
		operatorName:      operatorName,
	}
}

type Handler struct {
	manifestFactory *manifests.Factory

	operatorNamespace string
	operatorName      string
}

func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	switch o := event.Object.(type) {
	case *ingressv1alpha1.ClusterIngress:
		if event.Deleted {
			cond := osv1.OperatorStatusCondition{
				Type:    osv1.OperatorStatusConditionTypeWorking,
				Message: fmt.Sprintf("working towards state: ClusterIngress %q deleted",
					o.Name),
			}
			if err := h.syncOperatorStatus(cond); err != nil {
				return err
			}

			logrus.Infof("Deleting ClusterIngress object: %s", o.Name)
			if err := h.deleteIngress(o); err != nil {
				return err
			}

			cond = osv1.OperatorStatusCondition{
				Type:    osv1.OperatorStatusConditionTypeDone,
				Message: fmt.Sprintf("done applying state: ClusterIngress %q deleted",
					o.Name),
			}
			if err := h.syncOperatorStatus(cond); err != nil {
				return err
			}

			return nil
		}

		cond := osv1.OperatorStatusCondition{
			Type:    osv1.OperatorStatusConditionTypeWorking,
			Message: fmt.Sprintf("working towards state: %#v", o),
		}
		if err := h.syncOperatorStatus(cond); err != nil {
			return err
		}

		if err := h.syncIngressUpdate(o); err != nil {
			return err
		}

		cond = osv1.OperatorStatusCondition{
			Type:    osv1.OperatorStatusConditionTypeDone,
			Message: fmt.Sprintf("done applying state: %#v", o),
		}
		if err := h.syncOperatorStatus(cond); err != nil {
			return err
		}

		return nil
	}
	return nil
}

func (h *Handler) syncIngressUpdate(ci *ingressv1alpha1.ClusterIngress) error {
	ns, err := h.manifestFactory.RouterNamespace()
	if err != nil {
		h.syncOperatorStatusDegraded(err)

		return fmt.Errorf("couldn't build router namespace: %v", err)
	}
	err = sdk.Create(ns)
	if err == nil {
		logrus.Infof("created router namespace %q", ns.Name)
	} else if !errors.IsAlreadyExists(err) {
		if errors.IsForbidden(err) {
			h.syncOperatorStatusDegraded(err)
		}

		return fmt.Errorf("couldn't create router namespace %q: %v", ns.Name, err)
	}

	sa, err := h.manifestFactory.RouterServiceAccount()
	if err != nil {
		h.syncOperatorStatusDegraded(err)

		return fmt.Errorf("couldn't build router service account: %v", err)
	}
	err = sdk.Create(sa)
	if err == nil {
		logrus.Infof("created router service account %s/%s", sa.Namespace, sa.Name)
	} else if !errors.IsAlreadyExists(err) {
		if errors.IsForbidden(err) {
			h.syncOperatorStatusDegraded(err)
		}

		return fmt.Errorf("couldn't create router service account %s/%s: %v", sa.Namespace, sa.Name, err)
	}

	cr, err := h.manifestFactory.RouterClusterRole()
	if err != nil {
		h.syncOperatorStatusDegraded(err)

		return fmt.Errorf("couldn't build router cluster role: %v", err)
	}
	err = sdk.Create(cr)
	if err == nil {
		logrus.Infof("created router cluster role %q", cr.Name)
	} else if !errors.IsAlreadyExists(err) {
		if errors.IsForbidden(err) {
			h.syncOperatorStatusDegraded(err)
		}

		return fmt.Errorf("couldn't create router cluster role: %v", err)
	}

	crb, err := h.manifestFactory.RouterClusterRoleBinding()
	if err != nil {
		h.syncOperatorStatusDegraded(err)

		return fmt.Errorf("couldn't build router cluster role binding: %v", err)
	}
	err = sdk.Create(crb)
	if err == nil {
		logrus.Infof("created router cluster role binding %q", crb.Name)
	} else if !errors.IsAlreadyExists(err) {
		if errors.IsForbidden(err) {
			h.syncOperatorStatusDegraded(err)
		}

		return fmt.Errorf("couldn't create router cluster role binding: %v", err)
	}

	ds, err := h.manifestFactory.RouterDaemonSet(ci)
	if err != nil {
		h.syncOperatorStatusDegraded(err)

		return fmt.Errorf("couldn't build daemonset: %v", err)
	}
	err = sdk.Create(ds)
	if errors.IsAlreadyExists(err) {
		if err = sdk.Get(ds); err != nil {
			return fmt.Errorf("couldn't get daemonset %s, %v", ds.Name, err)
		}
	} else if err != nil {
		if errors.IsForbidden(err) {
			h.syncOperatorStatusDegraded(err)
		}

		return fmt.Errorf("failed to create daemonset %s/%s: %v", ds.Namespace, ds.Name, err)
	} else {
		logrus.Infof("created router daemonset %s/%s", ds.Namespace, ds.Name)
	}

	if ci.Spec.HighAvailability != nil {
		switch ci.Spec.HighAvailability.Type {
		case ingressv1alpha1.CloudClusterIngressHA:
			service, err := h.manifestFactory.RouterServiceCloud(ci)
			if err != nil {
				h.syncOperatorStatusDegraded(err)

				return fmt.Errorf("couldn't build service: %v", err)
			}
			trueVar := true
			dsRef := metav1.OwnerReference{
				APIVersion: ds.APIVersion,
				Kind:       ds.Kind,
				Name:       ds.Name,
				UID:        ds.UID,
				Controller: &trueVar,
			}
			service.SetOwnerReferences([]metav1.OwnerReference{dsRef})

			err = sdk.Create(service)
			if err == nil {
				logrus.Infof("created router service %s/%s", service.Namespace, service.Name)
			} else if !errors.IsAlreadyExists(err) {
				if errors.IsForbidden(err) {
					h.syncOperatorStatusDegraded(err)
				}

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

// syncOperatorStatus creates or updates the OperatorStatus object for the
// operator by setting the specified condition.
func (h *Handler) syncOperatorStatus(condition osv1.OperatorStatusCondition) error {
	status := &osv1.OperatorStatus{
		TypeMeta: metav1.TypeMeta{
			Kind:       "OperatorStatus",
			APIVersion: "operatorstatus.openshift.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: h.operatorNamespace,
			Name:      h.operatorName,
		},
	}

	err := sdk.Get(status)
	isNotFound := errors.IsNotFound(err)
	if err != nil && !isNotFound {
		logrus.Errorf("Failed to get OperatorStatus: %v", err)

		return err
	}

	status.Condition = condition
	status.Version = operatorversion.Version
	status.LastUpdate = metav1.Now()

	if isNotFound {
		err := sdk.Create(status)
		if err != nil {
			logrus.Errorf("Failed to create OperatorStatus: %v",
				err)

			return err
		}

		logrus.Infof("Created OperatorStatus %q (UID %v)",
			status.Name, status.UID)

		return nil
	}

	err = sdk.Update(status)
	if err != nil {
		logrus.Errorf("Failed to update OperatorStatus: %v", err)

		return err
	}

	return nil
}

// syncOperatorStatusDegraded updates the OperatorStatus to Degraded.
func (h *Handler) syncOperatorStatusDegraded(ierr error) error {
	cond := osv1.OperatorStatusCondition{
		Type:    osv1.OperatorStatusConditionTypeDegraded,
		Message: fmt.Sprintf("error syncing: %v", ierr),
	}

	if err := h.syncOperatorStatus(cond); err != nil {
		return err
	}

	return nil
}
