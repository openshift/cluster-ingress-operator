// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package install

import (
	"context"
	"fmt"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

type aggregationRole struct {
	suffix string
	label  string
	verbs  []string
}

var aggregationRoles = []aggregationRole{
	{
		suffix: "admin",
		label:  "rbac.authorization.k8s.io/aggregate-to-admin",
		verbs:  []string{"*"},
	},
	{
		suffix: "edit",
		label:  "rbac.authorization.k8s.io/aggregate-to-edit",
		verbs:  []string{"create", "delete", "patch", "update"},
	},
	{
		suffix: "view",
		label:  "rbac.authorization.k8s.io/aggregate-to-view",
		verbs:  []string{"get", "list", "watch"},
	},
}

func aggregationClusterRoleName(revision, suffix string) string {
	return fmt.Sprintf("istio-crd-%s-%s", suffix, revision)
}

func rulesFromCRDNames(crdNames []string, verbs []string) []rbacv1.PolicyRule {
	groupResources := make(map[string][]string)
	var groupOrder []string
	for _, name := range crdNames {
		parts := strings.SplitN(name, ".", 2)
		if len(parts) != 2 {
			continue
		}
		plural := parts[0]
		group := parts[1]
		if _, exists := groupResources[group]; !exists {
			groupOrder = append(groupOrder, group)
		}
		groupResources[group] = append(groupResources[group], plural)
	}

	rules := make([]rbacv1.PolicyRule, 0, len(groupOrder))
	for _, group := range groupOrder {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{group},
			Resources: groupResources[group],
			Verbs:     verbs,
		})
	}
	return rules
}

func reconcileAggregationClusterRoles(ctx context.Context, cl client.Client, cm *crdManager, revision string) error {
	log := logf.FromContext(ctx)

	if cm == nil || cm.crdFS == nil {
		return nil
	}

	crds, err := cm.loadCRDsMatching(Options{IncludeAllCRDs: true}, aggregatableCRD)
	if err != nil {
		return fmt.Errorf("failed to load Istio CRDs: %w", err)
	}

	crdNames := make([]string, 0, len(crds))
	for _, crd := range crds {
		crdNames = append(crdNames, crd.Name)
	}
	if len(crdNames) == 0 {
		return nil
	}

	for _, ar := range aggregationRoles {
		name := aggregationClusterRoleName(revision, ar.suffix)
		labels := map[string]string{
			managedByLabelKey: managedByValue,
			ar.label:          "true",
		}
		rules := rulesFromCRDNames(crdNames, ar.verbs)

		desired := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: labels,
			},
			Rules: rules,
		}

		existing := &rbacv1.ClusterRole{}
		err := cl.Get(ctx, client.ObjectKeyFromObject(desired), existing)
		if apierrors.IsNotFound(err) {
			log.V(2).Info("Creating aggregation ClusterRole", "name", name)
			if err := cl.Create(ctx, desired); err != nil {
				return fmt.Errorf("failed to create aggregation ClusterRole %s: %w", name, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to get aggregation ClusterRole %s: %w", name, err)
		}

		existing.Labels = labels
		existing.Rules = rules
		log.V(2).Info("Updating aggregation ClusterRole", "name", name)
		if err := cl.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update aggregation ClusterRole %s: %w", name, err)
		}
	}
	return nil
}

func deleteAggregationClusterRoles(ctx context.Context, cl client.Client, revision string) error {
	log := logf.FromContext(ctx)

	for _, ar := range aggregationRoles {
		name := aggregationClusterRoleName(revision, ar.suffix)
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
		if err := cl.Delete(ctx, cr); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to delete aggregation ClusterRole %s: %w", name, err)
		}
		log.V(2).Info("Deleted aggregation ClusterRole", "name", name)
	}
	return nil
}
