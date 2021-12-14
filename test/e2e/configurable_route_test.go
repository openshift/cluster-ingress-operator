// +build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	util "github.com/openshift/cluster-ingress-operator/pkg/util"

	rbacv1 "k8s.io/api/rbac/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	secretNamespace            = "openshift-config"
	componentRouteHashLabelKey = "ingress.operator.openshift.io/component-route-hash"
	timeout                    = time.Minute
)

// TestConfigurableRouteRBAC tests that the Configurable Route Controller is correctly creating and deleting
// roles and roleBindings based on the componentRoutes defined in the ingress resource.
func TestConfigurableRouteRBAC(t *testing.T) {
	ingress := &configv1.Ingress{}
	if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	defer func() {
		if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
			t.Fatalf("failed to get ingress resource: %v", err)
		}
		ingress.Spec.ComponentRoutes = nil
		if err := kclient.Update(context.TODO(), ingress); err != nil {
			t.Errorf("failed to restore cluster ingress.spec resource to original state: %v", err)
		}
		ingress.Status.ComponentRoutes = nil
		if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
			t.Errorf("failed to restore cluster ingress resource to original state: %v", err)
		}
	}()

	// Set the ingress.spec.
	ingress.Spec.ComponentRoutes = []configv1.ComponentRouteSpec{
		{
			Namespace: "default",
			Name:      "foo",
			Hostname:  "www.testing.com",
			ServingCertKeyPairSecret: configv1.SecretNameReference{
				Name: "foo",
			},
		},
		{
			Namespace: "default",
			Name:      "bar",
			Hostname:  "www.testing.com",
			ServingCertKeyPairSecret: configv1.SecretNameReference{
				Name: "bar",
			},
		},
	}
	if err := eventuallyUpdateIngressSpec(t, ingress.Spec); err != nil {
		t.Fatalf("error updating ingress.spec: %v", err)
	}

	// Update the status of the ingress resource to include consumers
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace: "default",
				Name:      "foo",
				ConsumingUsers: []configv1.ConsumingUser{
					"system:serviceaccount:default:foo",
					"system:serviceaccount:default:bar",
				},
				DefaultHostname:  "foo.com",
				CurrentHostnames: []configv1.Hostname{"foo.com"},
				RelatedObjects:   []configv1.ObjectReference{{}},
			},
			{
				Namespace: "default",
				Name:      "bar",
				ConsumingUsers: []configv1.ConsumingUser{
					"system:serviceaccount:default:bar",
				},
				DefaultHostname:  "bar.com",
				CurrentHostnames: []configv1.Hostname{"bar.com"},
				RelatedObjects:   []configv1.ObjectReference{{}},
			},
		},
	}
	if err := eventuallyUpdateIngressStatus(t, ingress.Status); err != nil {
		t.Fatalf("error updating ingress.status: %v", err)
	}

	// Check that a role and roleBinding are created for each Spec.ComponentRoutes entry in the openshift-config namespace
	for _, componentRoute := range ingress.Spec.ComponentRoutes {
		if err := pollForValidComponentRouteRole(t, componentRoute); err != nil {
			t.Errorf("expected role unavailable: %v", err)
		}
	}

	for _, componentRoute := range ingress.Status.ComponentRoutes {
		if err := pollForValidComponentRouteRoleBinding(t, componentRoute); err != nil {
			t.Errorf("expected roleBinding unavailable: %v", err)
		}
	}

	// Remove the "bar" consumingUser from the "foo" componentRoute and check that the roleBinding is updated
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace: "default",
				Name:      "foo",
				ConsumingUsers: []configv1.ConsumingUser{
					"system:serviceaccount:default:foo",
				},
				DefaultHostname:  "foo.com",
				CurrentHostnames: []configv1.Hostname{"foo.com"},
				RelatedObjects:   []configv1.ObjectReference{{}},
			},
			{
				Namespace: "default",
				Name:      "bar",
				ConsumingUsers: []configv1.ConsumingUser{
					"system:serviceaccount:default:bar",
				},
				DefaultHostname:  "bar.com",
				CurrentHostnames: []configv1.Hostname{"bar.com"},
				RelatedObjects:   []configv1.ObjectReference{{}},
			},
		},
	}
	if err := eventuallyUpdateIngressStatus(t, ingress.Status); err != nil {
		t.Fatalf("error updating ingress.status: %v", err)
	}

	for _, componentRoute := range ingress.Spec.ComponentRoutes {
		if err := pollForValidComponentRouteRole(t, componentRoute); err != nil {
			t.Errorf("expected role unavailable: %v", err)
		}
	}

	for _, componentRoute := range ingress.Status.ComponentRoutes {
		if err := pollForValidComponentRouteRoleBinding(t, componentRoute); err != nil {
			t.Errorf("expected roleBinding unavailable: %v", err)
		}
	}

	// Remove the `bar` componentRoute from the ingress spec and check that the related role and roleBinding are deleted
	ingress.Spec.ComponentRoutes = []configv1.ComponentRouteSpec{
		{
			Namespace: "default",
			Name:      "foo",
			Hostname:  "www.testing.com",
			ServingCertKeyPairSecret: configv1.SecretNameReference{
				Name: "foo",
			},
		},
	}
	if err := eventuallyUpdateIngressSpec(t, ingress.Spec); err != nil {
		t.Fatalf("error updating ingress.spec: %v", err)
	}

	// Make sure that the bar role and roleBinding are deleted
	listOptions := []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: util.Hash("default/bar"),
		},
		client.InNamespace(secretNamespace),
	}
	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleList{}, listOptions, 0); err != nil {
		t.Errorf("role not deleted: %v", err)
	}

	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleBindingList{}, listOptions, 0); err != nil {
		t.Errorf("roleBinding not deleted: %v", err)
	}
}

