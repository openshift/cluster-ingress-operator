package manager

import (
	"fmt"

	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/runtime"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"

	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// OperatorManager is a controller-runtime Manager which enables controllers in
// a primary (operator) manager to source events from related component
// controllers. For each requested component there is an associated manager
// containing a controller that relays generic events back to a shared Source
// which is exposed by `ComponentSource()`.
//
// Once an OperatorManager has been created, any controllers added to the manager
// can watch the component Source to start receiving events from the component
// controllers.
//
// Calling `Start()` will start all component managers asynchronously and then
// start the operator manager.
type OperatorManager struct {
	// Manager is the operator manager itself.
	manager.Manager

	// componentSource aggregates events from component controllers.
	componentSource source.Source
	// components aggregates component managers.
	components []manager.Manager
}

// ComponentOptions is a Manager configuration for a component managed by a
// OperatorManager. Generally the options should express the namespace and types
// of resources to watch in that namespace.
type ComponentOptions struct {
	// Options are the usual Manager options.
	manager.Options

	// Types are the kinds of API resources to watch.
	Types []runtime.Object
}

// ComponentManager is a Manager that exposes managed component events through a
// Source.
type ComponentManager interface {
	manager.Manager
	ComponentSource() source.Source
}

// New creates (but does not start) a new OperatorManager from configuration.
func New(restConfig *rest.Config, options manager.Options, managedComponents ...ComponentOptions) (*OperatorManager, error) {
	// Create the operator manager which is the event target for component
	// managers.
	operatorManager, err := manager.New(restConfig, options)
	if err != nil {
		return nil, err
	}

	// A channel mediates the relay from a forwarding event handler to a
	// source. All component managers will share the forwarder.
	componentEvents := make(chan event.GenericEvent)
	channelSource := &source.Channel{Source: componentEvents}
	// TODO: file a bug for this? Looks like the upstream controller never calls
	// it.
	channelSource.InjectStopChannel(make(<-chan struct{}))
	forwarder := &forwardingEventHandler{events: componentEvents}

	// Create a manager for every component and set up a watch for every type
	// defined for that component. The reconciler for the component controllers
	// does nothing.
	componentManagers := []manager.Manager{}
	for _, componentOptions := range managedComponents {
		componentManager, err := manager.New(restConfig, componentOptions.Options)
		componentController, err := controller.New(
			"component-controller-"+componentOptions.Namespace,
			componentManager, controller.Options{
				Reconciler: reconcile.Func(func(reconcile.Request) (reconcile.Result, error) { return reconcile.Result{}, nil }),
			})
		if err != nil {
			return nil, err
		}
		for _, t := range componentOptions.Types {
			if err := componentController.Watch(&source.Kind{Type: t}, forwarder); err != nil {
				return nil, fmt.Errorf("failed to create watch for component controller: %v", err)
			}
		}
		componentManagers = append(componentManagers, componentManager)
	}

	mgr := &OperatorManager{
		Manager:         operatorManager,
		componentSource: channelSource,
		components:      componentManagers,
	}
	return mgr, nil
}

// Start starts all component managers in their own goroutines and then starts
// the operator manager.
// TODO: Improve error handling when a component manager dies.
func (m *OperatorManager) Start(stop <-chan struct{}) error {
	logrus.Infof("ComponentManager starting with %d components", len(m.components))
	for _, componentManager := range m.components {
		go func() {
			logrus.Infof("starting component manager")
			if err := componentManager.Start(stop); err != nil {
				logrus.Errorf("component manager returned an error: %v", err)
			}
		}()
	}
	return m.Manager.Start(stop)
}

// ComponentSource returns a Source whose events are produced by the components
// owned by the OperatorManager.
func (m *OperatorManager) ComponentSource() source.Source {
	return m.componentSource
}

// forwardingEventHandler converts any type of event into a GenericEvent and
// sends the event to a channel. This is currently a low-fidelity event relay;
// type information is lost, as is additional context for deletes and updates.
type forwardingEventHandler struct {
	events chan event.GenericEvent
}

func (h *forwardingEventHandler) Create(e event.CreateEvent, _ workqueue.RateLimitingInterface) {
	h.events <- event.GenericEvent{Meta: e.Meta, Object: e.Object}
}
func (h *forwardingEventHandler) Update(e event.UpdateEvent, _ workqueue.RateLimitingInterface) {
	h.events <- event.GenericEvent{Meta: e.MetaNew, Object: e.ObjectNew}
}
func (h *forwardingEventHandler) Delete(e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	h.events <- event.GenericEvent{Meta: e.Meta, Object: e.Object}
}
func (h *forwardingEventHandler) Generic(e event.GenericEvent, _ workqueue.RateLimitingInterface) {
	h.events <- e
}
