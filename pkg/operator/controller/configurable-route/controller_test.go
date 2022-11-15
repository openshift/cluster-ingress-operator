package configurableroutes

import (
	"context"
	"fmt"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	util "github.com/openshift/cluster-ingress-operator/pkg/util"
	"github.com/openshift/cluster-ingress-operator/test/unit"
	"github.com/openshift/library-go/pkg/operator/events"

	rbacv1 "k8s.io/api/rbac/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/stretchr/testify/assert"
)

// Test_Reconcile verifies Reconcile
func Test_Reconcile(t *testing.T) {
	testCases := []struct {
		name                 string
		request              reconcile.Request
		ingressConfig        *configv1.Ingress
		roles                rbacv1.RoleList
		expectedRoles        rbacv1.RoleList
		expectedRoleBindings rbacv1.RoleBindingList
		expectError          bool
	}{
		{
			name: "ingress config doesn't exist",
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
			},
			expectError: false,
		},
		{
			name: "role and rolebinding should be created from component route",
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
			},
			ingressConfig: &configv1.Ingress{
				ObjectMeta: v1.ObjectMeta{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
				Spec: configv1.IngressSpec{
					ComponentRoutes: []configv1.ComponentRouteSpec{
						{
							Name:      "foo",
							Namespace: "openshift-config",
							ServingCertKeyPairSecret: configv1.SecretNameReference{
								Name: "foo-cert",
							},
						},
					},
				},
				Status: configv1.IngressStatus{
					ComponentRoutes: []configv1.ComponentRouteStatus{
						{
							Name:      "foo",
							Namespace: "openshift-config",
							ConsumingUsers: []configv1.ConsumingUser{
								"system:serviceaccount:openshift-config:foo-cert",
							},
						},
					},
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCertWithGeneratedHash("foo", "openshift-config", "foo-cert"),
				},
			},
			expectedRoleBindings: rbacv1.RoleBindingList{
				Items: []rbacv1.RoleBinding{
					*newRoleBindingForServiceCertWithGeneratedHash("foo", "openshift-config", "foo", "foo-cert", "openshift-config"),
				},
			},
		},
		{
			name: "extra role should be cleaned up",
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
			},
			ingressConfig: &configv1.Ingress{
				ObjectMeta: v1.ObjectMeta{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
				Spec: configv1.IngressSpec{
					ComponentRoutes: []configv1.ComponentRouteSpec{
						{
							Name:      "foo",
							Namespace: "openshift-config",
							ServingCertKeyPairSecret: configv1.SecretNameReference{
								Name: "foo-cert",
							},
						},
					},
				},
				Status: configv1.IngressStatus{
					ComponentRoutes: []configv1.ComponentRouteStatus{
						{
							Name:      "foo",
							Namespace: "openshift-config",
							ConsumingUsers: []configv1.ConsumingUser{
								"system:serviceaccount:openshift-config:foo-cert",
							},
						},
					},
				},
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("bar", "openshift-config", "bar-hash", "foo-cert"),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCertWithGeneratedHash("foo", "openshift-config", "foo-cert"),
				},
			},
			expectedRoleBindings: rbacv1.RoleBindingList{
				Items: []rbacv1.RoleBinding{
					*newRoleBindingForServiceCertWithGeneratedHash("foo", "openshift-config", "foo", "foo-cert", "openshift-config"),
				},
			},
		},
		{
			name: "invalid component route due to invalid consuming user",
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
			},
			ingressConfig: &configv1.Ingress{
				ObjectMeta: v1.ObjectMeta{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
				Spec: configv1.IngressSpec{
					ComponentRoutes: []configv1.ComponentRouteSpec{
						{
							Name:      "foo",
							Namespace: "openshift-config",
							ServingCertKeyPairSecret: configv1.SecretNameReference{
								Name: "foo-cert",
							},
						},
					},
				},
				Status: configv1.IngressStatus{
					ComponentRoutes: []configv1.ComponentRouteStatus{
						{
							Name:      "foo",
							Namespace: "openshift-config",
							ConsumingUsers: []configv1.ConsumingUser{
								"missing:a:colon",
							},
						},
					},
				},
			},
			expectedRoles:        rbacv1.RoleList{},
			expectedRoleBindings: rbacv1.RoleBindingList{},
		},
		{
			name: "invalid component route due to missing ServingCertificateName reference",
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
			},
			ingressConfig: &configv1.Ingress{
				ObjectMeta: v1.ObjectMeta{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
				Spec: configv1.IngressSpec{
					ComponentRoutes: []configv1.ComponentRouteSpec{
						{
							Name:                     "foo",
							Namespace:                "openshift-config",
							ServingCertKeyPairSecret: configv1.SecretNameReference{},
						},
					},
				},
				Status: configv1.IngressStatus{
					ComponentRoutes: []configv1.ComponentRouteStatus{
						{
							Name:      "foo",
							Namespace: "openshift-config",
							ConsumingUsers: []configv1.ConsumingUser{
								"system:serviceaccount:foo:foo",
							},
						},
					},
				},
			},
			expectedRoles:        rbacv1.RoleList{},
			expectedRoleBindings: rbacv1.RoleBindingList{},
		},
		{
			name: "invalid component route due to namespaces not matching",
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
			},
			ingressConfig: &configv1.Ingress{
				ObjectMeta: v1.ObjectMeta{
					Name:      "cluster",
					Namespace: "openshift-ingress-operator",
				},
				Spec: configv1.IngressSpec{
					ComponentRoutes: []configv1.ComponentRouteSpec{
						{
							Name:                     "foo",
							Namespace:                "openshift-config",
							ServingCertKeyPairSecret: configv1.SecretNameReference{},
						},
					},
				},
				Status: configv1.IngressStatus{
					ComponentRoutes: []configv1.ComponentRouteStatus{
						{
							Name:      "foo",
							Namespace: "not-openshift-config",
							ConsumingUsers: []configv1.ConsumingUser{
								"system:serviceaccount:foo:foo",
							},
						},
					},
				},
			},
			expectedRoles:        rbacv1.RoleList{},
			expectedRoleBindings: rbacv1.RoleBindingList{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeReconciler()
			if tc.ingressConfig != nil {
				if err := r.client.Create(context.Background(), tc.ingressConfig); err != nil {
					t.Errorf("error creating ingress config: %v", err)
				}
			}

			if _, err := r.Reconcile(context.Background(), tc.request); err == nil && tc.expectError {
				t.Errorf("expected error, got no error")
			} else if err != nil && !tc.expectError {
				t.Errorf("did not expect error: %v", err)
			} else {
				assertRolesEqual(t, r.client, &tc.expectedRoles)
				assertRoleBindingsEqual(t, r.client, &tc.expectedRoleBindings)
			}
		})
	}
}