// TestConfigurableRouteNoSecretNoRBAC tests that the no roles or roleBindings are generated
// for componentRoutes that include an empty servingCertKeyPairSecret name.
func TestConfigurableRouteNoSecretNoRBAC(t *testing.T) {
	ingress := &configv1.Ingress{}
	if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	defer func() {
		if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
			t.Fatalf("failed to get ingress resource: %v", err)
		}
		ingress.Spec.ComponentRoutes = nil
		if err := kclient.Update(context.TODO(), ingress); err != nil {
			t.Errorf("failed to restore cluster ingress resource to original state: %v", err)
		}
		ingress.Status.ComponentRoutes = nil
		if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
			t.Errorf("failed to restore cluster ingress resource to original state: %v", err)
		}
	}()

	// Include the componentRoute entry in the spec.
	ingress.Spec.ComponentRoutes = []configv1.ComponentRouteSpec{
		{
			Namespace: "default",
			Name:      "foo",
			Hostname:  "www.testing.com",
			ServingCertKeyPairSecret: configv1.SecretNameReference{
				Name: "foo",
			},
		},
	}
	if err := eventuallyUpdateIngressSpec(t, ingress.Spec); err != nil {
		t.Errorf("error updating ingress.spec: %v", err)
	}

	// Include the componentRoute entry in the status.
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace: "default",
				Name:      "foo",
				ConsumingUsers: []configv1.ConsumingUser{
					"system:serviceaccount:default:foo",
				},
				DefaultHostname:  "foo.com",
				CurrentHostnames: []configv1.Hostname{"foo.com"},
				RelatedObjects:   []configv1.ObjectReference{{}},
			},
		},
	}
	if err := eventuallyUpdateIngressStatus(t, ingress.Status); err != nil {
		t.Errorf("error updating ingress.status: %v", err)
	}

	listOptions := []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: util.Hash("default/foo"),
		},
		client.InNamespace(secretNamespace),
	}

	// Confirm that a single role is created from the componentRoute
	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleList{}, listOptions, 1); err != nil {
		t.Errorf("Number of roles in list != 1")
	}

	// Confirm that a single roleBinding is created from the componentRoute
	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleBindingList{}, listOptions, 1); err != nil {
		t.Errorf("Number of roleBindingss in list != 1")
	}

	// Update the spec of the ingress resource to have an empty secretName
	ingress.Spec.ComponentRoutes = []configv1.ComponentRouteSpec{
		{
			Namespace: "default",
			Name:      "foo",
			Hostname:  "www.testing.com",
			ServingCertKeyPairSecret: configv1.SecretNameReference{
				Name: "",
			},
		},
	}
	if err := eventuallyUpdateIngressSpec(t, ingress.Spec); err != nil {
		t.Errorf("error updating ingress.spec: %v", err)
	}

	// Confirm that the created role was deleted.
	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleList{}, listOptions, 0); err != nil {
		t.Error("Number of roles in list != 0")
	}

	// Confirm that the created roleBinding was deleted.
	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleBindingList{}, listOptions, 0); err != nil {
		t.Error("Number of roleBindings in list != 0")
	}
}

