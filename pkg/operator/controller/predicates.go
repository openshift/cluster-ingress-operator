package controller

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayHasOurController returns a function that will use the provided logger and
// clients, receive an object and return a boolean that represents if the provided
// object is a Gateway managed by our Gateway Class
func GatewayHasOurController(logger logr.Logger, crclient client.Reader) func(o client.Object) bool {
	key, value := IstioRevLabelKey, IstioName("").Name

	return func(o client.Object) bool {
		gateway, ok := o.(*gatewayapiv1.Gateway)
		if !ok {
			return false
		}

		if gateway.Labels[key] == value {
			return false
		}

		gatewayClassName := types.NamespacedName{
			Namespace: "", // Gatewayclasses are cluster-scoped.
			Name:      string(gateway.Spec.GatewayClassName),
		}
		var gatewayClass gatewayapiv1.GatewayClass
		if err := crclient.Get(context.Background(), gatewayClassName, &gatewayClass); err != nil {
			logger.Error(err, "failed to get gatewayclass for gateway", "gateway", gateway.Name, "namespace", gateway.Namespace, "gatewayclass", gatewayClassName.Name)
			return false
		}

		if gatewayClass.Spec.ControllerName == OpenShiftGatewayClassControllerName {
			return true
		}

		return false
	}
}
