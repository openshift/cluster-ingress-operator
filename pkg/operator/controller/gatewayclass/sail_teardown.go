package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"helm.sh/helm/v4/pkg/release"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const sailTeardownLabel = "ingress.operator.openshift.io/sail-teardown"

type helmReleaseReader interface {
	GetRelease(context.Context, string, string) (release.Releaser, error)
}

type teardownResource struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	Namespace  string    `json:"namespace,omitempty"`
	Name       string    `json:"name"`
	UID        types.UID `json:"uid"`
}

func (r *reconciler) teardownInventoryName() types.NamespacedName {
	namespace := r.config.OperatorNamespace
	if namespace == "" {
		namespace = operatorcontroller.DefaultOperatorNamespace
	}
	return types.NamespacedName{Namespace: namespace, Name: "istio-" + operatorcontroller.IstioName("").Name + "-teardown"}
}

func (r *reconciler) teardownInventory(ctx context.Context) (*corev1.ConfigMap, []teardownResource, error) {
	var checkpoint corev1.ConfigMap
	if err := r.client.Get(ctx, r.teardownInventoryName(), &checkpoint); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if checkpoint.Labels[sailTeardownLabel] != "true" {
		return nil, nil, fmt.Errorf("cannot use foreign Sail teardown inventory %s", client.ObjectKeyFromObject(&checkpoint))
	}
	var inventory []teardownResource
	if err := json.Unmarshal([]byte(checkpoint.Data["resources"]), &inventory); err != nil {
		return nil, nil, fmt.Errorf("cannot decode Sail teardown inventory: %w", err)
	}
	return &checkpoint, inventory, nil
}

func (r *reconciler) captureSailTeardown(ctx context.Context) error {
	if r.releaseReader == nil {
		return fmt.Errorf("Sail release reader is not configured")
	}
	checkpoint, inventory, err := r.teardownInventory(ctx)
	if err != nil {
		return err
	}
	releaseName := operatorcontroller.IstioName("").Name + "-istiod"
	current, err := r.releaseReader.GetRelease(ctx, r.config.OperandNamespace, releaseName)
	if err != nil {
		return fmt.Errorf("cannot read CIO Istio release inventory: %w", err)
	}
	if current != nil {
		accessor, err := release.NewAccessor(current)
		if err != nil {
			return err
		}
		if accessor.Name() != releaseName || accessor.Namespace() != r.config.OperandNamespace {
			return fmt.Errorf("unexpected Istio release identity")
		}
		decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(accessor.Manifest()), 4096)
		for {
			var object unstructured.Unstructured
			if err := decoder.Decode(&object.Object); err != nil {
				if err == io.EOF {
					break
				}
				return fmt.Errorf("cannot decode Istio release manifest: %w", err)
			}
			if len(object.Object) == 0 || object.GetAnnotations()["helm.sh/resource-policy"] == "keep" {
				continue
			}
			mapping, err := r.client.RESTMapper().RESTMapping(object.GroupVersionKind().GroupKind(), object.GroupVersionKind().Version)
			if err != nil {
				return fmt.Errorf("cannot map release resource %s: %w", object.GetName(), err)
			}
			if mapping.Scope.Name() == meta.RESTScopeNameNamespace && object.GetNamespace() == "" {
				object.SetNamespace(r.config.OperandNamespace)
			}
			if err := r.captureTeardownResource(ctx, &object, false, &inventory); err != nil {
				return err
			}
		}
	}
	for _, suffix := range []string{"admin", "edit", "view"} {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"})
		object.SetName("istio-crd-" + suffix + "-" + operatorcontroller.IstioName("").Name)
		if err := r.captureTeardownResource(ctx, object, true, &inventory); err != nil {
			return err
		}
	}
	return r.saveTeardownInventory(ctx, checkpoint, inventory)
}

func (r *reconciler) saveTeardownInventory(ctx context.Context, checkpoint *corev1.ConfigMap, inventory []teardownResource) error {
	if len(inventory) == 0 {
		return nil
	}
	seen := map[teardownResource]bool{}
	unique := make([]teardownResource, 0, len(inventory))
	for _, resource := range inventory {
		if !seen[resource] {
			unique = append(unique, resource)
			seen[resource] = true
		}
	}
	encoded, err := json.Marshal(unique)
	if err != nil {
		return err
	}
	if checkpoint == nil {
		key := r.teardownInventoryName()
		checkpoint = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: map[string]string{sailTeardownLabel: "true"}}, Data: map[string]string{"resources": string(encoded)}}
		return r.client.Create(ctx, checkpoint)
	}
	updated := checkpoint.DeepCopy()
	updated.Data["resources"] = string(encoded)
	if checkpoint.Data["resources"] == string(encoded) {
		return nil
	}
	return r.client.Patch(ctx, updated, client.MergeFromWithOptions(checkpoint, client.MergeFromWithOptimisticLock{}))
}

func (r *reconciler) captureTeardownResource(ctx context.Context, object *unstructured.Unstructured, aggregationRole bool, inventory *[]teardownResource) error {
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	owned := object.GetLabels()["managed-by"] == "sail-library" &&
		object.GetAnnotations()["meta.helm.sh/release-name"] == operatorcontroller.IstioName("").Name+"-istiod" &&
		object.GetAnnotations()["meta.helm.sh/release-namespace"] == r.config.OperandNamespace
	if aggregationRole {
		owned = object.GetLabels()["managed-by"] == "sail-library"
	}
	if !owned {
		return fmt.Errorf("refusing Sail uninstall: release resource %s %s is not CIO-owned", object.GetKind(), client.ObjectKeyFromObject(object))
	}
	resource := teardownResource{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
	for _, existing := range *inventory {
		if existing == resource {
			return nil
		}
	}
	*inventory = append(*inventory, resource)
	return nil
}

func (r *reconciler) finishSailTeardown(ctx context.Context) error {
	checkpoint, inventory, err := r.teardownInventory(ctx)
	if err != nil || checkpoint == nil {
		return err
	}
	for _, resource := range inventory {
		object := &unstructured.Unstructured{}
		object.SetAPIVersion(resource.APIVersion)
		object.SetKind(resource.Kind)
		err := r.client.Get(ctx, types.NamespacedName{Namespace: resource.Namespace, Name: resource.Name}, object)
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return err
		}
		if object.GetUID() == resource.UID {
			return fmt.Errorf("waiting for CIO Istio release resource %s %s to disappear", resource.Kind, client.ObjectKeyFromObject(object))
		}
	}
	if err := r.client.Delete(ctx, checkpoint, client.Preconditions{UID: &checkpoint.UID}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(checkpoint), &corev1.ConfigMap{}); err == nil {
		return fmt.Errorf("waiting for Sail teardown inventory removal")
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
