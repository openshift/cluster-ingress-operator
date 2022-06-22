//go:build e2e
// +build e2e

package e2e

import (
	"context"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"testing"
	"time"
)

func TestDomainNotMatchingBase(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.Platform != configv1.AWSPlatformType {
		t.Skip("test skipped on non-aws platform")
		return
	}

	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "domain-not-matching"}
	domain := icName.Name + ".local"
	ic := newLoadBalancerController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	//Ensure the DNSManaged=False condition
	t.Logf("waiting for ingresscontroller %v", icName)
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: ingresscontroller.IngressControllerAdmittedConditionType, Status: operatorv1.ConditionTrue},
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err := getIngressController(t, kclient, icName, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	//Ensure there is no DNS record created
	wildcardRecordName := controller.WildcardDNSRecordName(ic)
	wildcardRecord := &iov1.DNSRecord{}
	if err := kclient.Get(context.TODO(), wildcardRecordName, wildcardRecord); err == nil {
		t.Fatalf("Expected to find no wildcard dnsrecord, but found one %s", wildcardRecordName)
	}
	if err != nil && !errors.IsNotFound(err) {
		t.Fatalf("Unexpected error while looking for a dnsrecord: %v", err)
	}
}
