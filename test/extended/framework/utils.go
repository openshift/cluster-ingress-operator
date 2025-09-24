package framework

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	admissionapi "k8s.io/pod-security-admission/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type cleanupFunc func(ctx context.Context) error

// createNamespace creates a namespace with the specified name and registers a
// cleanup handler to delete the namespace when the test finishes.
//
// After creating the namespace, this function waits for the "default"
// ServiceAccount and "system:image-pullers" RoleBinding to be created as well,
// which is necessary in order for pods in the new namespace to be able to pull
// images.
// This function came originally from test/e2e/util_test.go and we should refactor it to
// be used by both tests
func CreateNamespace(ctx context.Context, name string, podsecuritylevel admissionapi.Level, kclient client.Client) (*corev1.Namespace, cleanupFunc, error) {
	Infof("Creating namespace %q...", name)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}

	if len(podsecuritylevel) > 0 {
		if ns.Labels == nil {
			ns.Labels = make(map[string]string)
		}
		ns.Labels[admissionapi.EnforceLevelLabel] = string(podsecuritylevel)
		// In contrast to upstream, OpenShift sets a global default on warn and audit pod security levels.
		// Since this would cause unwanted audit log and warning entries, we are setting the same level as for enforcement.
		ns.Labels[admissionapi.WarnLevelLabel] = string(podsecuritylevel)
		ns.Labels[admissionapi.AuditLevelLabel] = string(podsecuritylevel)
		ns.Labels["security.openshift.io/scc.podSecurityLabelSync"] = "false"
	}

	if err := kclient.Create(ctx, ns); err != nil {
		return nil, nil, err
	}

	cleanupFunc := func(ctx context.Context) error {
		Infof("Deleting namespace %q...", name)
		if err := kclient.Delete(ctx, ns); err != nil {
			return fmt.Errorf("failed to delete namespace %s: %v", ns.Name, err)
		}
		return nil
	}

	saName := types.NamespacedName{
		Namespace: name,
		Name:      "default",
	}
	Infof("Waiting for ServiceAccount %s to be provisioned...", saName)
	if err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		var sa corev1.ServiceAccount
		if err := kclient.Get(ctx, saName, &sa); err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		for _, s := range sa.Secrets {
			if strings.Contains(s.Name, "dockercfg") {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return nil, cleanupFunc, fmt.Errorf("timed out waiting for ServiceAccount %s to be provisioned: %v", saName, err)
	}

	rbName := types.NamespacedName{
		Namespace: name,
		Name:      "system:image-pullers",
	}
	Infof("Waiting for RoleBinding %s to be created...", rbName)
	if err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		var rb rbacv1.RoleBinding
		if err := kclient.Get(context.TODO(), rbName, &rb); err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}); err != nil {
		return nil, cleanupFunc, err
	}

	return ns, cleanupFunc, nil
}

// Below functions are extracted from openshift/origin exutils
// This is copied from go/src/internal/bytealg, which includes versions
// optimized for various platforms.  Those optimizations are elided here so we
// don't have to maintain them.
func IndexByteString(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// IPUrl safely converts a bare IPv4 or IPv6 into URL form with brackets
//
// This is copied from net.JoinHostPort, but without the port
// Use  net.JoinHostPort if you have host and port.
func IPUrl(host string) string {
	// We assume that host is a literal IPv6 address if host has
	// colons, and isn't already bracketed.
	if len(host) > 0 && !(host[0] == '[' && host[len(host)-1] == ']') && IndexByteString(host, ':') >= 0 {
		return "[" + host + "]"
	}
	return host
}
