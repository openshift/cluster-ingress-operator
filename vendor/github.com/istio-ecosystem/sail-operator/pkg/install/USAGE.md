# Install Library - CIO Usage Example

This document shows how the Cluster Ingress Operator (CIO) would wire the install library into its main and GatewayClass controller.

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│  main.go                                                     │
│                                                              │
│  1. Create install.Library                                   │
│  2. Start it (returns notifyCh)                              │
│  3. Pass Library + notifyCh to GatewayClassReconciler        │
│  4. Register notifyCh as source.Channel on the controller    │
│  5. Start manager                                            │
└──────────────────────────────────────────────────────────────┘
         │ Apply(opts)                   ▲ notifyCh signal
         ▼                               │
┌──────────────────────────────────────────────────────────────┐
│  install.Library (internal goroutines)                       │
│                                                              │
│  - Classifies + installs/updates CRDs                        │
│  - Installs istiod via Helm                                  │
│  - Watches Helm resources for drift                          │
│  - Watches CRDs for ownership changes                        │
│  - Sends signal on notifyCh after each reconciliation        │
└──────────────────────────────────────────────────────────────┘
         │ Status()                      ▲ drift / CRD event
         ▼                               │
┌──────────────────────────────────────────────────────────────┐
│  GatewayClassReconciler.Reconcile()                          │
│                                                              │
│  1. Get GatewayClass                                         │
│  2. Build values, call lib.Apply(opts)                       │
│  3. Read lib.Status()                                        │
│  4. Map status → GatewayClass conditions                     │
│  5. Update GatewayClass status                               │
└──────────────────────────────────────────────────────────────┘
```

## main.go

```go
package main

import (
	"os"

	"github.com/istio-ecosystem/sail-operator/pkg/install"
	"github.com/istio-ecosystem/sail-operator/resources"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

func main() {
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})
	if err != nil {
		os.Exit(1)
	}

	// 1. Create the install library.
	//    resources.FS contains the embedded Helm charts, profiles, and CRDs.
	lib, err := install.New(mgr.GetConfig(), resources.FS)
	if err != nil {
		os.Exit(1)
	}

	// 2. Start the library. This returns a notification channel that fires
	//    every time the library finishes a reconciliation (install, drift
	//    repair, CRD ownership change).
	//    The library sits idle until the first Apply() call.
	ctx := ctrl.SetupSignalHandler()
	notifyCh := lib.Start(ctx)

	// 3. Create the GatewayClass controller with the library injected.
	reconciler := &GatewayClassReconciler{
		Client: mgr.GetClient(),
		Lib:    lib,
	}

	// 4. Register the controller. The source.Channel watch triggers a
	//    reconcile whenever the library signals that something changed
	//    (drift repaired, CRD ownership changed, install completed).
	err = ctrl.NewControllerManagedBy(mgr).
		For(&gwapiv1.GatewayClass{}).
		WatchesRawSource(
			source.Channel(notifyCh, &handler.EnqueueRequestForObject{}),
		).
		Complete(reconciler)
	if err != nil {
		os.Exit(1)
	}

	// 5. Start the manager. This blocks until ctx is cancelled.
	if err := mgr.Start(ctx); err != nil {
		os.Exit(1)
	}
}
```

> **Note on `source.Channel`**: The channel emits a `struct{}`, not a specific object key. You may need a custom `handler.EventHandler` that enqueues a fixed key (e.g. the GatewayClass name) instead of `EnqueueRequestForObject`. See the handler section below.

## GatewayClass Controller

```go
package main

import (
	"context"
	"fmt"

	"github.com/istio-ecosystem/sail-operator/pkg/install"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	controllerName   = "openshift.io/ingress-controller"
	gatewayClassName = "openshift-default"
)

// GatewayClassReconciler reconciles GatewayClass objects and drives
// the install library to manage istiod.
type GatewayClassReconciler struct {
	client.Client
	Lib *install.Library
}

func (r *GatewayClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// 1. Get the GatewayClass.
	gc := &gwapiv1.GatewayClass{}
	if err := r.Get(ctx, req.NamespacedName, gc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only handle our GatewayClass.
	if string(gc.Spec.ControllerName) != controllerName {
		return ctrl.Result{}, nil
	}

	// 2. Build values and call Apply.
	//    Apply is idempotent — if nothing changed, it's a no-op.
	values := install.GatewayAPIDefaults()
	values.Pilot.Env["PILOT_GATEWAY_API_CONTROLLER_NAME"] = controllerName
	values.Pilot.Env["PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS_NAME"] = gatewayClassName

	r.Lib.Apply(install.Options{
		Namespace: "openshift-ingress",
		Values:    values,
	})

	// 3. Read the latest status from the library.
	status := r.Lib.Status()

	// 4. Map library status to GatewayClass conditions.
	conditions := mapStatusToConditions(status, gc.Generation)

	// 5. Update GatewayClass status.
	gc.Status.Conditions = conditions
	if err := r.Status().Update(ctx, gc); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update GatewayClass status: %w", err)
	}

	log.Info("reconciled",
		"installed", status.Installed,
		"version", status.Version,
		"crdState", status.CRDState,
	)
	return ctrl.Result{}, nil
}

