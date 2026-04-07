# Plan: E2E Tests for Manual Service Deployment with Gateway API

## 1. Summary

This plan defines the implementation of end-to-end tests that validate the "manual service creation" workflow for Gateway API on OpenShift with Istio/OSSM. The feature allows gateway owners to pre-create a Kubernetes Service before creating a Gateway resource, giving them full ownership over the service configuration -- including type, ports, selectors, annotations, and traffic policies.

**Key behavioral rules**:
1. When a user pre-creates a Service following the naming/labeling contract, Istio will NOT modify the service in any way. Istio only recognizes the service exists and uses it as-is. The user is fully responsible for configuring all ports (matching Gateway listeners), the selector, and any other service fields. Istio will copy the service's status (e.g., LoadBalancer address) to the Gateway status.
2. Istio CHECKS whether the pre-created service has ports matching all Gateway listeners. If a listener's port is missing from the service, that listener will NOT be ready, and the Gateway will report `Programmed=False`.
3. The `gateway.istio.io/managed` label is set by Istio itself on services it creates via automated deployment. Users MUST NOT manually add this label to pre-created services. The only required label is `gateway.networking.k8s.io/gateway-name`.

**Objective**: Provide comprehensive e2e coverage proving that:
1. Istio does not create a new service when a correctly pre-created one exists.
2. Istio does not mutate any field on the pre-created service.
3. Gateway status reflects the pre-created service's status.
4. Changes to the Gateway (e.g., adding listeners) do NOT modify the user-managed service.
5. Ports deliberately omitted from the service are NOT added by Istio, and cause the Gateway to report `Programmed=False` (proving Istio checks ports but does not modify them).
6. When the user manually adds the missing port to the service, the Gateway recovers to `Programmed=True`, and Istio does not further modify the service (proving the full lifecycle: missing port -> Programmed=False -> user fixes -> Programmed=True).

## 2. Obstacles and Assumptions

### Assumptions
- The Istio version deployed on the test cluster supports the manual service ownership model (service with label `gateway.networking.k8s.io/gateway-name` and name pattern `<gatewayName>-<gatewayClassName>`).
- When a user pre-creates a service, Istio treats it as fully user-managed. Istio will NOT add ports, set selectors, or change any spec field. The entire service lifecycle is the user's responsibility.
- For the Gateway to become `Programmed=True`, the pre-created service must have ports matching ALL Gateway listeners. If any listener port is missing from the service, that listener will not be ready and the Gateway will show `Programmed=False`.
- DNS-managed platforms are required for LoadBalancer connectivity verification.
- The `gateway.istio.io/managed` label is NOT required for manual service creation. Only the `gateway.networking.k8s.io/gateway-name` label and the correct naming pattern are required.

### Corrected Assumptions (from previous plan version)
- **REMOVED**: The previous plan assumed `gateway.istio.io/managed` label must be set on manually created services. This is WRONG. This label is set by Istio on services it creates automatically. Users should NOT set it manually.
- **CORRECTED**: The previous plan assumed the Gateway stays `Programmed=True` when a listener's port is missing from the service. This is WRONG. Istio checks service ports against listener ports. Missing ports cause `Programmed=False`.

### Obstacles
- **PR #1384 has incorrect behavioral assumptions**: The PR's LB test expects Istio to manage ports and selectors on the pre-created service (e.g., `setSelector: false`, waiting for ports to grow). This is wrong -- the user must set all ports and selectors upfront. The tests must be redesigned.
- **PR #1384 ClusterIP test is broken**: References undefined variables from LB test scope.
- **CI failures on PR #1384**: All CI jobs failed due to compilation errors and the commented-out test block.
- **`assertGatewaySuccessful` waits for `Programmed=True`**: This function (lines 716-775 of `util_gatewayapi_test.go`) polls until BOTH `Accepted=True` AND `Programmed=True`. If a Gateway has a listener whose port is missing from the service, the Gateway will be `Programmed=False` and `assertGatewaySuccessful` will timeout. The test design must account for this by creating the Gateway initially with ONLY the port-80 listener (which the service covers), asserting `Programmed=True`, THEN adding missing-port listeners and asserting `Programmed=False`.