// TestConfigurableRouteNoConsumingUserNoRBAC tests that the no roles or roleBindings are generated
// for componentRoutes that do not include any consumingUsers.
func TestConfigurableRouteNoConsumingUserNoRBAC(t *testing.T) {
	// Get the cluster ingress resource.
	ingress := &configv1.Ingress{}
	if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	defer func() {
		if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
			t.Fatalf("failed to get ingress resource: %v", err)
		}
		ingress.Spec.ComponentRoutes = nil
		if err := kclient.Update(context.TODO(), ingress); err != nil {
			t.Errorf("failed to restore cluster ingress resource to original state: %v", err)
		}
		ingress.Status.ComponentRoutes = nil
		if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
			t.Errorf("failed to restore cluster ingress resource to original state: %v", err)
		}
	}()

	// Include the componentRoute entry in the spec
	ingress.Spec.ComponentRoutes = []configv1.ComponentRouteSpec{
		{
			Namespace: "default",
			Name:      "foo",
			Hostname:  "www.testing.com",
			ServingCertKeyPairSecret: configv1.SecretNameReference{
				Name: "foo",
			},
		},
	}
	if err := eventuallyUpdateIngressSpec(t, ingress.Spec); err != nil {
		t.Errorf("error updating ingress.spec: %v", err)
	}

	// Update the status of the ingress resource to include consumers
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace: "default",
				Name:      "foo",
				ConsumingUsers: []configv1.ConsumingUser{
					"system:serviceaccount:default:foo",
				},
				DefaultHostname:  "foo.com",
				CurrentHostnames: []configv1.Hostname{"foo.com"},
				RelatedObjects:   []configv1.ObjectReference{{}},
			},
		},
	}
	if err := eventuallyUpdateIngressStatus(t, ingress.Status); err != nil {
		t.Errorf("error updating ingress.status: %v", err)
	}

	listOptions := []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: util.Hash("default/foo"),
		},
		client.InNamespace(secretNamespace),
	}

	// Confirm that a single role is created from the componentRoute
	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleList{}, listOptions, 1); err != nil {
		t.Errorf("Number of roles in list != 1")
	}

	// Confirm that a single roleBinding is created from the componentRoute
	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleBindingList{}, listOptions, 1); err != nil {
		t.Errorf("Number of roleBindings in list != 1")
	}

	// Remove all consumingUsers from the componentRoute.
	ingress.Status = configv1.IngressStatus{
		ComponentRoutes: []configv1.ComponentRouteStatus{
			{
				Namespace:        "default",
				Name:             "foo",
				ConsumingUsers:   []configv1.ConsumingUser{},
				DefaultHostname:  "foo.com",
				CurrentHostnames: []configv1.Hostname{"foo.com"},
				RelatedObjects:   []configv1.ObjectReference{{}},
			},
		},
	}
	if err := eventuallyUpdateIngressStatus(t, ingress.Status); err != nil {
		t.Errorf("error updating ingress.status: %v", err)

	}

	// Confirm that the created role was deleted.
	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleList{}, listOptions, 0); err != nil {
		t.Errorf("Number of roles in list != 0")
	}

	// Confirm that the created roleBinding was deleted.
	if err := pollForNumberOfEntriesInList(t, &rbacv1.RoleBindingList{}, listOptions, 0); err != nil {
		t.Errorf("Number of roleBindings in list != 0")
	}
}

func eventuallyUpdateIngressSpec(t *testing.T, ingressSpec configv1.IngressSpec) error {
	t.Helper()
	ingress := &configv1.Ingress{}
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
			t.Logf("error getting ingress: %v", err)
			return false, err
		}

		ingressSpec.DeepCopyInto(&ingress.Spec)

		if err := kclient.Update(context.TODO(), ingress); err != nil {
			t.Logf("error updating ingress.spec: %v", err)
			return false, nil
		}

		return true, nil
	})
}

