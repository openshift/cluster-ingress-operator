package detector

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

type StatusReporter interface {
	Update(obj client.Object, status bool)
}

type StatusSource interface {
	Channel() <-chan event.TypedGenericEvent[client.Object]
}

type ExternalStatus struct {
	channel chan event.TypedGenericEvent[client.Object]

	state map[string]StatusReporter
}

func (s *ExternalStatus) StatusReporter() {}

func (s *ExternalStatus) UpdateStatus(reason string, object client.Object) {
}

func (s *ExternalStatus) Degraded(obj client.Object) {
	//s.state[obj.GetNamespace()+"/"+obj.GetName()] = true
	s.channel <- event.TypedGenericEvent[client.Object]{Object: obj}
}

func (s *ExternalStatus) Channel() <-chan event.TypedGenericEvent[client.Object] {
	return s.channel
}