## 3. Technical Blueprint

### 3.1 File Locations

| File | Purpose |
|------|---------|
| `test/e2e/gateway_api_test.go` | Test function definitions and registration in `TestGatewayAPI` |
| `test/e2e/util_gatewayapi_test.go` | Helper functions: `createGatewayService`, `defaultManualServicePorts` |

No new files are needed. All changes go into the existing test files.

### 3.2 Test Registration

Add the two new test functions to `TestGatewayAPI` in `gateway_api_test.go`, placed AFTER `testOperatorDegradedCondition` and BEFORE any uninstall tests. This positioning ensures Istio is fully installed and operational when these tests run.

```go
// In TestGatewayAPI, after testOperatorDegradedCondition and before testGatewayOpenshiftConditions:
t.Run("testGatewayAPIManualLBService", testGatewayAPIManualLBService)
t.Run("testGatewayAPIManualClusterIPService", testGatewayAPIManualClusterIPService)
```

IMPORTANT: Do NOT comment out existing tests. The existing test ordering is deliberate -- earlier tests install Istio, later tests depend on it.

### 3.3 Test Scenarios

#### Test 1: `testGatewayAPIManualLBService`

**Purpose**: Validate that a user can pre-create a fully-configured LoadBalancer Service with `externalTrafficPolicy: Local`, that Istio uses it without modification, that adding listeners whose ports are missing from the service causes `Programmed=False`, and that the service remains unmodified throughout.

**Preconditions**:
- Platform supports managed DNS (`isDNSManagementSupported` returns true; skip otherwise).
- GatewayClass `openshift-default` exists.

**Steps**:

1. **Create the GatewayClass** using `createGatewayClass(t, OpenShiftDefaultGatewayClassName, OpenShiftGatewayClassControllerName)`.

2. **Create the Service BEFORE the Gateway** using `createGatewayService()` with:
   - Name: `<gatewayName>-<gatewayClassName>` (e.g., `test-gateway-managed-service-openshift-default`)
   - Namespace: `openshift-ingress`
   - Type: `LoadBalancer`
   - `externalTrafficPolicy: Local`
   - Label: `gateway.networking.k8s.io/gateway-name: <gatewayName>`
   - **NO `gateway.istio.io/managed` label** -- this label is set by Istio on auto-created services and must NOT be added manually
   - Ports: `status-port` (15021/TCP) AND `http` (80/TCP) -- user must set ALL ports that match Gateway listeners
   - Selector: `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}` -- user must set the selector

3. **Create the Gateway with ONE listener**: HTTP listener on port 80 only. The service has port 80, so the Gateway should become `Programmed=True`.

4. **Wait for Gateway to be Accepted and Programmed** using `assertGatewaySuccessful()`. This succeeds because the service has port 80 matching the single listener.

5. **Assert service was NOT mutated** by Istio:
   - `spec.type` == `LoadBalancer`
   - `spec.externalTrafficPolicy` == `Local`
   - `spec.selector` == `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}`
   - `spec.ports` length == 2 (exactly as created: status-port + http)
   - No additional ports were added by Istio

6. **Assert only ONE service exists** for this gateway (Istio did not create a duplicate).

7. **Update the Gateway to add a second listener** (port 8443, HTTP protocol). The service does NOT have port 8443.

8. **Assert the Gateway transitions to `Programmed=False`**: Poll the Gateway status and verify that the `Programmed` condition becomes `False`. This is the key behavioral proof -- Istio CHECKS the service ports against listener ports but does NOT modify the service to add the missing port. Use a polling loop with `wait.PollUntilContextTimeout` to wait for `Programmed=False`.

