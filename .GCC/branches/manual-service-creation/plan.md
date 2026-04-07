# Plan: E2E Tests for Manual Service Deployment with Gateway API

## 1. Summary

This plan defines the implementation of end-to-end tests that validate the "manual service creation" workflow for Gateway API on OpenShift with Istio/OSSM. The feature allows gateway owners to pre-create a Kubernetes Service before creating a Gateway resource, giving them full ownership over the service configuration -- including type, ports, selectors, annotations, and traffic policies.

**Key behavioral rule**: When a user pre-creates a Service following the naming/labeling contract, Istio will NOT modify the service in any way. Istio only recognizes the service exists and uses it as-is. The user is fully responsible for configuring all ports (matching Gateway listeners), the selector, and any other service fields. Istio will copy the service's status (e.g., LoadBalancer address) to the Gateway status.

**Objective**: Provide comprehensive e2e coverage proving that:
1. Istio does not create a new service when a correctly pre-created one exists.
2. Istio does not mutate any field on the pre-created service.
3. Gateway status reflects the pre-created service's status.
4. Changes to the Gateway (e.g., adding listeners) do NOT modify the user-managed service.
5. Ports deliberately omitted from the service are NOT added by Istio, even when the Gateway defines listeners on those ports (regression guard against future Istio versions that might start managing ports).
6. The `gateway.istio.io/managed` label does not grant Istio permission to modify the service -- toggling the label does not trigger port/selector/field modifications.

## 2. Obstacles and Assumptions

### Assumptions
- The Istio version deployed on the test cluster supports the manual service ownership model (service with label `gateway.networking.k8s.io/gateway-name` and name pattern `<gatewayName>-<gatewayClassName>`).
- When a user pre-creates a service, Istio treats it as fully user-managed. Istio will NOT add ports, set selectors, or change any spec field. The entire service lifecycle is the user's responsibility.
- For the Gateway to become Programmed, the pre-created service must have the correct ports (matching listeners) and selector already configured.
- DNS-managed platforms are required for LoadBalancer connectivity verification.
- The `gateway.istio.io/managed` label is needed for Istio to recognize the service as pre-created.

### Obstacles
- **PR #1384 has incorrect behavioral assumptions**: The PR's LB test expects Istio to manage ports and selectors on the pre-created service (e.g., `setSelector: false`, waiting for ports to grow). This is wrong -- the user must set all ports and selectors upfront. The tests must be redesigned.
- **PR #1384 ClusterIP test is broken**: References undefined variables from LB test scope.
- **CI failures on PR #1384**: All CI jobs failed due to compilation errors and the commented-out test block.

## 3. Technical Blueprint

### 3.1 File Locations

| File | Purpose |
|------|---------|
| `test/e2e/gateway_api_test.go` | Test function definitions and registration in `TestGatewayAPI` |
| `test/e2e/util_gatewayapi_test.go` | Helper functions: `createGatewayService`, `waitForGatewayService` |

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

**Purpose**: Validate that a user can pre-create a fully-configured LoadBalancer Service with `externalTrafficPolicy: Local`, that Istio uses it without modification, and that adding listeners to the Gateway does NOT mutate the service.

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
   - Label: `gateway.istio.io/managed: openshift.io-gateway-controller-v1`
   - Ports: `status-port` (15021/TCP) AND `http` (80/TCP) -- user must set ALL ports
   - Selector: `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}` -- user must set the selector
   - **Deliberately OMIT port 8443**: The Gateway will define a listener on 8443 (see step 3), but the service intentionally does not include it. This is a regression guard -- see step 5b.

