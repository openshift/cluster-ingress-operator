package detector_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/detector"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DegradedSupport = "DegradedSupport"
)

func Test_basicStatusModel(t *testing.T) {

	externalStatus := &detector.ExternalStatus{}

	// detector view
	reporter := externalStatus.StatusReporter(DegradedSupport)

	go func(reporter detector.StatusReporter) {
		fmt.Println("start writer")
		reporter.Update(createTestObject("test1"), true)
	}(reporter)

	time.Sleep(100 * time.Microsecond)

	// status receiver view
	go func(c <-chan event.TypedGenericEvent[client.Object]) {
		fmt.Println("start reader")
		for update := range c {
			fmt.Println("update received: " + update.Object.GetName())
		}
	}(externalStatus.Channel())

	time.Sleep(2 * time.Second)
	fmt.Println("done")
}

func createTestObject(name string) client.Object {
	return &appsv1.Deployment{
		TypeMeta: v1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: "test",
		},
	}
}