9. **Assert the service was NOT modified** (even though the Gateway is now `Programmed=False`):
   - `spec.ports` length is still 2
   - `spec.externalTrafficPolicy` is still `Local`
   - `spec.type` is still `LoadBalancer`
   - Port 8443 was NOT added by Istio
   - No duplicate service was created

10. **User manually adds port 8443 to the service** (recovery step): Use a retry loop (`Get-mutate-Update` with `PollUntilContextTimeout`) to append port 8443 to the service's `spec.ports`. This mirrors the retry pattern used for managed label updates to handle ResourceVersion conflicts gracefully.

11. **Assert Gateway transitions back to `Programmed=True`** using `assertGatewaySuccessful()`. Since the service now has ports for all listeners (15021, 80, 8443), Istio should re-evaluate and mark the Gateway as `Programmed=True`.

12. **Assert service was NOT further modified by Istio** after recovery:
   - `spec.ports` length == 3 (exactly as the user set: status-port + http + http-8443)
   - `spec.type` == `LoadBalancer`
   - `spec.externalTrafficPolicy` == `Local`
   - `spec.selector` unchanged
   - No additional ports were added by Istio beyond the 3 the user explicitly set
   - No duplicate service was created

13. **Cleanup**: Delete Gateway and Service in `t.Cleanup`.

#### Test 2: `testGatewayAPIManualClusterIPService`

**Purpose**: Validate that a user can pre-create a ClusterIP Service with all necessary configuration, that Istio uses it without modification, that adding a listener with a missing port causes `Programmed=False`, and that the service remains unmodified.

**Preconditions**:
- GatewayClass `openshift-default` exists.
- No DNS platform requirement (ClusterIP does not provision external LB addresses).

**Steps**:

1. **Create the GatewayClass** using `createGatewayClass()`.

2. **Create the Service BEFORE the Gateway** using `createGatewayService()` with:
   - Name: `<gatewayName>-<gatewayClassName>` (e.g., `test-gateway-clusterip-service-openshift-default`)
   - Namespace: `openshift-ingress`
   - Type: `ClusterIP`
   - Label: `gateway.networking.k8s.io/gateway-name: <gatewayName>`
   - **NO `gateway.istio.io/managed` label**
   - Ports: `status-port` (15021/TCP) AND `http` (80/TCP) -- user must set ALL ports
   - Selector: `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}` -- user must set the selector

3. **Create the Gateway with ONE listener**: HTTP listener on port 80 only.

4. **Wait for Gateway to be Accepted and Programmed** using `assertGatewaySuccessful()`.

5. **Assert service was NOT mutated** by Istio:
   - `spec.type` == `ClusterIP`
   - `spec.selector` == `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}`
   - `spec.ports` length == 2 (exactly as created)

6. **Assert only ONE service exists** for this gateway.

7. **Update the Gateway to add a second listener** (port 8443, HTTP protocol).

8. **Assert the Gateway transitions to `Programmed=False`**: Same polling approach as the LB test.

9. **Assert the service was NOT modified**:
   - `spec.ports` length is still 2
   - `spec.type` remains `ClusterIP`
   - `spec.selector` unchanged
   - Port 8443 is NOT present
   - No duplicate service was created

10. **User manually adds port 8443 to the service** (recovery step): Same retry loop pattern as the LB test (`Get-mutate-Update` with `PollUntilContextTimeout`).

11. **Assert Gateway transitions back to `Programmed=True`** using `assertGatewaySuccessful()`.

12. **Assert service was NOT further modified by Istio** after recovery:
   - `spec.ports` length == 3 (status-port + http + http-8443)
   - `spec.type` == `ClusterIP`
   - `spec.selector` unchanged
   - No additional ports added beyond the 3 the user explicitly set
   - No duplicate service was created

13. **Cleanup**: Delete Gateway and Service.

### 3.4 Helper Function: `createGatewayService` (Revised)

Location: `test/e2e/util_gatewayapi_test.go`

The helper from PR #1384 needs significant revision to match the correct behavioral model.