func eventuallyUpdateIngressStatus(t *testing.T, ingressStatus configv1.IngressStatus) error {
	t.Helper()
	ingress := &configv1.Ingress{}
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: "", Name: "cluster"}, ingress); err != nil {
			t.Logf("error getting ingress: %v", err)
			return false, nil
		}

		ingressStatus.DeepCopyInto(&ingress.Status)

		if err := kclient.Status().Update(context.TODO(), ingress); err != nil {
			t.Logf("error updating ingress.status: %v", err)
			return false, nil
		}

		return true, nil
	})
}

func pollForNumberOfEntriesInList(t *testing.T, list client.ObjectList, listOptions []client.ListOption, expectedLength int) error {
	t.Helper()

	if !meta.IsListType(list) {
		return fmt.Errorf("%v is not a list type", list)
	}

	// Confirm that a single role is created from the componentRoute
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := kclient.List(context.TODO(), list, listOptions...); err != nil {
			t.Logf("error retrieving list: %v", err)
			return false, nil
		}

		if meta.LenList(list) != expectedLength {
			t.Logf("unexpected number of items exist: expected (%v), actual (%v)", expectedLength, meta.LenList(list))
			return false, nil
		}
		return true, nil
	})
}

func pollForValidComponentRouteRole(t *testing.T, componentRoute configv1.ComponentRouteSpec) error {
	t.Helper()
	listOptions := []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: util.Hash(fmt.Sprintf("%s/%s", componentRoute.Namespace, componentRoute.Name)),
		},
		client.InNamespace(secretNamespace),
	}

	roleList := &rbacv1.RoleList{}
	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := kclient.List(context.TODO(), roleList, listOptions...); err != nil {
			t.Logf("error retrieving roleList: %v", err)
			return false, nil
		}

		if len(roleList.Items) != 1 {
			t.Logf("unexpected number of roles exist: expected (1), actual (%v)", len(roleList.Items))
			return false, nil
		}

		role := roleList.Items[0]
		if len(role.Rules) != 1 ||
			len(role.Rules[0].Verbs) != 3 || role.Rules[0].Verbs[0] != "get" || role.Rules[0].Verbs[1] != "list" || role.Rules[0].Verbs[2] != "watch" ||
			len(role.Rules[0].APIGroups) != 1 || role.Rules[0].APIGroups[0] != "" ||
			len(role.Rules[0].Resources) != 1 || role.Rules[0].Resources[0] != "secrets" ||
			len(role.Rules[0].ResourceNames) != 1 || role.Rules[0].ResourceNames[0] != componentRoute.ServingCertKeyPairSecret.Name {

			return false, fmt.Errorf("Invalid Role generated: %v", role)
		}

		return true, nil
	})
	return err
}

func pollForValidComponentRouteRoleBinding(t *testing.T, componentRoute configv1.ComponentRouteStatus) error {
	t.Helper()
	listOptions := []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: util.Hash(fmt.Sprintf("%s/%s", componentRoute.Namespace, componentRoute.Name)),
		},
		client.InNamespace(secretNamespace),
	}

	roleBindingList := &rbacv1.RoleBindingList{}
	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := kclient.List(context.TODO(), roleBindingList, listOptions...); err != nil {
			t.Logf("error retrieving roleBindingList: %v", err)
			return false, nil
		}

		if len(roleBindingList.Items) != 1 {
			t.Logf("unexpected number of roleBindings exist: expected (1), actual (%v)", len(roleBindingList.Items))
			return false, nil
		}

		roleBinding := roleBindingList.Items[0]
		if roleBinding.RoleRef.APIGroup != "rbac.authorization.k8s.io" ||
			roleBinding.RoleRef.Kind != "Role" ||
			roleBinding.RoleRef.Name != roleBinding.GetName() {

			t.Logf("Invalid roleBinding generated: %v", roleBinding)
			return false, nil
		}

		if len(roleBinding.Subjects) != len(componentRoute.ConsumingUsers) {
			t.Logf("Invalid roleBinding generated: %v", roleBinding)
			return false, nil
		}

		for _, expectedSubject := range componentRoute.ConsumingUsers {
			found := false
			splitExpectedSubjects := strings.Split(string(expectedSubject), ":")
			for _, subject := range roleBinding.Subjects {
				if subject.Namespace == splitExpectedSubjects[2] &&
					subject.Name == splitExpectedSubjects[3] {
					found = true
					break
				}
			}
			if !found {
				return false, nil
			}
		}
		return true, nil
	})

	return err
}
