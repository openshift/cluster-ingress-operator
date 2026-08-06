# E2E Testing Patterns — CIO Correctness Guidelines

This document describes the required patterns for writing reliable e2e tests in this repository. These patterns prevent flaky test failures caused by transient Kubernetes API errors, resource version conflicts, cleanup ordering issues, race conditions, and logic bugs.

All rules apply to new code **and** to modifications of existing code. When touching a test, fix any violations in the lines you modify.

---

## 1. Resource Cleanup

### 1.1 Use `t.Cleanup()` instead of `defer`

`defer` runs when the enclosing function returns, but **does not run** when `t.FailNow()` or `t.Fatal()` is called from a subtest. `t.Cleanup()` is guaranteed to run regardless of how the test terminates.

```go
// CORRECT
if err := kclient.Create(context.TODO(), echoPod); err != nil {
    t.Fatalf("failed to create pod: %v", err)
}
t.Cleanup(func() { deleteWithRetryOnError(t, context.Background(), echoPod, 2*time.Minute) })

// WRONG — will not run if a subtest calls t.Fatal
defer func() {
    if err := kclient.Delete(context.TODO(), echoPod); err != nil {
        t.Fatalf("failed to delete pod: %v", err)
    }
}()
```

### 1.2 Use `deleteWithRetryOnError()` for all resource deletion

Direct `kclient.Delete()` calls fail on transient network errors or API server load. The `deleteWithRetryOnError()` helper retries deletion for up to the specified timeout, ignoring `NotFound` errors (the resource may already be gone).

```go
// CORRECT — retries on transient errors, ignores NotFound
t.Cleanup(func() { deleteWithRetryOnError(t, context.Background(), obj, 2*time.Minute) })

// WRONG — fatals on a single transient error
t.Cleanup(func() { assertDeleted(t, kclient, obj) })

// WRONG — inline delete without retry
t.Cleanup(func() {
    if err := kclient.Delete(context.TODO(), obj); err != nil {
        t.Fatalf("failed to delete: %v", err)
    }
})
```

**Standard cleanup timeout: `2*time.Minute`.**

### 1.3 Use `createWithRetryOnError()` for resource creation when appropriate

When creating resources that may transiently fail (e.g., during API server restarts), use the retry helper:

```go
if err := createWithRetryOnError(t, context.Background(), obj, 2*time.Minute); err != nil {
    t.Fatalf("failed to create resource: %v", err)
}
```

### 1.4 Never use `t.Fatalf()` inside cleanup functions

A `t.Fatalf()` in a cleanup function masks the original test failure and prevents other registered cleanup functions from running. Always use `t.Errorf()`:

```go
// CORRECT
t.Cleanup(func() {
    if err := restoreOriginalState(); err != nil {
        t.Errorf("failed to restore state: %v", err)  // logs error, other cleanups still run
    }
})

// WRONG — kills cleanup chain, leaks resources
t.Cleanup(func() {
    if err := restoreOriginalState(); err != nil {
        t.Fatalf("failed to restore state: %v", err)
    }
})
```

This also applies to `defer` blocks. Any `t.Fatalf` inside a `defer` calls `runtime.Goexit()`, preventing subsequent deferred functions from running and leaking resources (IngressControllers, Services, Pods, Routes, NetworkPolicies).

### 1.5 Cleanup for IngressControllers

Use `assertIngressControllerDeleted()` wrapped in `t.Cleanup()`:

```go
t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })
```

This helper handles the full deletion lifecycle including waiting for finalizers.

---

## 2. Error Handling in Delete/Create Helpers

### 2.1 Use correct error type checks for the operation

Delete operations return `NotFound` (not `AlreadyExists`). Create operations return `AlreadyExists` (not `NotFound`). Mixing them up inverts the retry logic:

```go
// CORRECT — Delete ignores NotFound
if err := kclient.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) {
    // retry
}

// CORRECT — Create ignores AlreadyExists
if err := kclient.Create(ctx, obj); err != nil && !errors.IsAlreadyExists(err) {
    // retry
}

// WRONG — Delete checking AlreadyExists (AlreadyExists never returned by Delete)
if err := kclient.Delete(ctx, obj); err != nil && !errors.IsAlreadyExists(err) {
    // BUG: swallows all real errors, retries on NotFound
}

// WRONG — inverted IsNotFound guard
if err := kclient.Delete(ctx, obj); err != nil && errors.IsNotFound(err) {
    t.Fatalf(...)  // BUG: fatals on harmless case, ignores real errors
}
```