```go
func createGatewayService(
    t *testing.T,
    gatewayName string,
    gatewayClass string,
    namespace string,
    svctype corev1.ServiceType,
    ports []corev1.ServicePort,
) (*corev1.Service, error)
```

**Key changes from the previous plan version**:
- **REMOVED `gateway.istio.io/managed` label**: This label must NOT be set on manually created services. Only set `gateway.networking.k8s.io/gateway-name`.
- Always set selector to `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}`.
- Always set label `gateway.networking.k8s.io/gateway-name: <gatewayName>`.
- If `svctype == LoadBalancer`, set `externalTrafficPolicy: Local` (the motivating use case).
- Accept explicit `ports` parameter from caller.
- Fix the doc comment.

**Rationale for removing the managed label**: The `gateway.istio.io/managed` label is an Istio-internal label that Istio sets on services it creates automatically. It is not part of the user-facing contract for manual service creation. Even Istio documentation does not recommend setting it on manually pre-created services. The only required identifiers are the naming pattern (`<gatewayName>-<gatewayClassName>`) and the `gateway.networking.k8s.io/gateway-name` label.

### 3.5 Helper Function: `defaultManualServicePorts`

Location: `test/e2e/util_gatewayapi_test.go`

Returns the two standard ports used by both manual service tests: `status-port` (15021/TCP, `appProtocol: tcp`) and `http` (80/TCP). Centralizes port definitions to avoid duplication between the LB and ClusterIP tests.

### 3.6 New Assertion Helper: `assertGatewayProgrammedFalse`

Location: `test/e2e/util_gatewayapi_test.go` (or inline in the test)

Polls the Gateway status until `Programmed=False` is observed, or times out with an error. This is needed because after adding a listener whose port is missing from the service, the Gateway will transition to `Programmed=False`. The assertion should:

```go
func assertGatewayProgrammedFalse(t *testing.T, namespace, name string) error {
    t.Helper()
    gw := &gatewayapiv1.Gateway{}
    nsName := types.NamespacedName{Namespace: namespace, Name: name}
    return wait.PollUntilContextTimeout(context.Background(), 3*time.Second, 2*time.Minute, false,
        func(ctx context.Context) (bool, error) {
            if err := kclient.Get(ctx, nsName, gw); err != nil {
                t.Logf("Failed to get gateway %v: %v; retrying...", nsName, err)
                return false, nil
            }
            for _, condition := range gw.Status.Conditions {
                if condition.Type == string(gatewayapiv1.GatewayConditionProgrammed) {
                    if condition.Status == metav1.ConditionFalse {
                        t.Logf("Gateway %v is Programmed=False as expected", nsName)
                        return true, nil
                    }
                }
            }
            t.Logf("Gateway %v is not yet Programmed=False; retrying...", nsName)
            return false, nil
        })
}
```

### 3.7 Critical Fixes Required from Previous Implementation

1. **Remove ALL references to `gateway.istio.io/managed` label** from:
   - `createGatewayService` helper: Remove the label from the service definition
   - `testGatewayAPIManualLBService`: Remove the entire managed label toggle test (Step 8 in the old plan / lines 1625-1688 in the current implementation)
   - `testGatewayAPIManualClusterIPService`: No managed label references to remove (it did not have the toggle test)

2. **Change the Gateway creation strategy**: Instead of creating the Gateway with TWO listeners (80 + 8443) from the start, create it with ONE listener (80) initially. This ensures `assertGatewaySuccessful` passes (because the service covers port 80). THEN add the 8443 listener and assert `Programmed=False`.

3. **Replace immutability-only assertions after listener addition with `Programmed=False` assertion**: The old plan assumed the Gateway stays `Programmed=True` after adding a listener whose port is missing. The new plan asserts `Programmed=False`, which is a STRONGER test -- it proves Istio checks ports but does not modify the service.

4. **Remove the managed label toggle test entirely** (Step 9 in the old LB test): This entire test step was based on the wrong assumption that users should set `gateway.istio.io/managed`. Since users should NOT set this label, testing its toggle behavior is meaningless.