// mapStatusToConditions translates the library Status into GatewayClass conditions.
func mapStatusToConditions(status install.Status, generation int64) []metav1.Condition {
	var conditions []metav1.Condition

	// Accepted condition: true if istiod is installed.
	accepted := metav1.Condition{
		Type:               string(gwapiv1.GatewayClassConditionStatusAccepted),
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}
	if status.Installed {
		accepted.Status = metav1.ConditionTrue
		accepted.Reason = "Installed"
		accepted.Message = fmt.Sprintf("istiod %s installed", status.Version)
	} else if status.Error != nil {
		accepted.Status = metav1.ConditionFalse
		accepted.Reason = "InstallFailed"
		accepted.Message = status.Error.Error()
	} else {
		accepted.Status = metav1.ConditionUnknown
		accepted.Reason = "Pending"
		accepted.Message = "waiting for first reconciliation"
	}
	conditions = append(conditions, accepted)

	// CRD condition: reflects CRD ownership state.
	crd := metav1.Condition{
		Type:               "CRDsReady",
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}
	switch status.CRDState {
	case install.CRDManagedByCIO:
		crd.Status = metav1.ConditionTrue
		crd.Reason = "ManagedByCIO"
		crd.Message = status.CRDMessage
	case install.CRDManagedByOLM:
		crd.Status = metav1.ConditionTrue
		crd.Reason = "ManagedByOLM"
		crd.Message = status.CRDMessage
	case install.CRDNoneExist:
		crd.Status = metav1.ConditionUnknown
		crd.Reason = "NoneExist"
		crd.Message = "CRDs not yet installed"
	case install.CRDMixedOwnership:
		crd.Status = metav1.ConditionFalse
		crd.Reason = "MixedOwnership"
		crd.Message = status.CRDMessage
	case install.CRDUnknownManagement:
		crd.Status = metav1.ConditionFalse
		crd.Reason = "UnknownManagement"
		crd.Message = status.CRDMessage
	}
	conditions = append(conditions, crd)

	return conditions
}
```

## Custom Event Handler for source.Channel

Since `source.Channel` receives `struct{}` (not a Kubernetes event), you need a handler that maps it to the right reconcile request:

```go
package main

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// enqueueGatewayClass returns an event handler that always enqueues
// a reconcile request for the fixed GatewayClass name.
type enqueueGatewayClass struct {
	Name string
}

func (e *enqueueGatewayClass) Generic(_ context.Context, _ event.GenericEvent, q workqueue.RateLimitingInterface) {
	q.Add(reconcile.Request{
		NamespacedName: types.NamespacedName{Name: e.Name},
	})
}

// Create, Update, Delete are unused — source.Channel only emits Generic events.
func (e *enqueueGatewayClass) Create(context.Context, event.CreateEvent, workqueue.RateLimitingInterface)   {}
func (e *enqueueGatewayClass) Update(context.Context, event.UpdateEvent, workqueue.RateLimitingInterface)   {}
func (e *enqueueGatewayClass) Delete(context.Context, event.DeleteEvent, workqueue.RateLimitingInterface)   {}
```

Then in main:

```go
	err = ctrl.NewControllerManagedBy(mgr).
		For(&gwapiv1.GatewayClass{}).
		WatchesRawSource(
			source.Channel(notifyCh, &enqueueGatewayClass{Name: gatewayClassName}),
		).
		Complete(reconciler)
```

## Sequence: What Happens When

### Initial startup (no CRDs, no istiod)

1. Manager starts, controller watches GatewayClass
2. GatewayClass is created by admin or platform
3. Controller reconciles: calls `lib.Apply(opts)`
4. Library wakes up, classifies CRDs (none exist) → installs CRDs with CIO labels
5. Library installs istiod via Helm
6. Library sends signal on `notifyCh`
7. Controller reconciles again: reads `lib.Status()` → sets Accepted=True, CRDsReady=True

### OSSM installed later (CRD ownership conflict)

1. Admin installs OSSM via OLM
2. OLM updates CRDs, adds `olm.managed=true` label
3. Library's CRD informer detects label change → re-reconciles
4. Library classifies CRDs → `MixedOwnership` (some CIO, some OLM)
5. Library skips CRD install/update, sets `Status.Error`
6. Library sends signal on `notifyCh`
7. Controller reconciles: reads status → sets CRDsReady=False, reason=MixedOwnership

### Drift detected (someone deletes a ConfigMap)

1. Someone deletes `istio` ConfigMap in the target namespace
2. Library's Helm resource informer detects the delete event
3. Library re-reconciles: Helm reinstalls the ConfigMap
4. Library sends signal on `notifyCh`
5. Controller reconciles: reads status → still Accepted=True (everything healthy)

### No-op Apply (same values)

1. Controller reconciles for some other reason (e.g. GatewayClass spec unchanged)
2. Calls `lib.Apply(opts)` with identical options
3. Library detects no change via `optionsEqual()` → does nothing
4. No `notifyCh` signal, no unnecessary Helm work

### External state change (e.g. OLM Subscription deleted)

1. OLM Subscription managing Istio CRDs is deleted
2. Controller detects the Subscription change (via its own watch)
3. Calls `lib.Enqueue()` to force CRD re-classification
4. Library re-reconciles with the previously applied options, re-classifying CRD ownership
5. Library sends signal on `notifyCh`
6. Controller reconciles: reads status → CRD state may have changed (e.g. takeover now possible)

Use `Enqueue()` instead of `Apply()` when the Options haven't changed but external cluster state
(CRD ownership, Subscription lifecycle, etc.) has. `Apply()` with identical options is a no-op,
so it won't trigger re-evaluation. `Enqueue()` bypasses that check.

## RBAC

The library needs cluster-wide permissions. Aggregate them into your ClusterRole:

```go
rules := append(myOperatorRules, install.LibraryRBACRules()...)
```

Or in YAML, merge the rules from `install.LibraryRBACRules()` into your operator's ClusterRole manifest.