// Test_deleteOrphanedRoles verifies deleteOrphanedRoles
func Test_deleteOrphanedRoles(t *testing.T) {
	testCases := []struct {
		name           string
		existingHashes sets.String
		roles          rbacv1.RoleList
		expectedRoles  rbacv1.RoleList
		expectError    bool
	}{
		{
			name: "all roles valid",
			existingHashes: sets.String{
				"foo-hash": sets.Empty{},
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "foo-hash", ""),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "foo-hash", ""),
				},
			},
		},
		{
			name: "foo-role doesn't have a hash",
			existingHashes: sets.String{
				"foo-hash": sets.Empty{},
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "", ""),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "", ""),
				},
			},
		},
		{
			name: "foo-role has outdated hash",
			existingHashes: sets.String{
				"foo-hash": sets.Empty{},
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "bar-hash", ""),
				},
			},
			expectedRoles: rbacv1.RoleList{},
		},
		{
			name:           "No existing hashes list",
			existingHashes: sets.String{},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "foo-hash", ""),
				},
			},
			expectedRoles: rbacv1.RoleList{},
		},
		{
			name: "foo-role is in a different namespace, but matches existingHashes",
			existingHashes: sets.String{
				"foo-hash": sets.Empty{},
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "not-openshift-config", "foo-hash", ""),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "not-openshift-config", "foo-hash", ""),
				},
			},
		},
		{
			name: "foo-role is in a different namespace, but doesn't match existingHashes",
			existingHashes: sets.String{
				"foo-hash": sets.Empty{},
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "not-openshift-config", "bar-hash", ""),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "not-openshift-config", "bar-hash", ""),
				},
			},
		},
		{
			name: "multiple roles that both match and don't match",
			existingHashes: sets.String{
				"foo-hash": sets.Empty{},
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "not-openshift-config", "bar-hash", ""),
					*newRoleForServiceCert("bar-role", "openshift-config", "foo-hash", ""),
					*newRoleForServiceCert("baz-role", "openshift-config", "bar-hash", ""),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "not-openshift-config", "bar-hash", ""),
					*newRoleForServiceCert("bar-role", "openshift-config", "foo-hash", ""),
				},
			},
		},
		{
			name:           "no roles",
			existingHashes: sets.String{},
			roles:          rbacv1.RoleList{},
			expectedRoles:  rbacv1.RoleList{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeReconciler()
			for _, role := range tc.roles.Items {
				if err := r.client.Create(context.Background(), &role); err != nil {
					t.Errorf("error creating role: %v", err)
				}
			}

			if errs := r.deleteOrphanedRoles(nil, tc.existingHashes); len(errs) == 0 && tc.expectError {
				t.Errorf("expected errors, got no errors")
			} else if len(errs) != 0 && !tc.expectError {
				t.Errorf("did not expect errors: %v", errs)
			}
			assertRolesEqual(t, r.client, &tc.expectedRoles)
		})
	}
}