### 3.8 Test Naming Convention

Following the existing pattern (`testGatewayAPI<Feature>`), the test names are:
- `testGatewayAPIManualLBService`
- `testGatewayAPIManualClusterIPService`

These are registered as subtests of `TestGatewayAPI`.

### 3.9 Assertion Strategy

Use `assert.Equal` for checks that do not prevent further assertions.
Use `require.Equal` or `require.Len` for checks where failure means subsequent assertions would panic (e.g., checking port count before indexing ports).

The core assertion pattern for both tests is:

```go
// Fetch service after Gateway is programmed
var svc corev1.Service
err := kclient.Get(ctx, svcKey, &svc)

// Assert service was NOT mutated
assert.Equal(t, expectedType, svc.Spec.Type)
assert.Equal(t, expectedSelector, svc.Spec.Selector)
require.Len(t, svc.Spec.Ports, expectedPortCount)
// For LB:
assert.Equal(t, corev1.ServiceExternalTrafficPolicyLocal, svc.Spec.ExternalTrafficPolicy)

// Assert no duplicate service was created
var services corev1.ServiceList
kclient.List(ctx, &services, matchingLabels, inNamespace)
require.Len(t, services.Items, 1, "Istio must not create a duplicate service")
```

After adding a listener with a missing port:

```go
// Assert Gateway is Programmed=False (Istio checked ports, found mismatch)
err := assertGatewayProgrammedFalse(t, namespace, gatewayName)
require.NoError(t, err, "Gateway must be Programmed=False when listener port is missing from service")

// Re-fetch service -- it must STILL be unchanged
err = kclient.Get(ctx, svcKey, &svc)

// Assert service was NOT modified despite Programmed=False
assert.Equal(t, expectedType, svc.Spec.Type)
require.Len(t, svc.Spec.Ports, expectedPortCount, "Istio must not add ports to manual service")

// Negative port assertion
for _, p := range svc.Spec.Ports {
    assert.NotEqual(t, int32(8443), p.Port,
        "Istio must not add port 8443 to service even though Gateway has a listener on 8443")
}
```

After user manually adds port 8443 to the service (recovery):

```go
// Add port 8443 to service using retry loop to handle conflicts
err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
    if err := kclient.Get(ctx, svcKey, &svc); err != nil {
        t.Logf("Failed to get service for port addition: %v; retrying...", err)
        return false, nil
    }
    svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
        Name:       "http-8443",
        Port:       8443,
        TargetPort: intstr.FromInt32(8443),
        Protocol:   corev1.ProtocolTCP,
    })
    if err := kclient.Update(ctx, &svc); err != nil {
        t.Logf("Failed to update service to add port 8443: %v; retrying...", err)
        return false, nil
    }
    return true, nil
})
require.NoError(t, err, "Failed to add port 8443 to service")

// Assert Gateway recovers to Programmed=True
_, err = assertGatewaySuccessful(t, namespace, gatewayName)
require.NoError(t, err, "Gateway must recover to Programmed=True after user adds missing port")

// Assert service was NOT further modified by Istio after recovery
err = kclient.Get(ctx, svcKey, &svc)
require.Len(t, svc.Spec.Ports, 3, "Service must have exactly 3 ports after user adds port 8443")
assert.Equal(t, expectedType, svc.Spec.Type)
assert.Equal(t, expectedSelector, svc.Spec.Selector)
```

## 4. Impact Matrix

| Component | Impact | Details |
|-----------|--------|---------|
| `test/e2e/gateway_api_test.go` | MODIFIED | Rewrite 2 test functions: remove managed label references, change Gateway creation strategy, add Programmed=False assertions, remove managed label toggle test |
| `test/e2e/util_gatewayapi_test.go` | MODIFIED | Update `createGatewayService` to remove managed label; add `assertGatewayProgrammedFalse` helper |
| `go.mod` / `go.sum` | POSSIBLY MODIFIED | If `stretchr/testify/require` is not already vendored |
| `vendor/` | POSSIBLY MODIFIED | Vendor `testify/require` package |
| Gateway API CRDs | NO CHANGE | Tests use existing CRDs |
| Operator reconciler logic | NO CHANGE | Tests validate Istio behavior, not operator code |