3. **Create the Gateway** with TWO listeners: one HTTP listener on port 80 AND one HTTPS/TLS listener on port 8443 (with a wildcard hostname under the cluster's base domain for both).

4. **Wait for Gateway to be Accepted and Programmed** using `assertGatewaySuccessful()`.

5. **Assert service was NOT mutated** by Istio:
   - `spec.type` == `LoadBalancer`
   - `spec.externalTrafficPolicy` == `Local`
   - `spec.selector` == `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}`
   - `spec.ports` length == 2 (exactly as created: status-port + http)
   - No additional ports were added by Istio

5b. **Negative port assertion (regression guard)**: Assert that port 8443 was NOT added to the service by Istio, even though the Gateway has a listener on port 8443. Iterate over `spec.ports` and confirm no port entry has `port: 8443`. This is the key regression guard -- if a future Istio version starts managing ports on pre-created services, this assertion will fail and alert us.

6. **Assert only ONE service exists** for this gateway (Istio did not create a duplicate).

7. **Update the Gateway to add a third listener** (port 8080, HTTP protocol).

8. **Wait briefly, then assert the service was NOT modified**:
   - `spec.ports` length is still 2
   - `spec.externalTrafficPolicy` is still `Local`
   - `spec.type` is still `LoadBalancer`
   - No new ports were added by Istio (including port 8080 and port 8443)

9. **Test `gateway.istio.io/managed` label immutability**: After the assertions in step 8, remove the `gateway.istio.io/managed` label from the service, wait briefly, then re-add it. After re-adding, re-fetch the service and assert that Istio still did not modify it (ports, selector, type, externalTrafficPolicy all unchanged). This confirms the managed label does not trigger Istio to "take over" and start managing the service fields.

10. **Cleanup**: Delete Gateway and Service in `t.Cleanup`.

#### Test 2: `testGatewayAPIManualClusterIPService`

**Purpose**: Validate that a user can pre-create a ClusterIP Service with all necessary configuration, that Istio uses it without modification, and that Gateway status reflects the service correctly.

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
   - Label: `gateway.istio.io/managed: openshift.io-gateway-controller-v1`
   - Ports: `status-port` (15021/TCP) AND `http` (80/TCP) -- user must set ALL ports
   - Selector: `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}` -- user must set the selector
   - **Deliberately OMIT port 8443**: Same negative port strategy as the LB test.

3. **Create the Gateway** with TWO listeners: one HTTP listener on port 80 AND one on port 8443.

4. **Wait for Gateway to be Accepted and Programmed** using `assertGatewaySuccessful()`.

5. **Assert service was NOT mutated** by Istio:
   - `spec.type` == `ClusterIP`
   - `spec.selector` == `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}`
   - `spec.ports` length == 2 (exactly as created)
   - **Negative port assertion**: Port 8443 is NOT present in `spec.ports`.

6. **Assert only ONE service exists** for this gateway.

7. **Update the Gateway to add a third listener** (port 8080).

8. **Assert the service was NOT modified**:
   - `spec.ports` length is still 2
   - `spec.type` remains `ClusterIP`
   - Ports 8080 and 8443 are NOT present

9. **Cleanup**: Delete Gateway and Service.

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

**Key changes from PR #1384**:
- Remove `setSelector` parameter: The selector MUST always be set by the user for manual services. The helper always sets it.
- Remove `setManaged` parameter: The managed label MUST always be set. The helper always sets it.
- Add explicit `ports` parameter: Since Istio will NOT manage ports, the caller must specify all ports including listener ports. This makes the test intent clear.
- Always set selector to `{"gateway.networking.k8s.io/gateway-name": "<gatewayName>"}`.
- Always set label `gateway.istio.io/managed: openshift.io-gateway-controller-v1`.
- Always set label `gateway.networking.k8s.io/gateway-name: <gatewayName>`.
- If `svctype == LoadBalancer`, set `externalTrafficPolicy: Local` (the motivating use case).
- Fix the doc comment (currently says "createGatewayClass").

**Rationale for removing boolean parameters**: Since manual service creation requires ALL fields to be user-managed, the `setSelector` and `setManaged` toggles create misleading optionality. A service without the selector or managed label would not work correctly. If future tests need different configurations, they can create the service inline.

### 3.5 Helper Function: `defaultManualServicePorts`

Location: `test/e2e/util_gatewayapi_test.go`

Returns the two standard ports used by both manual service tests: `status-port` (15021/TCP, `appProtocol: tcp`) and `http` (80/TCP). Centralizes port definitions to avoid duplication between the LB and ClusterIP tests.

**Note**: `waitForGatewayService` from PR #1384 is NOT needed. Since the test pre-creates the service itself, a direct `kclient.Get` is sufficient to re-fetch it. Adding a poll-based wait helper for a resource the test already created would be dead code.

### 3.6 Critical Fixes Required from PR #1384

1. **Restore the `TestGatewayAPI` function body**: Remove the `/* */` comment wrapping around existing tests. Add the two new tests at the correct position in the existing sequence.

2. **Rewrite `testGatewayAPIManualLBService`**:
   - Pre-create service with ALL ports and selector (not relying on Istio to add them).
   - Assert that the service is NOT mutated after Gateway creation.
   - Assert that adding a listener does NOT add ports to the service.

3. **Rewrite `testGatewayAPIManualClusterIPService`**:
   - Must be a standalone function (not referencing variables from LB test scope).
   - Pre-create service with all ports and selector.
   - Same immutability assertions as the LB test.

4. **Revise `createGatewayService` helper**:
   - Remove `setSelector` and `setManaged` boolean parameters.
   - Always set selector and managed label.
   - Accept explicit ports list from caller.
   - Fix doc comment.

5. **Fix the doc comment** on `createGatewayService` (currently says "createGatewayClass").

### 3.7 Test Naming Convention

Following the existing pattern (`testGatewayAPI<Feature>`), the test names are:
- `testGatewayAPIManualLBService`
- `testGatewayAPIManualClusterIPService`

These are registered as subtests of `TestGatewayAPI`.

### 3.8 Assertion Strategy

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

Negative port assertion (regression guard):

```go
// Assert that port 8443 was NOT added by Istio, even though the Gateway has a listener on 8443
for _, p := range svc.Spec.Ports {
    assert.NotEqual(t, int32(8443), p.Port,
        "Istio must not add omitted port 8443 to pre-created service (regression guard)")
}
```

After adding a listener and waiting:

```go
// Re-fetch service
err = kclient.Get(ctx, svcKey, &svc)

// Assert service is STILL unchanged
assert.Equal(t, expectedType, svc.Spec.Type)
require.Len(t, svc.Spec.Ports, expectedPortCount, "Istio must not add ports to manual service")

// Negative port assertion still holds
for _, p := range svc.Spec.Ports {
    assert.NotEqual(t, int32(8443), p.Port)
    assert.NotEqual(t, int32(8080), p.Port)
}
```

Managed label toggle assertion (LB test only):

```go
// Remove the managed label
delete(svc.Labels, "gateway.istio.io/managed")
err = kclient.Update(ctx, &svc)
// Wait briefly for any Istio reconciliation
time.Sleep(10 * time.Second)

// Re-add the managed label
svc.Labels["gateway.istio.io/managed"] = "openshift.io-gateway-controller-v1"
err = kclient.Update(ctx, &svc)
// Wait again
time.Sleep(10 * time.Second)

// Re-fetch and assert nothing changed
err = kclient.Get(ctx, svcKey, &svc)
assert.Equal(t, expectedType, svc.Spec.Type)
require.Len(t, svc.Spec.Ports, expectedPortCount, "Managed label toggle must not trigger port modification")
assert.Equal(t, expectedSelector, svc.Spec.Selector)
assert.Equal(t, corev1.ServiceExternalTrafficPolicyLocal, svc.Spec.ExternalTrafficPolicy)
```

## 4. Impact Matrix

| Component | Impact | Details |
|-----------|--------|---------|
| `test/e2e/gateway_api_test.go` | MODIFIED | Add 2 new test functions, register them in `TestGatewayAPI` |
| `test/e2e/util_gatewayapi_test.go` | MODIFIED | Add `createGatewayService` and `waitForGatewayService` helpers |
| `go.mod` / `go.sum` | POSSIBLY MODIFIED | If `stretchr/testify/require` is not already vendored |
| `vendor/` | POSSIBLY MODIFIED | Vendor `testify/require` package |
| Gateway API CRDs | NO CHANGE | Tests use existing CRDs |
| Operator reconciler logic | NO CHANGE | Tests validate Istio behavior, not operator code |

## 5. Verification Strategy (100-point rubric)

| # | Check | Points | Pass Criteria |
|---|-------|--------|---------------|
| 1 | LB service pre-created with correct name/labels/ports/selector | 8 | Service created before Gateway with full config |
| 2 | Gateway reaches Accepted=True with pre-created LB service | 8 | `assertGatewaySuccessful` passes |
| 3 | Gateway reaches Programmed=True with pre-created LB service | 8 | `assertGatewaySuccessful` passes |
| 4 | LB service `externalTrafficPolicy` NOT mutated by Istio | 8 | Field remains `Local` after Gateway reconciliation |
| 5 | LB service ports NOT mutated by Istio | 8 | Port count unchanged, no extra ports added |
| 6 | LB service selector NOT mutated by Istio | 4 | Selector unchanged from user-set value |
| 7 | No duplicate LB service created by Istio | 4 | Only 1 service exists with gateway-name label |
| 8 | Adding listener does NOT modify LB service | 8 | Ports and all fields remain unchanged |
| 9 | **Negative port: omitted port 8443 NOT added to LB service** | 8 | Port 8443 absent from `spec.ports` despite Gateway listener existing |
| 10 | **Managed label toggle does NOT trigger LB service mutation** | 8 | After removing and re-adding `gateway.istio.io/managed`, service fields unchanged |
| 11 | ClusterIP service type NOT mutated by Istio | 4 | Type remains `ClusterIP` |
| 12 | ClusterIP service ports NOT mutated by Istio | 8 | Port count unchanged |
| 13 | ClusterIP service selector NOT mutated by Istio | 4 | Selector unchanged |
| 14 | Adding listener does NOT modify ClusterIP service | 4 | Ports remain unchanged |
| 15 | **Negative port: omitted port 8443 NOT added to ClusterIP service** | 8 | Port 8443 absent from `spec.ports` despite Gateway listener existing |
| 16 | Cleanup succeeds without resource leaks | 4 | No resources left after test |
| **Total** | | **100** | |

## 6. Documentation Guidance for Tech Writers

### Feature: Manual Service Creation for Gateway API Gateways

**Target audience**: Cluster administrators and platform engineers who need fine-grained control over the Kubernetes Service created for a Gateway API Gateway.

#### What to Document

1. **Feature description**: When using Gateway API with Istio/OSSM on OpenShift, Istio automatically creates a Kubernetes Service for each Gateway. However, users who need to customize the Service (e.g., setting `externalTrafficPolicy: Local`, using `ClusterIP` instead of `LoadBalancer`, or adding cloud-provider-specific annotations) can pre-create the Service before creating the Gateway. When Istio detects a pre-created service matching the expected name and labels, it will use that service as-is without modifying it.

2. **The contract -- CRITICAL for users to understand**:
   - The Service name MUST follow the pattern: `<gatewayName>-<gatewayClassName>` (e.g., `my-gateway-openshift-default`).
   - The Service MUST have the label `gateway.networking.k8s.io/gateway-name: <gatewayName>`.
   - The Service MUST have the label `gateway.istio.io/managed: openshift.io-gateway-controller-v1`.
   - The Service MUST be in the same namespace as the Gateway (typically `openshift-ingress`).
   - The Service MUST include ports for every Gateway listener (matching port numbers).
   - The Service MUST include the Istio status port (15021/TCP, `appProtocol: tcp`).
   - The Service MUST have the selector `gateway.networking.k8s.io/gateway-name: <gatewayName>`.

3. **User's responsibility -- Istio will NOT manage the service**:
   - Istio will NOT add, remove, or modify ports on the pre-created service.
   - Istio will NOT set or change the selector.
   - Istio will NOT change the service type, traffic policy, or any other field.
   - When adding new listeners to the Gateway, the user MUST manually update the Service to include the corresponding ports. Failure to do so means traffic on those listeners will not reach the Gateway pods.
   - When removing listeners, the user SHOULD remove the corresponding ports from the Service.

4. **What Istio WILL do**:
   - Recognize the pre-created service and use it for the Gateway's data plane.
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
       gateway.istio.io/managed: openshift.io-gateway-controller-v1
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
       gateway.istio.io/managed: openshift.io-gateway-controller-v1
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
   - If Istio creates a new Service instead of using the pre-created one: verify the naming pattern (`<gatewayName>-<gatewayClassName>`), the `gateway.networking.k8s.io/gateway-name` label, and the `gateway.istio.io/managed` label.
   - If the Gateway is Accepted but not Programmed: verify the service has the correct ports matching all Gateway listeners, and the selector is set correctly.
   - If traffic does not reach the Gateway after adding a new listener: the user must manually add the corresponding port to the service.
   - If using ClusterIP and the Gateway shows no address: this is expected behavior for ClusterIP services (they do not have external addresses).

8. **Supportability statement**: This feature relies on the Istio/OSSM manual service ownership model. It is supported on OpenShift with the `openshift-default` GatewayClass and the managed Istio/OSSM installation. Behavior depends on the Istio version deployed with the OpenShift release. Users should test this workflow in non-production environments before relying on it in production.

9. **Security considerations** (MUST be included in documentation):
   - **RBAC is the security boundary**: Any principal with `create` permissions on Services in the Gateway namespace (typically `openshift-ingress`) can pre-create a service that Istio will adopt for a Gateway. Cluster administrators MUST restrict Service creation in this namespace to trusted users only.
   - **Service hijacking risk**: If an untrusted user can create Services in the Gateway namespace, they could pre-create a service with the correct naming/labeling pattern before a legitimate Gateway is created, causing Istio to use the attacker-controlled service. This could redirect traffic to arbitrary pods via a malicious selector.
   - **Selector ownership**: The manual service pattern gives the service creator full control over the pod selector. A misconfigured or malicious selector can route Gateway traffic to unintended workloads. Administrators should audit selectors on manually-created Gateway services.
   - **Recommendation**: Use OpenShift RBAC or a ValidatingAdmissionPolicy to restrict which users/service accounts can create Services with the `gateway.networking.k8s.io/gateway-name` label in the `openshift-ingress` namespace.

## 7. Approval Status

**IMPLEMENTED — QA PASSED (97/100), SECURITY PASSED**

---

### Quick Reference: Key Constants and Identifiers

| Constant | Value | Source |
|----------|-------|--------|
| `OpenShiftDefaultGatewayClassName` | `openshift-default` | `pkg/operator/controller/names.go` |
| `OpenShiftGatewayClassControllerName` | `openshift.io/gateway-controller/v1` | `pkg/operator/controller/names.go` |
| `DefaultOperandNamespace` | `openshift-ingress` | `pkg/operator/controller/names.go` |
| Gateway service name pattern | `<gatewayName>-<gatewayClassName>` | Istio convention |
| Gateway name label | `gateway.networking.k8s.io/gateway-name` | Gateway API spec |
| Istio managed label | `gateway.istio.io/managed` | Istio convention |
| Istio status port | 15021/TCP | Istio default |
