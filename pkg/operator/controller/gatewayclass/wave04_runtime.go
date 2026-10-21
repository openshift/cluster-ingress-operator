package gatewayclass

import (
	"context"
	_ "embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/istio-ecosystem/sail-operator/chart/crds"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"
)

//go:embed runtime/servicemeshoperator3.clusterserviceversion.yaml
var ossmLiteRuntimeCSV []byte

const istioAccessRolePrefix = "system:openshift:gateway-api:istio-aggregate-to-"
const istioAccessOwnerLabel = "ingress.operator.openshift.io/managed-by"
const istioAccessOwner = "gateway-api"

func istioAccessRoles() ([]*rbacv1.ClusterRole, error) {
	files, err := fs.Glob(crds.FS, "*.yaml")
	if err != nil {
		return nil, err
	}
	rules := make([]rbacv1.PolicyRule, 0, len(files))
	for _, name := range files {
		content, err := fs.ReadFile(crds.FS, name)
		if err != nil {
			return nil, err
		}
		var definition apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(content, &definition); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(definition.Spec.Group, ".istio.io") && definition.Spec.Group != "sailoperator.io" {
			continue
		}
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{definition.Spec.Group}, Resources: []string{definition.Spec.Names.Plural}})
	}
	roles := make([]*rbacv1.ClusterRole, 0, 3)
	for _, role := range []struct {
		name  string
		verbs []string
	}{
		{"admin", []string{"*"}},
		{"edit", []string{"create", "delete", "patch", "update"}},
		{"view", []string{"get", "list", "watch"}},
	} {
		policy := make([]rbacv1.PolicyRule, len(rules))
		copy(policy, rules)
		for index := range policy {
			policy[index].Verbs = role.verbs
		}
		roles = append(roles, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
			Name:   istioAccessRolePrefix + role.name,
			Labels: map[string]string{"rbac.authorization.k8s.io/aggregate-to-" + role.name: "true", istioAccessOwnerLabel: istioAccessOwner},
		}, Rules: policy})
	}
	return roles, nil
}

func (r *reconciler) reconcileIstioAccess(ctx context.Context, remove bool) error {
	roles, err := istioAccessRoles()
	if err != nil {
		return err
	}
	for _, desired := range roles {
		if remove {
			current := &rbacv1.ClusterRole{}
			if err := r.client.Get(ctx, client.ObjectKeyFromObject(desired), current); apierrors.IsNotFound(err) {
				continue
			} else if err != nil {
				return err
			}
			if current.Labels[istioAccessOwnerLabel] != istioAccessOwner || len(current.OwnerReferences) != 0 {
				return fmt.Errorf("refusing to delete ClusterRole %s with conflicting ownership", current.Name)
			}
			if err := r.client.Delete(ctx, current, client.Preconditions{UID: &current.UID, ResourceVersion: &current.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			continue
		}
		current := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: desired.Name}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.client, current, func() error {
			if current.ResourceVersion != "" && (current.Labels[istioAccessOwnerLabel] != istioAccessOwner || len(current.OwnerReferences) != 0) {
				return fmt.Errorf("refusing to update ClusterRole %s with conflicting ownership", current.Name)
			}
			if current.Labels == nil {
				current.Labels = map[string]string{}
			}
			for _, role := range []string{"admin", "edit", "view"} {
				delete(current.Labels, "rbac.authorization.k8s.io/aggregate-to-"+role)
			}
			for key, value := range desired.Labels {
				current.Labels[key] = value
			}
			current.Rules = desired.Rules
			current.AggregationRule = nil
			return nil
		}); err != nil {
			return fmt.Errorf("failed to reconcile ClusterRole %s: %w", desired.Name, err)
		}
	}
	return nil
}