## 5. Verification Strategy (100-point rubric)

| # | Check | Points | Pass Criteria |
|---|-------|--------|---------------|
| 1 | LB service pre-created with correct name/labels/ports/selector (NO managed label) | 10 | Service created before Gateway with `gateway.networking.k8s.io/gateway-name` label only, full port+selector config |
| 2 | Gateway reaches Accepted=True and Programmed=True with single-listener (port 80) LB service | 10 | `assertGatewaySuccessful` passes |
| 3 | LB service `externalTrafficPolicy` NOT mutated by Istio | 8 | Field remains `Local` after Gateway reconciliation |
| 4 | LB service ports NOT mutated by Istio | 8 | Port count unchanged, no extra ports added |
| 5 | LB service selector NOT mutated by Istio | 4 | Selector unchanged from user-set value |
| 6 | No duplicate LB service created by Istio | 4 | Only 1 service exists with gateway-name label |
| 7 | Adding listener (port 8443) causes Gateway Programmed=False | 10 | `assertGatewayProgrammedFalse` passes after adding listener whose port is missing from service |
| 8 | LB service NOT modified after listener addition causes Programmed=False | 8 | Ports, type, policy unchanged; port 8443 NOT added |
| 8a | User adds port 8443 to LB service, Gateway recovers to Programmed=True | 6 | `assertGatewaySuccessful` passes after user adds port 8443 via retry loop |
| 8b | LB service NOT further modified by Istio after recovery | 6 | Ports == 3 (user-set), type/policy/selector unchanged, no duplicates |
| 9 | ClusterIP service pre-created with correct name/labels/ports/selector (NO managed label) | 10 | Same labeling contract as LB test |
| 10 | Gateway reaches Accepted=True and Programmed=True with single-listener (port 80) ClusterIP service | 8 | `assertGatewaySuccessful` passes |
| 11 | ClusterIP service type NOT mutated by Istio | 4 | Type remains `ClusterIP` |
| 12 | ClusterIP service ports NOT mutated by Istio | 4 | Port count unchanged |
| 13 | ClusterIP service selector NOT mutated by Istio | 4 | Selector unchanged |
| 14 | Adding listener causes ClusterIP Gateway Programmed=False | 8 | Same as LB test check 7 |
| 15 | ClusterIP service NOT modified after Programmed=False | 4 | Ports, type unchanged; port 8443 NOT added |
| 15a | User adds port 8443 to ClusterIP service, Gateway recovers to Programmed=True | 4 | `assertGatewaySuccessful` passes after user adds port 8443 via retry loop |
| 15b | ClusterIP service NOT further modified by Istio after recovery | 4 | Ports == 3 (user-set), type/selector unchanged, no duplicates |
| 16 | Cleanup succeeds without resource leaks | 4 | No resources left after test |
| **Total** | | **130** | Normalize to 100 by dividing each score by 1.3, or accept 130-point scale |

NOTE: Point total exceeds 100 due to the added Programmed=False and recovery checks. The rubric can be rebalanced during implementation.

## 6. Documentation Guidance for Tech Writers

### Feature: Manual Service Creation for Gateway API Gateways

**Target audience**: Cluster administrators and platform engineers who need fine-grained control over the Kubernetes Service created for a Gateway API Gateway.

#### What to Document

1. **Feature description**: When using Gateway API with Istio/OSSM on OpenShift, Istio automatically creates a Kubernetes Service for each Gateway. However, users who need to customize the Service (e.g., setting `externalTrafficPolicy: Local`, using `ClusterIP` instead of `LoadBalancer`, or adding cloud-provider-specific annotations) can pre-create the Service before creating the Gateway. When Istio detects a pre-created service matching the expected name and labels, it will use that service as-is without modifying it.

