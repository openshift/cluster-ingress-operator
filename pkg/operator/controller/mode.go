package controller

import (
	"context"
	"sync"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var modeLog = logf.Logger.WithName("mode")

// SailUninstaller provides the ability to uninstall the CIO-managed Istio
// instance. The gatewayapi controller uses this interface to trigger Sail
// uninstall when transitioning to Unmanaged mode.
type SailUninstaller interface {
	UninstallSail(ctx context.Context) error
}

// IngressWakeUpMapper returns a handler.MapFunc that lists objects from
// the cache and converts them to reconcile.Requests. Used for Ingress
// wake-up watches to enqueue work when transitioning from Unmanaged to
// Managed mode.
func IngressWakeUpMapper(cacheReader client.Reader, objList client.ObjectList, listOpts ...client.ListOption) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []reconcile.Request {
		if err := cacheReader.List(ctx, objList, listOpts...); err != nil {
			modeLog.Error(err, "Failed to list objects for Ingress wake-up")
			return nil
		}
		items, err := meta.ExtractList(objList)
		if err != nil {
			modeLog.Error(err, "Failed to extract items from ObjectList")
			return nil
		}
		requests := make([]reconcile.Request, 0, len(items))
		for _, item := range items {
			obj, ok := item.(client.Object)
			if !ok {
				continue
			}
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: obj.GetNamespace(),
					Name:      obj.GetName(),
				},
			})
		}
		return requests
	}
}

// TransitionState describes the progress of a Gateway API management
// mode transition. The gatewayapi controller sets this state and the
// status controller reads it to compute ClusterOperator Progressing
// and Degraded conditions.
type TransitionState struct {
	// InProgress is true while the gatewayapi controller is
	// actively performing operations required by a mode change
	// (e.g., deleting VAP, uninstalling Sail, installing CRDs).
	InProgress bool
	// Target is the management mode that the system is
	// transitioning toward.
	Target operatorv1alpha1.GatewayAPIManagementMode
	// Error is non-nil when a required transition operation has
	// failed (e.g., VAP delete, Sail uninstall).
	Error error
}

// ModeAccessor provides thread-safe access to the resolved Gateway API
// management mode and its derived state. It is constructed once in
// operator.go and passed to the gatewayapi controller as the sole
// writer. Dependent controllers read AllowDependents and wire
// DependentPredicate into their watches.
type ModeAccessor struct {
	mu          sync.RWMutex
	gateEnabled bool

	desiredMode operatorv1alpha1.GatewayAPIManagementMode

	managed   bool
	present   bool
	compliant bool

	crdsEstablished bool

	transition TransitionState

	// lastAppliedMode tracks the mode that was last successfully
	// reconciled to completion. nil means no reconcile has completed
	// yet (first run). Used to detect actual mode transitions and
	// avoid setting InProgress on steady-state reconciles.
	lastAppliedMode *operatorv1alpha1.GatewayAPIManagementMode
}

// NewModeAccessor creates a ModeAccessor. When gateEnabled is false the
// accessor operates in legacy mode: AllowDependents tracks only whether
// the managed CRDs have been established, with no dependency on the
// Ingress CR.
func NewModeAccessor(gateEnabled bool) *ModeAccessor {
	return &ModeAccessor{
		gateEnabled: gateEnabled,
		desiredMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
	}
}

// ShouldManageCRDs reports whether the reconciler should ensure
// (create/update) Gateway API CRDs and aggregated RBAC. Returns true
// only when the resolved Managed condition is True -- i.e., the desired
// mode is Managed and takeover is not blocked.
func (m *ModeAccessor) ShouldManageCRDs() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.managed
}

// AllowDependents reports whether dependent controllers should be active.
//
// Gate OFF: true once all managed CRDs are established (legacy path).
// Gate ON:  true only when Managed + Present + Compliant are all True.
func (m *ModeAccessor) AllowDependents() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.gateEnabled {
		return m.crdsEstablished
	}
	return m.managed && m.present && m.compliant
}

// DesiredMode returns the desired management mode from the Ingress CR
// spec. Defaults to Managed when the CR is absent or the field is empty.
func (m *ModeAccessor) DesiredMode() operatorv1alpha1.GatewayAPIManagementMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.desiredMode
}

// GateEnabled returns whether the GatewayAPIManagementMode feature gate
// was enabled at operator startup.
func (m *ModeAccessor) GateEnabled() bool {
	return m.gateEnabled
}

// DependentPredicate returns a predicate that passes only when
// AllowDependents is true. Dependent controllers wire this into their
// operational watches so events are filtered when the mode disallows
// dependent processing.
func (m *ModeAccessor) DependentPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(_ client.Object) bool {
		return m.AllowDependents()
	})
}

// Update is called by the gatewayapi reconciler to refresh the mode
// accessor after computing conditions from the Ingress CR and CRD
// state.
func (m *ModeAccessor) Update(desired operatorv1alpha1.GatewayAPIManagementMode, managed, present, compliant bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.desiredMode = desired
	m.managed = managed
	m.present = present
	m.compliant = compliant
}

// SetTransitionState is called by the gatewayapi reconciler to signal
// mode transition progress. The status controller reads this to
// compute ClusterOperator Progressing and Degraded conditions.
func (m *ModeAccessor) SetTransitionState(state TransitionState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transition = state
}

// GetTransitionState returns the current mode transition state. It is
// safe for concurrent use and is read by the status controller.
func (m *ModeAccessor) GetTransitionState() TransitionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.transition
}

// GetLastAppliedMode returns the mode that was last successfully
// reconciled to completion. Returns nil if no reconcile has completed.
func (m *ModeAccessor) GetLastAppliedMode() *operatorv1alpha1.GatewayAPIManagementMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastAppliedMode
}

// SetLastAppliedMode records the mode that was successfully reconciled
// to completion. Called at the end of a successful gate-ON reconcile.
func (m *ModeAccessor) SetLastAppliedMode(mode operatorv1alpha1.GatewayAPIManagementMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastAppliedMode = &mode
}

// SetCRDsEstablished is called by the gatewayapi reconciler in the
// gate-off (legacy) path once allManagedCRDsEstablished returns true.
func (m *ModeAccessor) SetCRDsEstablished(established bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.crdsEstablished = established
}