### 2.2 Never use `wait.PollInfinite` or unbounded polling

Unbounded polling can hang the entire test suite if a resource gets stuck (finalizer, webhook, etc.). Always use `wait.PollUntilContextTimeout` or `wait.PollImmediate` with an explicit timeout:

```go
// CORRECT — bounded timeout
wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
    // ...
})

// WRONG — hangs forever if condition never met
wait.PollInfinite(5*time.Second, func() (bool, error) {
    // ...
})
```

### 2.3 Prefer `wait.PollUntilContextTimeout` over deprecated `wait.PollImmediate`

`wait.PollImmediate` is deprecated and doesn't accept a context, so operations can't be cancelled on test timeout, creating zombie goroutines. New code should use `wait.PollUntilContextTimeout`:

```go
// PREFERRED — context-aware, cancellable
wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
    // ...
})

// DEPRECATED — not context-aware
wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
    // ...
})
```

---

## 3. Updating Shared Resources

### 3.1 Always use retry-on-conflict helpers

Kubernetes uses optimistic concurrency via `resourceVersion`. A direct `Update()` call will fail with a conflict error if the resource was modified between `Get()` and `Update()`. The retry helpers re-fetch the resource before each attempt:

```go
// CORRECT — re-fetches, applies mutation, retries on conflict
if err := updateIngressControllerWithRetryOnConflict(t, name, timeout, func(ic *operatorv1.IngressController) {
    ic.Spec.Replicas = pointer.Int32(2)
}); err != nil {
    t.Fatalf("failed to update: %v", err)
}

// WRONG — stale resourceVersion causes conflict
if err := kclient.Get(context.TODO(), name, ic); err != nil { ... }
ic.Spec.Replicas = pointer.Int32(2)
if err := kclient.Update(context.TODO(), ic); err != nil { ... }  // conflict!
```

### 3.2 Available retry helpers

All defined in `test/e2e/util_test.go`:

| Helper | Resource | Updates |
|--------|----------|---------|
| `updateIngressControllerWithRetryOnConflict()` | `IngressController` | Spec fields |
| `updateIngressConfigSpecWithRetryOnConflict()` | `Ingress` (config.openshift.io) | Spec fields |
| `updateIngressConfigStatusWithRetryOnConflict()` | `Ingress` (config.openshift.io) | Status fields |
| `updateRouteWithRetryOnConflict()` | `Route` | Any fields |
| `updateAndVerifyInfrastructureConfigWithRetry()` | `Infrastructure` | Spec + status verification |
| `updateInfrastructureConfigStatusWithRetryOnConflict()` | `Infrastructure` | Status via typed client |

---

## 4. Shared Mutable State

### 4.1 Protect shared variables with atomic or mutex

Package-level variables shared between parallel tests must use `sync/atomic` or `sync.Mutex`:

```go
// CORRECT — atomic counter shared between parallel tests
var podCount atomic.Int32

func testHeaders(t *testing.T, ...) {
    count := podCount.Add(1)
    podName := fmt.Sprintf("test-pod-%d", count)
}

// WRONG — data race between parallel tests
var podCount int

func testHeaders(t *testing.T, ...) {
    podCount++
    podName := fmt.Sprintf("test-pod-%d", podCount)
}
```

### 4.2 Never mutate global config variables

Package-level variables like `dnsConfig` and `infraConfig` are initialized in `TestMain` and shared across all tests. Never re-read into these globals from individual tests:

```go
// CORRECT — read into a local variable
localDNS := configv1.DNS{}
if err := kclient.Get(ctx, types.NamespacedName{Name: "cluster"}, &localDNS); err != nil { ... }

// WRONG — corrupts the shared global for all other tests
if err := kclient.Get(ctx, types.NamespacedName{Name: "cluster"}, &dnsConfig); err != nil { ... }
dnsConfig.Spec.PrivateZone.ID = "corrupted-value"  // affects all subsequent tests
```

### 4.3 Serial tests that modify cluster-wide state must verify restoration

When a serial test modifies cluster-wide resources (default IC, cluster ingress config, infrastructure config), the cleanup must:
1. Use `t.Errorf` (not `t.Fatalf`) so other cleanups still run
2. Explicitly verify the state was restored before returning