2. **The contract -- CRITICAL for users to understand**:
   - The Service name MUST follow the pattern: `<gatewayName>-<gatewayClassName>` (e.g., `my-gateway-openshift-default`).
   - The Service MUST have the label `gateway.networking.k8s.io/gateway-name: <gatewayName>`.
   - The Service MUST NOT have the label `gateway.istio.io/managed`. This label is set by Istio on auto-created services and must not be added manually by users.
   - The Service MUST be in the same namespace as the Gateway (typically `openshift-ingress`).
   - The Service MUST include ports for every Gateway listener (matching port numbers). If a listener's port is missing from the service, the Gateway will report `Programmed=False` and that listener will not be ready.
   - The Service MUST include the Istio status port (15021/TCP, `appProtocol: tcp`).
   - The Service MUST have the selector `gateway.networking.k8s.io/gateway-name: <gatewayName>`.

3. **User's responsibility -- Istio will NOT manage the service**:
   - Istio will NOT add, remove, or modify ports on the pre-created service.
   - Istio will NOT set or change the selector.
   - Istio will NOT change the service type, traffic policy, or any other field.
   - Istio WILL check that the service has ports matching all Gateway listeners. Missing ports cause the Gateway to report `Programmed=False`.
   - When adding new listeners to the Gateway, the user MUST manually update the Service to include the corresponding ports. Failure to do so will cause the Gateway to become `Programmed=False`.
   - When removing listeners, the user SHOULD remove the corresponding ports from the Service.

4. **What Istio WILL do**:
   - Recognize the pre-created service and use it for the Gateway's data plane.
   - Check that the service has ports matching all Gateway listeners (report `Programmed=False` if not).
   - Copy the service's status (e.g., LoadBalancer ingress address) to the Gateway status.
   - Route traffic through the service to the Gateway pods.

5. **Supported service types**:
   - `LoadBalancer`: For external access. User can set `externalTrafficPolicy: Local` for client IP preservation.
   - `ClusterIP`: For internal-only access within the cluster.

6. **Example YAML snippets to include**:

   a. Pre-created LoadBalancer Service:
   ```yaml
   apiVersion: v1
   kind: Service
   metadata:
     name: my-gateway-openshift-default
     namespace: openshift-ingress
     labels:
       gateway.networking.k8s.io/gateway-name: my-gateway
   spec:
     type: LoadBalancer
     externalTrafficPolicy: Local
     selector:
       gateway.networking.k8s.io/gateway-name: my-gateway
     ports:
     - name: status-port
       port: 15021
       targetPort: 15021
       protocol: TCP
       appProtocol: tcp
     - name: http
       port: 80
       targetPort: 80
       protocol: TCP
   ```

   b. Pre-created ClusterIP Service:
   ```yaml
   apiVersion: v1
   kind: Service
   metadata:
     name: my-gateway-openshift-default
     namespace: openshift-ingress
     labels:
       gateway.networking.k8s.io/gateway-name: my-gateway
   spec:
     type: ClusterIP
     selector:
       gateway.networking.k8s.io/gateway-name: my-gateway
     ports:
     - name: status-port
       port: 15021
       targetPort: 15021
       protocol: TCP
       appProtocol: tcp
     - name: http
       port: 80
       targetPort: 80
       protocol: TCP
   ```

   c. Corresponding Gateway:
   ```yaml
   apiVersion: gateway.networking.k8s.io/v1
   kind: Gateway
   metadata:
     name: my-gateway
     namespace: openshift-ingress
   spec:
     gatewayClassName: openshift-default
     listeners:
     - name: http
       hostname: "*.example.com"
       port: 80
       protocol: HTTP
   ```

