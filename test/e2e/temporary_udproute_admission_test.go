//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	udproutestatuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/udproute-status"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type temporaryUDPFixture struct {
	client   client.Client
	timeout  time.Duration
	interval time.Duration
}

func (fixture temporaryUDPFixture) poll(ctx context.Context, check func(context.Context) (bool, error)) error {
	return wait.PollUntilContextTimeout(ctx, fixture.interval, fixture.timeout, true, check)
}

func (fixture temporaryUDPFixture) ensure(ctx context.Context, registerCleanup func(types.UID)) error {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	key := client.ObjectKey{Name: udproutestatuscontroller.UDPRouteCRDName}
	readCtx, cancel := context.WithTimeout(ctx, fixture.timeout)
	err := fixture.client.Get(readCtx, key, crd)
	cancel()
	if apierrors.IsNotFound(err) {
		err = fixture.withAdmissionPaused(ctx, func(ctx context.Context) error {
			if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(temporaryUDPRouteCRD), 4096).Decode(crd); err != nil {
				return err
			}
			var denied error
			createErr := fixture.poll(ctx, func(ctx context.Context) (bool, error) {
				err := fixture.client.Create(ctx, crd)
				if apierrors.IsForbidden(err) {
					denied = err
					return false, nil
				}
				return true, err
			})
			if apierrors.IsAlreadyExists(createErr) {
				return fixture.client.Get(ctx, key, crd)
			}
			if createErr != nil {
				return fmt.Errorf("create temporary UDPRoute CRD: %w", errors.Join(createErr, denied))
			}
			if crd.UID == "" {
				return fmt.Errorf("created temporary UDPRoute CRD has no UID")
			}
			registerCleanup(crd.UID)
			return nil
		})
	}
	if err != nil {
		return err
	}
	uid := crd.UID
	return fixture.poll(ctx, func(ctx context.Context) (bool, error) {
		if err := fixture.client.Get(ctx, key, crd); err != nil {
			return false, err
		}
		if crd.UID != uid {
			return false, fmt.Errorf("UDPRoute CRD replaced while waiting for Established")
		}
		for _, condition := range crd.Status.Conditions {
			if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

func (fixture temporaryUDPFixture) removeOwned(ctx context.Context, uid types.UID) error {
	if uid == "" {
		return fmt.Errorf("refusing temporary UDPRoute cleanup without UID")
	}
	crd := &apiextensionsv1.CustomResourceDefinition{}
	key := client.ObjectKey{Name: udproutestatuscontroller.UDPRouteCRDName}
	readCtx, cancel := context.WithTimeout(ctx, fixture.timeout)
	err := fixture.client.Get(readCtx, key, crd)
	cancel()
	if apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if crd.UID != uid {
		return nil
	}
	return fixture.withAdmissionPaused(ctx, func(ctx context.Context) error {
		var denied error
		err := fixture.poll(ctx, func(ctx context.Context) (bool, error) {
			err := fixture.client.Delete(ctx, crd, client.Preconditions{UID: &uid})
			if apierrors.IsForbidden(err) {
				denied = err
				return false, nil
			}
			if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
				return true, nil
			}
			return true, err
		})
		if err != nil {
			return errors.Join(err, denied)
		}
		return fixture.poll(ctx, func(ctx context.Context) (bool, error) {
			current := &apiextensionsv1.CustomResourceDefinition{}
			err := fixture.client.Get(ctx, key, current)
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return err == nil && current.UID != uid, err
		})
	})
}

func (fixture temporaryUDPFixture) withAdmissionPaused(ctx context.Context, operation func(context.Context) error) (result error) {
	ctx, cancel := context.WithTimeout(ctx, fixture.timeout)
	defer cancel()
	keys := []client.ObjectKey{
		{Namespace: cvoNamespace, Name: cvoDeploymentName},
		{Namespace: operatorNamespace, Name: "ingress-operator"},
	}
	var deployments []*appsv1.Deployment
	for _, key := range keys {
		deployment := &appsv1.Deployment{}
		if err := fixture.client.Get(ctx, key, deployment); err != nil {
			return fmt.Errorf("snapshot admission owner %s: %w", key, err)
		}
		deployments = append(deployments, deployment)
	}
	var binding *admissionv1.ValidatingAdmissionPolicyBinding
	defer func() {
		if binding != nil {
			restoreCtx, restoreCancel := context.WithTimeout(context.Background(), fixture.timeout)
			result = errors.Join(result, fixture.restoreBinding(restoreCtx, binding))
			restoreCancel()
		}
		for index := len(deployments) - 1; index >= 0; index-- {
			restoreCtx, restoreCancel := context.WithTimeout(context.Background(), fixture.timeout)
			if err := fixture.scaleOwner(restoreCtx, deployments[index], deployments[index].Spec.Replicas); err != nil {
				result = errors.Join(result, fmt.Errorf("restore admission owner: %w", err))
			}
			restoreCancel()
		}
	}()
	zero := int32(0)
	for _, deployment := range deployments {
		if err := fixture.scaleOwner(ctx, deployment, &zero); err != nil {
			return fmt.Errorf("pause admission owner: %w", err)
		}
	}
	current := &admissionv1.ValidatingAdmissionPolicyBinding{}
	if err := fixture.client.Get(ctx, client.ObjectKey{Name: gwapiCRDVAPName}, current); err == nil {
		binding = current.DeepCopy()
		if err := fixture.client.Delete(ctx, current, client.Preconditions{UID: &current.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("pause temporary CRD admission binding: %w", err)
		}
		if err := fixture.poll(ctx, func(ctx context.Context) (bool, error) {
			err := fixture.client.Get(ctx, client.ObjectKey{Name: gwapiCRDVAPName}, &admissionv1.ValidatingAdmissionPolicyBinding{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}); err != nil {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	return operation(ctx)
}

func (fixture temporaryUDPFixture) restoreBinding(ctx context.Context, original *admissionv1.ValidatingAdmissionPolicyBinding) error {
	restored := original.DeepCopy()
	restored.ObjectMeta = metav1.ObjectMeta{Name: original.Name, Labels: original.Labels, Annotations: original.Annotations, OwnerReferences: original.OwnerReferences, Finalizers: original.Finalizers}
	err := fixture.client.Create(ctx, restored)
	if apierrors.IsAlreadyExists(err) {
		current := &admissionv1.ValidatingAdmissionPolicyBinding{}
		if err := fixture.client.Get(ctx, client.ObjectKeyFromObject(original), current); err != nil {
			return err
		}
		if current.DeletionTimestamp != nil || !reflect.DeepEqual(current.Spec, original.Spec) || !reflect.DeepEqual(current.Labels, original.Labels) || !reflect.DeepEqual(current.Annotations, original.Annotations) || !reflect.DeepEqual(current.OwnerReferences, original.OwnerReferences) || !reflect.DeepEqual(current.Finalizers, original.Finalizers) {
			return fmt.Errorf("admission binding changed during temporary setup; refusing overwrite")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("restore temporary CRD admission binding: %w", err)
	}
	return nil
}

func (fixture temporaryUDPFixture) scaleOwner(ctx context.Context, original *appsv1.Deployment, replicas *int32) error {
	wanted := int32(1)
	if replicas != nil {
		wanted = *replicas
	}
	err := fixture.poll(ctx, func(ctx context.Context) (bool, error) {
		current := &appsv1.Deployment{}
		if err := fixture.client.Get(ctx, client.ObjectKeyFromObject(original), current); err != nil {
			return false, err
		}
		if current.UID != original.UID {
			return false, fmt.Errorf("admission owner %s replaced; refusing scale", original.Name)
		}
		if !reflect.DeepEqual(current.Spec.Replicas, replicas) {
			current.Spec.Replicas = replicas
			if err := fixture.client.Update(ctx, current); err != nil {
				if apierrors.IsConflict(err) {
					return false, nil
				}
				return false, err
			}
			return false, nil
		}
		if current.Status.ObservedGeneration < current.Generation {
			return false, nil
		}
		if wanted != 0 {
			return current.Status.UpdatedReplicas == wanted && current.Status.AvailableReplicas == wanted, nil
		}
		selector, err := metav1.LabelSelectorAsSelector(current.Spec.Selector)
		if err != nil || selector.Empty() {
			return false, fmt.Errorf("unsafe admission owner pod selector: %v", err)
		}
		pods := &corev1.PodList{}
		if err := fixture.client.List(ctx, pods, client.InNamespace(current.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return false, err
		}
		return current.Status.Replicas == 0 && len(pods.Items) == 0, nil
	})
	if err != nil {
		return fmt.Errorf("scale temporary admission owner %s/%s to %d: %w", original.Namespace, original.Name, wanted, err)
	}
	return nil
}