```go
t.Cleanup(func() {
    if err := updateIngressConfigSpecWithRetryOnConflict(t, clusterConfigName, 5*time.Minute,
        func(spec *configv1.IngressSpec) {
            spec.RequiredHSTSPolicies = nil
        }); err != nil {
        t.Errorf("failed to restore ingress config: %v", err)
    }
})
```

---

## 5. Nil Safety

### 5.1 Guard `PlatformStatus` before access

`infraConfig.Status.PlatformStatus` can be nil on some platforms. Always guard:

```go
// CORRECT
if infraConfig.Status.PlatformStatus == nil {
    t.Skip("test skipped on nil platform")
}
platform := infraConfig.Status.PlatformStatus.Type

// WRONG — panics on nil PlatformStatus
platform := infraConfig.Status.PlatformStatus.Type
```

### 5.2 Never compare address-of-value to nil

Taking the address of a value variable and comparing to nil is always false (the address of a stack variable is never nil). Use direct value comparison or pointer types:

```go
// WRONG — always false, dead code
if &listenerStatus == nil {
    t.Logf("listener not found")
}

// CORRECT — check a pointer, or check for zero value
if listenerStatus == (SomeType{}) {
    t.Logf("listener not found")
}
```

### 5.3 Validate slice bounds before indexing

Never index into a slice without checking its length:

```go
// CORRECT
if len(record.Spec.Targets) == 0 {
    t.Fatalf("no targets in DNS record")
}
target := record.Spec.Targets[0]

// WRONG — panics if Targets is empty
target := record.Spec.Targets[0]
```

---

## 6. Timeout Conventions

| Operation | Minimum Timeout | Rationale |
|-----------|----------------|-----------|
| Controller condition polling | `3*time.Minute` | Controllers need time to reconcile, especially under load |
| Resource deletion cleanup | `2*time.Minute` | Allows for finalizer processing |
| Load balancer readiness | `5*time.Minute` | Cloud provisioning is slow |
| DNS resolution | `10*time.Minute` (`dnsResolutionTimeout`) | Propagation + negative cache TTL |
| IBM Cloud DNS warmup | `7*time.Minute` | Platform-specific negative caching |
| Quick API fetches | `1*time.Minute` | For simple Get/List operations |

Never use `10*time.Second` or similar tiny timeouts for controller condition checks. These cause flaky failures under CI load.

---

## 7. Error Wrapping

Use `%w` (not `%v`) in `fmt.Errorf` when the returned error may be inspected with `errors.Is()` or `errors.As()`:

```go
// CORRECT — preserves the error chain
return fmt.Errorf("failed to update ingresscontroller: %w", err)

// WRONG — loses the error type
return fmt.Errorf("failed to update ingresscontroller: %v", err)
```

---

## 8. Functions That Return Errors vs Call t.Fatal

### 8.1 Consistent function contract

If a helper function has an `error` return, it must NOT call `t.Fatalf` internally. Either return the error for the caller to handle, or call `t.Fatalf` and return nothing:

```go
// CORRECT — returns error, caller decides
func hardStopAfterTestIngressController(t *testing.T, ...) error {
    if err := updateIC(...); err != nil {
        return fmt.Errorf("failed to update: %w", err)
    }
    return nil
}

// WRONG — has error return but calls t.Fatalf (caller never sees the error)
func hardStopAfterTestIngressController(t *testing.T, ...) error {
    if err := updateIC(...); err != nil {
        t.Fatalf("failed to update: %v", err)  // runtime.Goexit, error return is dead code
    }
    return nil
}
```

---

## 9. Resource Naming and Collision Prevention

### 9.1 Use generated names for test resources

Hardcoded resource names cause `AlreadyExists` failures when tests are re-run after a cleanup failure. Prefer generated names:

```go
// PREFERRED — unique per run
name := names.SimpleNameGenerator.GenerateName("test-gateway-")

// ACCEPTABLE — descriptive but ensure cleanup is robust
name := "test-gateway-update"
```

### 9.2 Log messages must match the actual operation

Verify that `t.Log` messages accurately describe what the code does. Misleading log messages make debugging flaky tests significantly harder:

```go
// WRONG — says Classic, sets NLB
t.Log("resetting the default LB type to Classic")
spec.LoadBalancer.Platform.AWS.Type = configv1.NLB

// CORRECT
t.Log("resetting the default LB type to NLB")
spec.LoadBalancer.Platform.AWS.Type = configv1.NLB
```