7. **Troubleshooting**:
   - If Istio creates a new Service instead of using the pre-created one: verify the naming pattern (`<gatewayName>-<gatewayClassName>`) and the `gateway.networking.k8s.io/gateway-name` label. Do NOT add the `gateway.istio.io/managed` label -- this is an Istio-internal label.
   - If the Gateway is Accepted but not Programmed (`Programmed=False`): verify the service has ports matching ALL Gateway listeners, and the selector is set correctly. Check the Gateway's `status.conditions` for the specific reason.
   - If traffic does not reach the Gateway after adding a new listener: the user must manually add the corresponding port to the service. Until the port is added, the Gateway will remain `Programmed=False` for that listener.
   - If using ClusterIP and the Gateway shows no address: this is expected behavior for ClusterIP services (they do not have external addresses).

8. **Supportability statement**: This feature relies on the Istio/OSSM manual service ownership model. It is supported on OpenShift with the `openshift-default` GatewayClass and the managed Istio/OSSM installation. Behavior depends on the Istio version deployed with the OpenShift release. Users should test this workflow in non-production environments before relying on it in production.

9. **Security considerations** (MUST be included in documentation):
   - **RBAC is the security boundary**: Any principal with `create` permissions on Services in the Gateway namespace (typically `openshift-ingress`) can pre-create a service that Istio will adopt for a Gateway. Cluster administrators MUST restrict Service creation in this namespace to trusted users only.
   - **Service hijacking risk**: If an untrusted user can create Services in the Gateway namespace, they could pre-create a service with the correct naming/labeling pattern before a legitimate Gateway is created, causing Istio to use the attacker-controlled service. This could redirect traffic to arbitrary pods via a malicious selector.
   - **Selector ownership**: The manual service pattern gives the service creator full control over the pod selector. A misconfigured or malicious selector can route Gateway traffic to unintended workloads. Administrators should audit selectors on manually-created Gateway services.
   - **Recommendation**: Use OpenShift RBAC or a ValidatingAdmissionPolicy to restrict which users/service accounts can create Services with the `gateway.networking.k8s.io/gateway-name` label in the `openshift-ingress` namespace.

## 7. Approval Status

**AWAITING EXECUTION**

---

### Quick Reference: Key Constants and Identifiers

| Constant | Value | Source |
|----------|-------|--------|
| `OpenShiftDefaultGatewayClassName` | `openshift-default` | `pkg/operator/controller/names.go` |
| `OpenShiftGatewayClassControllerName` | `openshift.io/gateway-controller/v1` | `pkg/operator/controller/names.go` |
| `DefaultOperandNamespace` | `openshift-ingress` | `pkg/operator/controller/names.go` |
| Gateway service name pattern | `<gatewayName>-<gatewayClassName>` | Istio convention |
| Gateway name label | `gateway.networking.k8s.io/gateway-name` | Gateway API spec |
| Istio status port | 15021/TCP | Istio default |

### Changes Summary from Previous Plan Version

1. **REMOVED**: All references to `gateway.istio.io/managed` label on manually created services
2. **REMOVED**: Managed label toggle test (old Step 9 in LB test)
3. **CHANGED**: Gateway creation strategy -- start with single listener (port 80), then add missing-port listener
4. **ADDED**: `assertGatewayProgrammedFalse` assertion after adding listener with missing port
5. **CHANGED**: Assertion flow -- after adding listener, assert `Programmed=False` THEN assert service immutability
6. **UPDATED**: Documentation to explicitly warn against setting `gateway.istio.io/managed` label
7. **UPDATED**: Troubleshooting to describe `Programmed=False` behavior when ports are missing
8. **ADDED**: Recovery steps (Steps 10-12) in both LB and ClusterIP tests: user adds port 8443 to service, Gateway recovers to Programmed=True, service immutability re-asserted with 3 ports
9. **ADDED**: Objective 6 covering the full lifecycle (missing port -> Programmed=False -> user fixes -> Programmed=True)
10. **ADDED**: Rubric checks 8a/8b (LB recovery) and 15a/15b (ClusterIP recovery)
11. **ADDED**: Recovery assertion pattern in section 3.9 using retry loop for service update