// Test_ensureServiceCertKeyPairSecretRole verifies ensureServiceCertKeyPairSecretRole
func Test_ensureServiceCertKeyPairSecretRole(t *testing.T) {
	testCases := []struct {
		name           string
		componentRoute aggregatedComponentRoute
		roles          rbacv1.RoleList
		expectedRoles  rbacv1.RoleList
		expectError    bool
	}{
		{
			name: "service cert role doesn't exist",
			componentRoute: aggregatedComponentRoute{
				Name:                   "foo",
				Hash:                   "foo-hash",
				ServingCertificateName: "foo-cert",
			},
			roles: rbacv1.RoleList{},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", "foo-cert"),
				},
			},
		},
		{
			name: "service cert role exists with the correct rules",
			componentRoute: aggregatedComponentRoute{
				Name:                   "foo",
				Hash:                   "foo-hash",
				ServingCertificateName: "foo-cert",
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", "foo-cert"),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", "foo-cert"),
				},
			},
		},
		{
			name: "service cert role exists with the correct rules, but a different name",
			componentRoute: aggregatedComponentRoute{
				Name:                   "foo",
				Hash:                   "foo-hash",
				ServingCertificateName: "foo-cert",
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "foo-hash", "foo-cert"),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "foo-hash", "foo-cert"),
				},
			},
		},
		{
			name: "service cert role exists with the correct rules, but a different hash",
			componentRoute: aggregatedComponentRoute{
				Name:                   "foo",
				Hash:                   "foo-hash",
				ServingCertificateName: "foo-cert",
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "bar-hash", "foo-cert"),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo-role", "openshift-config", "bar-hash", "foo-cert"),
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", "foo-cert"),
				},
			},
		},
		{
			name: "service cert role exists with the correct rules, but in a different namespace",
			componentRoute: aggregatedComponentRoute{
				Name:                   "foo",
				Hash:                   "foo-hash",
				ServingCertificateName: "foo-cert",
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("bar", "not-openshift-config", "bar-hash", "bar-cert"),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", "foo-cert"),
					*newRoleForServiceCert("bar", "not-openshift-config", "bar-hash", "bar-cert"),
				},
			},
		},
		{
			name: "service cert role exists without rules",
			componentRoute: aggregatedComponentRoute{
				Name:                   "foo",
				Hash:                   "foo-hash",
				ServingCertificateName: "foo-cert",
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", ""),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", "foo-cert"),
				},
			},
		},
		{
			name: "service cert role exists with incorrect rules",
			componentRoute: aggregatedComponentRoute{
				Name:                   "foo",
				Hash:                   "foo-hash",
				ServingCertificateName: "foo-cert",
			},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					{
						ObjectMeta: v1.ObjectMeta{
							Name:      "foo",
							Namespace: "openshift-config",
							Labels: map[string]string{
								"ingress.operator.openshift.io/component-route-hash": "foo-hash",
							},
						},
						Rules: []rbacv1.PolicyRule{
							{
								Verbs:         []string{"get", "list", "delete"},
								APIGroups:     []string{""},
								Resources:     []string{"secrets"},
								ResourceNames: []string{"bar-cert"},
							},
						},
					},
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", "foo-cert"),
				},
			},
		},
		{
			name:           "no component route with a role",
			componentRoute: aggregatedComponentRoute{},
			roles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", ""),
				},
			},
			expectedRoles: rbacv1.RoleList{
				Items: []rbacv1.Role{
					*newRoleForServiceCert("foo", "openshift-config", "foo-hash", ""),
				},
			},
		},
		{
			name:           "no component route with no roles",
			componentRoute: aggregatedComponentRoute{},
			roles:          rbacv1.RoleList{},
			expectedRoles:  rbacv1.RoleList{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeReconciler()
			for _, role := range tc.roles.Items {
				if err := r.client.Create(context.Background(), &role); err != nil {
					t.Errorf("error creating role: %v", err)
				}
			}

			if _, err := r.ensureServiceCertKeyPairSecretRole(tc.componentRoute); err == nil && tc.expectError {
				t.Errorf("expected error, got no error")
			} else if err != nil && !tc.expectError {
				t.Errorf("did not expect error: %v", err)
			} else {
				assertRolesEqual(t, r.client, &tc.expectedRoles)
			}
		})
	}
}

