package detector

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

type StatusReporter interface {
	Update(obj client.Object, status bool)
}

type Status struct {
	Details string
	Active  bool
}
type StatusSource interface {
	Channel() <-chan event.TypedGenericEvent[client.Object]

	GetCurrentStatus(statusName string) Status
}

type ExternalStatus struct {
	channel chan event.TypedGenericEvent[client.Object]

	state map[string]*externalStatusReporter
}

func (s *ExternalStatus) StatusReporter(statusName string) StatusReporter {
	if s.state == nil {
		s.state = map[string]*externalStatusReporter{}
	}
	if s.channel == nil {
		s.channel = make(chan event.TypedGenericEvent[client.Object])
	}
	if v, found := s.state[statusName]; found {
		return v
	}
	v := &externalStatusReporter{
		source: s.channel,
	}
	s.state[statusName] = v
	return v
}

func (s *ExternalStatus) Channel() <-chan event.TypedGenericEvent[client.Object] {
	if s.channel == nil {
		s.channel = make(chan event.TypedGenericEvent[client.Object])
	}
	return s.channel
}

func (s *ExternalStatus) GetCurrentStatus(statusName string) Status {
	if reporter, found := s.state[statusName]; found {
		if len(reporter.state) > 0 {
			return Status{
				Details: strings.Join(slices.Collect(maps.Keys(reporter.state)), ", "),
				Active:  true,
			}
		}
	}
	return Status{}
}

type externalStatusReporter struct {
	state map[string]bool

	source chan event.TypedGenericEvent[client.Object]
}

func (e *externalStatusReporter) Update(obj client.Object, active bool) {
	key := objectToKey(obj)

	if e.state == nil {
		e.state = map[string]bool{}
	}

	if previouslyActive, found := e.state[key]; found {
		if previouslyActive == active {
			return // nothing changed, move on
		}

		if !active { // if no longer active, remove & #Update
			delete(e.state, key)
			// #Update
			e.source <- event.TypedGenericEvent[client.Object]{Object: obj}
		}
	} else if active { // not recorded before and active, add & #Update
		fmt.Println("new active object")
		e.state[key] = true
		// #Update
		e.source <- event.TypedGenericEvent[client.Object]{Object: obj}
		fmt.Println("sent")
	}
}

func objectToKey(obj client.Object) string {
	return fmt.Sprintf("%s-%s/%s", obj.GetObjectKind().GroupVersionKind(), obj.GetNamespace(), obj.GetName())
}