---

## 10. Resource Lifecycle in Loops

### 10.1 Never `defer` resource cleanup inside a loop

`defer` in a loop accumulates until function exit. Use explicit cleanup or `t.Cleanup`:

```go
// WRONG — all log streams stay open until function returns
for _, pod := range pods.Items {
    logs, _ := getLogStream(pod)
    defer logs.Close()  // BUG: file descriptor leak during loop
    // ...
}

// CORRECT — close in each iteration
for _, pod := range pods.Items {
    logs, _ := getLogStream(pod)
    buf := new(bytes.Buffer)
    buf.ReadFrom(logs)
    logs.Close()
    // ...
}
```

---

## 11. Context Propagation in Nested Polling Loops

### 11.1 Inner polling contexts must derive from the outer context

When a polling loop is nested inside another (e.g., restarting a pod inside `verifyInternalIngressController`), the inner context must derive from the outer loop's context. This ensures that if the outer loop times out, the inner loop is cancelled automatically — no orphaned goroutines or hanging polls:

```go
// CORRECT — inner context derives from outer, cancellation propagates
err = wait.PollUntilContextTimeout(outerCtx, 10*time.Second, 10*time.Minute, false, func(outerCtx context.Context) (bool, error) {
    // ...
    deleteCtx, deleteCancel := context.WithTimeout(outerCtx, 2*time.Minute)  // derives from outer
    defer deleteCancel()
    wait.PollUntilContextTimeout(deleteCtx, 5*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
        // ...
    })
    // ...
})

// WRONG — inner context is independent, won't cancel when outer times out
deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 2*time.Minute)
```

### 11.2 Call cancel explicitly instead of deferring inside poll callbacks