// Test_ensureServiceCertKeyPairSecretRoleBinding verifies ensureServiceCertKeyPairSecretRoleBinding
func Test_ensureServiceCertKeyPairSecretRoleBinding(t *testing.T) {
	testCases := []struct {
		name                string
		componentRoute      aggregatedComponentRoute
		role                *rbacv1.Role
		roleBinding         *rbacv1.RoleBinding
		expectedRoleBinding *rbacv1.RoleBinding
		expectError         bool
	}{
		{
			name:           "empty role",
			componentRoute: aggregatedComponentRoute{},
			role:           nil,
			expectError:    true,
		},
		{
			name: "no role binding exist, but valid componentRoute and role",
			componentRoute: aggregatedComponentRoute{
				Name: "foo",
				Hash: "foo-hash",
				ConsumingUsers: []configv1.ConsumingUser{
					"system:serviceaccount:openshift-config:foo-cert",
				},
			},
			role:                newRoleForServiceCert("foo", "openshift-config", "foo-hash", ""),
			expectedRoleBinding: newRoleBindingForServiceCert("foo", "openshift-config", "foo", "foo-cert", "openshift-config", "foo-hash"),
		},
		{
			name: "valid role binding already exist, don't change",
			componentRoute: aggregatedComponentRoute{
				Name: "foo",
				Hash: "foo-hash",
				ConsumingUsers: []configv1.ConsumingUser{
					"system:serviceaccount:openshift-config:foo-cert",
				},
			},
			role:                newRoleForServiceCert("foo", "openshift-config", "foo-hash", ""),
			roleBinding:         newRoleBindingForServiceCert("foo", "openshift-config", "foo", "foo-cert", "openshift-config", "foo-hash"),
			expectedRoleBinding: newRoleBindingForServiceCert("foo", "openshift-config", "foo", "foo-cert", "openshift-config", "foo-hash"),
		},
		{
			name: "invalid role binding already exist, add update",
			componentRoute: aggregatedComponentRoute{
				Name: "foo",
				Hash: "foo-hash",
				ConsumingUsers: []configv1.ConsumingUser{
					"system:serviceaccount:openshift-config:foo-cert",
				},
			},
			role: newRoleForServiceCert("foo", "openshift-config", "foo-hash", ""),
			roleBinding: &rbacv1.RoleBinding{
				ObjectMeta: v1.ObjectMeta{
					Name:      "foo",
					Namespace: "openshift-config",
					Labels: map[string]string{
						componentRouteHashLabelKey: "foo-hash",
					},
					OwnerReferences: []v1.OwnerReference{},
				},
				Subjects: []rbacv1.Subject{},
				RoleRef:  rbacv1.RoleRef{},
			},
			expectedRoleBinding: newRoleBindingForServiceCert("foo", "openshift-config", "foo", "foo-cert", "openshift-config", "foo-hash"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var r *reconciler
			r = newFakeReconciler()

			if tc.role != nil {
				if err := r.client.Create(context.Background(), tc.role); err != nil {
					t.Errorf("error creating role: %v", err)
				}
			}
			if tc.roleBinding != nil {
				if err := r.client.Create(context.Background(), tc.roleBinding); err != nil {
					t.Errorf("error creating roleBinding: %v", err)
				}
			}

			if err := r.ensureServiceCertKeyPairSecretRoleBinding(tc.role, tc.componentRoute); tc.expectError {
				if err == nil {
					t.Errorf("expected error, got no error")
				}
			} else if err != nil && !tc.expectError {
				t.Errorf("did not expect error: %v", err)
			} else {
				actualRoleBinding := &rbacv1.RoleBinding{}
				actualRoleBindingName := types.NamespacedName{Name: tc.role.Name, Namespace: tc.role.Namespace}
				if err := r.client.Get(context.Background(), actualRoleBindingName, actualRoleBinding); err == nil {
					actualRoleBinding.TypeMeta = v1.TypeMeta{}
					actualRoleBinding.ResourceVersion = ""
					assert.Equal(t, tc.expectedRoleBinding, actualRoleBinding)
				} else {
					t.Errorf("error retrieving rolebinding from client: %v", err)
				}
			}
		})
	}
}

