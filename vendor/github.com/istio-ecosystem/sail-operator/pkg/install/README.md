# pkg/install

Library for managing istiod installations without running the Sail Operator.
Designed for embedding in other operators (e.g. OpenShift Ingress) that need
to install and maintain Istio as an internal dependency.

## Usage

```go
lib, err := install.New(kubeConfig, resourceFS)
notifyCh := lib.Start(ctx)

// In controller reconcile:
lib.Apply(install.Options{
    Namespace: "istio-system",
    Values:    install.GatewayAPIDefaults(),
})

// Read result after notification:
for range notifyCh {
    status := lib.Status()
    // update conditions from status
}

// Teardown:
lib.Uninstall(ctx, "istio-system", "default")
```

## How it works

The Library runs as an independent actor with a simple state model:

1. **Apply** -- consumer sends desired state (version, namespace, values)
2. **Reconcile** -- Library installs/upgrades CRDs and istiod via Helm
3. **Drift detection** -- dynamic informers watch owned resources and CRDs, re-enqueuing reconciliation on changes
4. **Status** -- consumer reads the reconciliation result

The reconciliation loop sits idle until the first `Apply()` call. After that,
it stays active with informers running until `Uninstall()` clears the desired
state and stops the loop.

## Public API

### Constructor

- `New(kubeConfig, resourceFS)` -- creates a Library with Kubernetes clients, Helm chart manager, and CRD manager
- `FromDirectory(path)` -- creates an `fs.FS` from a filesystem path (alternative to embedded resources)

### Library methods

| Method | Description |
|---|---|
| `Start(ctx)` | Starts the reconciliation loop; returns a notification channel |
| `Apply(opts)` | Sets desired state; enqueues reconciliation if changed |
| `Enqueue()` | Forces re-reconciliation without changing desired state |
| `Status()` | Returns the latest reconciliation result |
| `Uninstall(ctx, ns, rev)` | Stops informers, waits for processing, then Helm-uninstalls |

### Types

- **Options** -- install options: `Namespace`, `Version`, `Revision`, `Values`, `ManageCRDs`, `IncludeAllCRDs`, `OverwriteOLMManagedCRD`
- **Status** -- reconciliation result: `CRDState`, `CRDMessage`, `CRDs`, `Installed`, `Version`, `Error`
- **CRDManagementState** -- aggregate CRD ownership: `ManagedByCIO`, `ManagedByOLM`, `UnknownManagement`, `MixedOwnership`, `NoneExist`
- **CRDInfo** -- per-CRD state: `Name`, `State`, `Found`
- **ImageNames** -- image names for each component: `Istiod`, `Proxy`, `CNI`, `ZTunnel`

### Helper functions

- `GatewayAPIDefaults()` -- pre-configured values for Gateway API mode on OpenShift
- `MergeValues(base, overlay)` -- deep-merge two Values structs (overlay wins)
- `DefaultVersion(resourceFS)` -- highest stable semver version from the resource FS
- `NormalizeVersion(version)` -- ensures a `v` prefix
- `ValidateVersion(resourceFS, version)` -- checks that a version directory exists
- `SetImageDefaults(resourceFS, registry, images)` -- populates image refs from version directories
- `LibraryRBACRules()` -- returns RBAC PolicyRules for a consumer's ClusterRole

## CRD management

The Library classifies existing CRDs by ownership labels before deciding what to do:

| State | Meaning | Action |
|---|---|---|
| `NoneExist` | No target CRDs on cluster | Install with CIO labels |
| `ManagedByCIO` | All owned by Cluster Ingress Operator | Update as needed |
| `ManagedByOLM` | All owned by OLM (OSSM subscription) | Leave alone; Helm install proceeds |
| `UnknownManagement` | CRDs exist without recognized labels | Leave alone; set Status.Error |
| `MixedOwnership` | Inconsistent labels across CRDs | Leave alone; set Status.Error |

The `OverwriteOLMManagedCRD` callback in Options lets the consumer decide
whether to take over OLM-managed CRDs (e.g. after an OSSM subscription is deleted).

Which CRDs are targeted depends on `IncludeAllCRDs`: when false (default), only
CRDs matching `PILOT_INCLUDE_RESOURCES` / `PILOT_IGNORE_RESOURCES` are managed.

## Files

| File | Purpose |
|---|---|
| `library.go` | Public API, types (`Library`, `Status`, `Options`), constructor |
| `lifecycle.go` | Reconciliation loop, workqueue, `Start`/`Apply`/`Uninstall` |
| `installer.go` | Core install/uninstall logic, Helm values resolution, watch spec extraction |
| `crds.go` | CRD ownership classification, install, update |
| `crds_filter.go` | CRD selection based on `PILOT_INCLUDE_RESOURCES` / `PILOT_IGNORE_RESOURCES` |
| `values.go` | `GatewayAPIDefaults()`, `MergeValues()` |
| `predicates.go` | Event filtering for informers (ownership checks, status-only changes) |
| `informers.go` | Dynamic informer setup for drift detection |
| `version.go` | Version resolution and validation |
| `images.go` | Image configuration from resource FS |
| `rbac.go` | RBAC rules for library consumers |