`defer` inside a poll callback accumulates deferred calls across iterations (the callback is a function called repeatedly, but defers don't fire until the parent function returns). Call cancel explicitly after the inner poll completes:

```go
// PREFERRED — explicit cancel after use
deleteCtx, deleteCancel := context.WithTimeout(outerCtx, 2*time.Minute)
wait.PollUntilContextTimeout(deleteCtx, 5*time.Second, 2*time.Minute, true, ...)
deleteCancel()  // immediate cleanup

// AVOID — defer accumulates if callback runs multiple times
defer deleteCancel()  // won't fire until parent function returns
```

---

## 12. Test Helper Conventions

### 12.1 Always call `t.Helper()` in test helper functions

Every function that accepts `*testing.T` and is not a test itself must call `t.Helper()` at the top. This makes failure output point to the caller, not the helper:

```go
// CORRECT
func deleteWithRetryOnError(t *testing.T, ctx context.Context, obj client.Object, timeout time.Duration) {
    t.Helper()
    // ...
}

// WRONG — failures report this helper's line number, not the caller's
func deleteWithRetryOnError(t *testing.T, ctx context.Context, obj client.Object, timeout time.Duration) {
    // missing t.Helper()
}
```

### 12.2 Never discard return values from polling functions

Always check the error returned by `wait.PollUntilContextTimeout`. A silently dropped return value means timeout failures are invisible:

```go
// CORRECT — error is checked
if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, condFn); err != nil {
    t.Fatalf("timed out waiting for condition: %v", err)
}

// WRONG — timeout is silently swallowed
wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, condFn)
```

### 12.3 Always create a resource before registering its cleanup

Registering cleanup for a resource that was never created is a no-op at best and misleading at worst. Always verify `Create()` succeeded before `t.Cleanup()`:

```go
// CORRECT — create, check error, then register cleanup
svc := buildEchoService(pod.Name, pod.Namespace, pod.ObjectMeta.Labels)
if err := kclient.Create(context.TODO(), svc); err != nil {
    t.Fatalf("failed to create service: %v", err)
}
t.Cleanup(func() { deleteWithRetryOnError(t, context.Background(), svc, 2*time.Minute) })

// WRONG — cleanup registered but resource was never created
svc := buildEchoService(pod.Name, pod.Namespace, pod.ObjectMeta.Labels)
t.Cleanup(func() { deleteWithRetryOnError(t, context.Background(), svc, 2*time.Minute) })
// missing kclient.Create(...)
```

---

## 13. Test Structure

### Parallel vs Serial

- **Parallel tests** (~90): Use `t.Parallel()`, create their own `IngressController`, independent of each other
- **Serial tests** (~50): Modify cluster-wide resources (default IC, cluster ingress config, infrastructure config)

### Namespace creation

Use `createNamespace()` which handles:
1. Creating the namespace
2. Waiting for the `default` ServiceAccount
3. Waiting for the `system:image-pullers` RoleBinding
4. Registering cleanup with event dumping on failure

### Common test resource setup pattern

```go
func TestSomething(t *testing.T) {
    t.Parallel()

    // 1. Create IngressController
    ic := newPrivateController(name, domain)
    if err := kclient.Create(context.TODO(), ic); err != nil {
        t.Fatalf("failed to create ingresscontroller: %v", err)
    }
    t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

    // 2. Wait for readiness
    if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, conditions...); err != nil {
        t.Fatalf("failed to observe expected conditions: %v", err)
    }

    // 3. Create test workload (pod + service + route)
    echoPod := buildEchoPod(name.Name, name.Namespace)
    if err := kclient.Create(context.TODO(), echoPod); err != nil {
        t.Fatalf("failed to create pod: %v", err)
    }
    t.Cleanup(func() { deleteWithRetryOnError(t, context.Background(), echoPod, 2*time.Minute) })

    echoService := buildEchoService(echoPod.Name, echoPod.Namespace, echoPod.ObjectMeta.Labels)
    if err := kclient.Create(context.TODO(), echoService); err != nil {
        t.Fatalf("failed to create service: %v", err)
    }
    t.Cleanup(func() { deleteWithRetryOnError(t, context.Background(), echoService, 2*time.Minute) })

    echoRoute := buildRoute(echoPod.Name, echoPod.Namespace, echoService.Name)
    if err := kclient.Create(context.TODO(), echoRoute); err != nil {
        t.Fatalf("failed to create route: %v", err)
    }
    t.Cleanup(func() { deleteWithRetryOnError(t, context.Background(), echoRoute, 2*time.Minute) })

    // 4. Test logic...
}
```

---

## Available Test Helpers

### Resource builders (`util_test.go`)

| Builder | Returns | Purpose |
|---------|---------|---------|
| `buildEchoPod()` | `*corev1.Pod` | Socat-based echo server |
| `buildCurlPod()` | `*corev1.Pod` | Curl pod for HTTP testing |
| `buildExecPod()` | `*corev1.Pod` | Long-running pod for exec commands |
| `buildSlowHTTPDPod()` | `*corev1.Pod` | Slow-responding HTTP server |
| `buildEchoService()` | `*corev1.Service` | HTTP service for echo pods |
| `buildRoute()` | `*routev1.Route` | Route to a service |
| `buildRouteWithHost()` | `*routev1.Route` | Route with explicit host |
| `buildRouteWithHSTS()` | `*routev1.Route` | Route with HSTS annotation |
| `buildEchoReplicaSet()` | `*appsv1.ReplicaSet` | ReplicaSet wrapping echo pod |
| `buildTestPodNetworkPolicy()` | `*networkingv1.NetworkPolicy` | Allow-all policy for test pods |

### IngressController factories (`operator_test.go`)

| Factory | Strategy |
|---------|----------|
| `newPrivateController()` | Private (no LB, no DNS) |
| `newLoadBalancerController()` | LoadBalancer with managed DNS |
| `newNodePortController()` | NodePort |
| `newHostNetworkController()` | HostNetwork |

### Waiters and pollers

| Helper | Purpose |
|--------|---------|
| `waitForIngressControllerCondition()` | Poll for expected IC conditions |
| `waitForDeploymentComplete()` | Wait for deployment rollout |
| `waitForAvailableReplicas()` | Wait for replica count |
| `waitForClusterOperatorConditions()` | Poll ClusterOperator status |
| `waitForHTTPClientCondition()` | Poll HTTP endpoint with retry |
| `waitForPodReady()` | Wait for pod Running+Ready |
| `waitForLBAnnotation()` | Wait for annotation on LB service |

### Connectivity verification

| Helper | Use case |
|--------|----------|
| `verifyExternalIngressController()` | External HTTP via load balancer |
| `verifyInternalIngressController()` | Internal HTTP via curl pod |
| `checkRouteConnectivity()` | Simple route reachability check |