// newFakeReconciler builds a reconciler object for configurable-route based on fake clients and caches.
func newFakeReconciler(initObjs ...client.Object) *reconciler {
	client, kclient := unit.NewFakeClient(initObjs...)
	cache := unit.NewFakeCache(client)
	recorder := events.NewLoggingEventRecorder(ControllerName)
	r := reconciler{
		client: client,
		cache:  cache,
		config: Config{
			SecretNamespace: "openshift-config",
		},
		kclient:       kclient,
		eventRecorder: recorder,
	}
	return &r
}

// newRoleForServiceCert returns a new role with a name, namespace, component-route-hash, and policy rules.
func newRoleForServiceCert(name, namespace, hash, servingCertificateName string) *rbacv1.Role {
	role := rbacv1.Role{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	if len(hash) != 0 {
		role.ObjectMeta.Labels = map[string]string{
			"ingress.operator.openshift.io/component-route-hash": hash,
		}
	}
	role.Rules = []rbacv1.PolicyRule{
		{
			Verbs:         []string{"get", "list", "watch"},
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			ResourceNames: []string{servingCertificateName},
		},
	}
	role.GenerateName = name + "-"
	return &role
}

// newRoleForServiceCertWithGeneratedHash returns a new role with a name, namespace, component-route-hash WITH a
// correctly generated hash, and policy rules.
func newRoleForServiceCertWithGeneratedHash(name, namespace, servingCertificateName string) *rbacv1.Role {
	role := newRoleForServiceCert(name, namespace, "", servingCertificateName)
	role.ObjectMeta.Labels = map[string]string{
		"ingress.operator.openshift.io/component-route-hash": util.Hash(namespacedName(role.Namespace, role.Name)),
	}
	return role
}

// newRoleBindingForServiceCert creates a role binding that links binds a role to a cert.
func newRoleBindingForServiceCert(name, namespace, roleName, subjectName, subjectNamespace, hash string) *rbacv1.RoleBinding {
	roleBinding := rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []v1.OwnerReference{
				{
					APIVersion: "rbac.authorization.k8s.io/v1",
					Kind:       "Role",
					Name:       roleName,
				},
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				APIGroup:  "",
				Name:      subjectName,
				Namespace: subjectNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     roleName,
			APIGroup: rbacv1.GroupName,
		},
	}
	if len(hash) != 0 {
		roleBinding.ObjectMeta.Labels = map[string]string{
			"ingress.operator.openshift.io/component-route-hash": hash,
		}
	}

	return &roleBinding
}

// newRoleBindingForServiceCertWithGeneratedHash creates a role binding that links binds a role to a cert.
func newRoleBindingForServiceCertWithGeneratedHash(name, namespace, roleName, subjectName, subjectNamespace string) *rbacv1.RoleBinding {
	roleBinding := newRoleBindingForServiceCert(name, namespace, roleName, subjectName, subjectNamespace, "")
	roleBinding.ObjectMeta.Labels = map[string]string{
		"ingress.operator.openshift.io/component-route-hash": util.Hash(namespacedName(namespace, roleName)),
	}
	return roleBinding
}

// assertRolesEqual compares provided expectedRoles to the actual roles that exist in the client
// It ignores generated metadata fields such as ResourceVersion and GenerateName.
func assertRolesEqual(t *testing.T, client client.Client, expectedRoles *rbacv1.RoleList) {
	t.Helper()

	actualRoles := &rbacv1.RoleList{}
	if err := client.List(context.Background(), actualRoles); err == nil {
		for _, expectedRole := range expectedRoles.Items {
			// Find the actual role that we should compare status with this expected role
			var actualRole rbacv1.Role
			for _, role := range actualRoles.Items {
				// TRICKY: Because ensureServiceCertKeyPairSecretRole *sometimes* generates a name for the
				//         role, we need to compare both name and generated name.
				if role.GenerateName == expectedRole.GenerateName || role.Name == expectedRole.Name {
					actualRole = role
					expectedRole.Name = role.Name
				}
			}
			if assert.NotEmptyf(t, actualRole, fmt.Sprintf("Could not find expected role: %s/%s", expectedRole.Namespace, expectedRole.Name)) {
				// Value that we want to ignore in the comparison.
				actualRole.ResourceVersion = ""
				actualRole.GenerateName = ""
				expectedRole.GenerateName = ""
				assert.Equal(t, expectedRole, actualRole)
			}
		}
	} else {
		t.Errorf("error retrieving roles from client: %v", err)
	}
}

// assertRoleBindingsEqual compares provided expectedRoleBindings to the actual roleBindings that exist in the client
// It ignores generated metadata fields such as ResourceVersion.
func assertRoleBindingsEqual(t *testing.T, client client.Client, expectedRoleBindings *rbacv1.RoleBindingList) {
	t.Helper()

	actualRoleBindings := &rbacv1.RoleBindingList{}
	if err := client.List(context.Background(), actualRoleBindings); err == nil {
		for _, expectedRoleBinding := range expectedRoleBindings.Items {
			// Find the actual role that we should compare status with this expected role
			var actualRoleBinding rbacv1.RoleBinding
			for _, roleBinding := range actualRoleBindings.Items {
				// TRICKY: RoleBinding will have generated name, so we will look for the prefix.
				if strings.HasPrefix(roleBinding.Name, expectedRoleBinding.Name+"-") {
					actualRoleBinding = roleBinding
					expectedRoleBinding.Name = actualRoleBinding.Name
					expectedRoleBinding.RoleRef.Name = actualRoleBinding.Name
					expectedRoleBinding.OwnerReferences[0].Name = actualRoleBinding.Name
				}
			}
			if assert.NotEmptyf(t, actualRoleBinding, fmt.Sprintf("Could not find expected rolebinding: %s/%s", expectedRoleBinding.Namespace, expectedRoleBinding.Name)) {
				// Value that we want to ignore in the comparison.
				expectedRoleBinding.ResourceVersion = ""
				assert.Equal(t, expectedRoleBinding, actualRoleBinding)
			}
		}
	} else {
		t.Errorf("error retrieving rolebindings from client: %v", err)
	}
}
