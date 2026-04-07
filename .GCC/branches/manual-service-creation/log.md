# Architect Log: Manual Service Creation E2E Tests

## Session: 2026-04-07

### Observation

1. Explored the repository structure. The e2e tests live in `test/e2e/` with two key files:
   - `gateway_api_test.go`: Contains all Gateway API test functions, registered sequentially in `TestGatewayAPI`. The ordering is deliberate -- earlier tests install Istio, later tests depend on it.
   - `util_gatewayapi_test.go`: Contains helper functions for creating GatewayClasses, Gateways, asserting conditions, and waiting for services.

2. Reviewed PR #1384 (WIP, needs rebase). Key findings:
   - `testGatewayAPIManualLBService` was mostly structured but had INCORRECT behavioral assumptions -- it expected Istio to manage ports and selectors on pre-created services, which is wrong.
   - `testGatewayAPIManualClusterIPService` had a critical bug: references undefined variables from LB test scope.
   - The PR commented out ALL existing tests in `TestGatewayAPI` for dev convenience, breaking CI.
   - The `createGatewayService` helper had a misleading doc comment and boolean parameters (`setSelector`, `setManaged`) that create false optionality.
   - The `waitForGatewayService` helper is well-designed and reusable.
   - The PR vendors `stretchr/testify/require`.

3. Analyzed existing test patterns in the repository:
   - Tests use `isDNSManagementSupported()` to skip DNS-dependent tests.
   - `assertGatewaySuccessful()` waits for Accepted + Programmed conditions.
   - `createWithRetryOnError()` and `updateGatewaySpecWithRetry()` provide retry-with-backoff.
   - Tests use `t.Cleanup()` for resource deletion.
   - Test names follow `testGatewayAPI<Feature>` pattern.

### Thought

Initial analysis incorrectly assumed Istio would manage ports/selectors for LoadBalancer-type pre-created services. User correction clarified: Istio does NOT modify pre-created services AT ALL, regardless of service type. The user is fully responsible for all configuration.

This changes the test design fundamentally:
- Both LB and ClusterIP tests must pre-create services with ALL ports and selectors set.
- The core assertion is immutability: after Gateway creation and listener updates, the service must remain unchanged.
- The `createGatewayService` helper should always set the selector and managed label (no boolean toggles).
- The `createGatewayService` helper should accept explicit ports from the caller.

PR #1384's LB test was wrong in:
- Setting `setSelector: false` (expecting Istio to set it)
- Using `waitForGatewayService` with `portLen=1` (expecting Istio to add ports, growing to 2 then 3)
- Asserting port count growth after listener addition

### Action

Generated `plan.md` with corrected behavioral model:
- Both tests pre-create services with full configuration (ports + selector)
- Core assertion is service immutability
- `createGatewayService` helper redesigned to remove misleading boolean params
- Comprehensive documentation guidance for tech writers emphasizing user responsibility
- 100-point verification rubric

### Discovered Environment Quirks

- The `TestGatewayAPI` function has a top-level cleanup for `testGatewayName` ("test-gateway"). New tests use unique names to avoid conflicts.
- PR #1384 uses `assert.Equal` for non-fatal checks and `require.Len` for fatal checks. This pattern is correct.
- The `waitForGatewayService` helper uses `t.Fatalf` inside a polling function for the "more than 1 service" case -- acceptable since it indicates a test environment issue.
- The test ordering in `TestGatewayAPI` is sequential and deliberate. Tests that depend on Istio being installed must come after the installation test.

## Session: 2026-04-07 (Plan Update -- Regression Guards)

### Observation

User identified two gaps in the test plan that leave us exposed to silent Istio behavioral changes:
1. No test verifies what happens when a Gateway listener port is deliberately absent from the pre-created service. If a future Istio version starts "helping" by adding missing ports, we would not catch it.
2. No test verifies that the `gateway.istio.io/managed` label does not act as a trigger for Istio to start managing service fields. The label is required for Istio to recognize the service, but it must not grant Istio mutation authority.

### Thought

These are regression guards, not functional tests. The distinction matters:
- Functional tests verify "the feature works." These already exist in the plan (service immutability assertions).
- Regression guards verify "the feature does not silently break." These catch upstream Istio changes that alter the contract.

For the negative port test, the cleanest integration is to expand the existing LB and ClusterIP tests rather than creating separate test functions. The Gateway already gets updated with a new listener in step 7/8 -- we can add a SECOND listener (port 8443) at Gateway creation time (step 3), deliberately omit it from the service ports, and assert it never appears. This tests two things simultaneously: (a) omitted port at initial creation, and (b) omitted port after listener addition.

For the managed label test, the best approach is to add a step in the LB test (after the listener-addition assertions) that toggles the label off and on, then re-asserts immutability. This is placed in the LB test only since the behavior is label-driven, not type-driven -- testing it once is sufficient.

Design decision: I chose to integrate these into existing test scenarios rather than creating new test functions because:
- The setup cost (GatewayClass, Service, Gateway creation) is identical.
- Running them as part of the same test reduces total e2e runtime.
- The assertions are logically extensions of the existing immutability checks.

### Action

Updated `plan.md` with the following changes:
- Added objective items 5 and 6 covering negative port regression and managed label immutability.
- Modified LB test step 2 to note the deliberate omission of port 8443.
- Modified LB test step 3 to create the Gateway with TWO listeners (80 + 8443) from the start.
- Added LB test step 5b: negative port assertion confirming 8443 is absent.
- Modified LB test step 7 to add a THIRD listener (8080), since 8443 already exists.
- Added LB test step 9: managed label toggle test (remove, wait, re-add, wait, assert immutability).
- Applied the same negative port pattern to the ClusterIP test (steps 2, 3, 5, 8).
- Added assertion code patterns for negative port checks and managed label toggle.
- Updated the 100-point rubric: added checks 9 (negative port LB), 10 (managed label toggle), 15 (negative port ClusterIP), and rebalanced point allocations to maintain 100 total.

## Session: 2026-04-07 (Implementation)

### Observation

Read the full codebase context: `gateway_api_test.go`, `util_gatewayapi_test.go`, `pkg/operator/controller/names.go`, and PR #1384 diff. Confirmed all required imports, patterns, and constants.

### Action

Implemented the plan across two files:

**`test/e2e/util_gatewayapi_test.go`** -- Added three functions:
- `createGatewayService`: Creates a service following Istio manual ownership contract. Always sets gateway-name label, managed label, and selector. Sets externalTrafficPolicy=Local for LoadBalancer type. Accepts explicit ports list (no boolean toggles).
- `waitForGatewayService`: Polls until a service exists for the gateway with the expected port count. Returns the service for further assertions.
- `defaultManualServicePorts`: Returns the standard two ports (status-port 15021, http 80) used in both test functions.

**`test/e2e/gateway_api_test.go`** -- Added two test functions and registered them:
- `testGatewayAPIManualLBService`: Full LB test with 9 assertion steps including negative port guard and managed label toggle.
- `testGatewayAPIManualClusterIPService`: ClusterIP test with 7 assertion steps including negative port guard.
- Registered both in `TestGatewayAPI` after `testOperatorDegradedCondition` and before `testGatewayOpenshiftConditions`.

Added `intstr` and `ptr` imports to `util_gatewayapi_test.go`.

### Build Verification

`go build -tags e2e ./test/e2e/` completed successfully with no errors.

### Obstacles and Workarounds

None discovered during implementation. All required packages were already vendored (`testify/assert`, `testify/require`, `k8s.io/utils/ptr`). The `intstr` package was not previously imported in `util_gatewayapi_test.go` but was available in vendor.

## Session: 2026-04-07 (QA Verification)

### Agent: @qa

### Observation

Performed deep verification of the implementation against `plan.md` using the 100-point rubric. Read both modified files in full, cross-referenced every plan step, and verified compilation with `go build -tags e2e ./test/e2e/`.

### 100-Point Rubric Evaluation

| # | Check | Points | Score | Verdict |
|---|-------|--------|-------|---------|
| 1 | LB service pre-created with correct name/labels/ports/selector | 8 | 8 | PASS. `createGatewayService` sets name=`<gw>-<class>`, labels (`gateway-name`, `managed`), selector, and ports via `defaultManualServicePorts()`. ExternalTrafficPolicy=Local set for LB type. |
| 2 | Gateway reaches Accepted=True with pre-created LB service | 8 | 8 | PASS. Uses `assertGatewaySuccessful()` which waits for both Accepted and Programmed. |
| 3 | Gateway reaches Programmed=True with pre-created LB service | 8 | 8 | PASS. Same `assertGatewaySuccessful()` call checks Programmed=True. |
| 4 | LB service `externalTrafficPolicy` NOT mutated by Istio | 8 | 8 | PASS. Line 1559: `assert.Equal(t, corev1.ServiceExternalTrafficPolicyLocal, svc.Spec.ExternalTrafficPolicy, ...)`. Also checked post-listener-add (line 1604) and post-label-toggle (line 1637). |
| 5 | LB service ports NOT mutated by Istio | 8 | 8 | PASS. Line 1564: `require.Len(t, svc.Spec.Ports, 2, ...)`. Checked again after listener add (line 1605). |
| 6 | LB service selector NOT mutated by Istio | 4 | 4 | PASS. Line 1563: `assert.Equal(t, expectedSelector, svc.Spec.Selector, ...)`. Also post-label-toggle (line 1638). |
| 7 | No duplicate LB service created by Istio | 4 | 4 | PASS. Lines 1573-1580: Lists services by label, asserts `Len == 1`. |
| 8 | Adding listener does NOT modify LB service | 8 | 8 | PASS. Lines 1582-1609: Adds port-8080 listener, sleeps 10s, re-fetches, asserts type/policy/ports unchanged. Negative port check for 8443 and 8080. |
| 9 | Negative port: omitted port 8443 NOT added to LB service | 8 | 8 | PASS. Lines 1567-1569: Iterates `svc.Spec.Ports`, asserts `NotEqual(8443)`. Also checked post-listener-add (lines 1607-1608). |
| 10 | Managed label toggle does NOT trigger LB service mutation | 8 | 8 | PASS. Lines 1611-1639: Removes label, sleeps, re-adds, sleeps, re-fetches, asserts type/policy/selector/ports unchanged. |
| 11 | ClusterIP service type NOT mutated by Istio | 4 | 4 | PASS. Line 1723: `assert.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type, ...)`. Also post-listener-add (line 1767). |
| 12 | ClusterIP service ports NOT mutated by Istio | 8 | 8 | PASS. Line 1728: `require.Len(t, svc.Spec.Ports, 2, ...)`. Post-listener-add (line 1768). |
| 13 | ClusterIP service selector NOT mutated by Istio | 4 | 4 | PASS. Line 1727: `assert.Equal(t, expectedSelector, svc.Spec.Selector, ...)`. |
| 14 | Adding listener does NOT modify ClusterIP service | 4 | 4 | PASS. Lines 1746-1772: Same pattern as LB test. |
| 15 | Negative port: omitted port 8443 NOT added to ClusterIP service | 8 | 8 | PASS. Lines 1731-1733 and 1769-1771. |
| 16 | Cleanup succeeds without resource leaks | 4 | 4 | PASS. Both tests use `t.Cleanup` for Gateway and Service deletion with `errors.IsNotFound` guards. |
| **Total** | | **100** | **100** | |

### Detailed Findings

#### Critical Issues (BLOCKERS)

None found.

#### Major Issues

None found.

#### Minor Issues

1. **[MINOR] `time.Sleep` usage for reconciliation waiting** (LB test lines 1597, 1620, 1630; ClusterIP test line 1763): The tests use `time.Sleep(10 * time.Second)` to wait for Istio reconciliation. While this is an accepted pattern in the existing test suite (e.g., `testGatewayOpenshiftConditions` uses `time.Sleep(time.Second)` at line 726), it introduces timing sensitivity. In CI environments with higher latency, 10 seconds may be insufficient; on fast clusters it wastes time. However, since the alternative (polling for "nothing changed") is inherently racy and the existing tests use the same pattern, this is acceptable.

2. **[MINOR] `waitForGatewayService` helper is declared but not used by the new tests** (lines 1610-1643 in `util_gatewayapi_test.go`): The tests fetch the service directly via `kclient.Get` rather than using `waitForGatewayService`. The helper is still valid infrastructure for future tests, and Go allows unused functions (unlike unused variables). Not a blocker.

3. **[MINOR] The ClusterIP test does not have the managed label toggle step**: This is by design per the plan -- the label toggle test is in the LB test only. Documented here for traceability.

### Code Idioms Verification

- **assert vs require**: Correctly used. `require.Len` is used before any port indexing (preventing panics). `assert.Equal` used for non-fatal field checks. Matches plan section 3.8.
- **t.Helper()**: Present on `createGatewayService`, `waitForGatewayService` helpers. Test functions do not need it (they are top-level).
- **t.Cleanup()**: Both tests register cleanup for Gateway and Service. Cleanup guards against `IsNotFound` errors.
- **t.Logf()**: Used consistently for progress reporting, matching existing test patterns.
- **Error handling**: All `kclient.Get`, `kclient.List`, `kclient.Create`, `kclient.Update`, `kclient.Delete` calls have error checks.
- **Context propagation**: Uses `context.Background()` consistently with existing patterns.

### Consistency with Existing Patterns

- Test names follow `testGatewayAPI<Feature>` convention.
- Registration placement in `TestGatewayAPI` matches plan (after `testOperatorDegradedCondition`, before `testGatewayOpenshiftConditions`).
- Constants `OpenShiftDefaultGatewayClassName` and `OpenShiftGatewayClassControllerName` sourced from `operatorcontroller` package.
- `createGatewayClass` helper reused from existing codebase.
- `createWithRetryOnError` used for Gateway creation (retry-with-backoff pattern).
- `updateGatewaySpecWithRetry` used for adding listeners (existing helper, no reinvention).
- DNS skip pattern (`isDNSManagementSupported`) correctly applied to LB test only.
- ClusterIP test correctly does NOT skip on non-DNS platforms.

### Build Verification

`go build -tags e2e ./test/e2e/` succeeded with zero errors.

### Thought

The implementation is a faithful and complete realization of the plan. All 16 rubric checks pass. The code follows existing patterns precisely. The only minor observations (sleep timing, unused helper) do not affect correctness or functionality. The negative port regression guards and managed label toggle tests are particularly well-integrated -- they extend the existing immutability assertions without adding separate test functions or setup cost.

### Decision

**Grade: 97/100**

Point deductions:
- -2 for `waitForGatewayService` helper being declared but not used by the new tests (minor dead code risk, mitigated by being useful infrastructure).
- -1 for `time.Sleep` usage instead of polling-based wait (accepted pattern but suboptimal).

**Approval Status: RELEASE**

## Session: 2026-04-07 (Security Review -- @security)

### Agent: Offensive Security Specialist

### Observation

Conducted offensive security review of the manual service e2e test implementation across two files:
- `test/e2e/gateway_api_test.go` (lines 1479-1775): `testGatewayAPIManualLBService`, `testGatewayAPIManualClusterIPService`
- `test/e2e/util_gatewayapi_test.go` (lines 1555-1663): `createGatewayService`, `waitForGatewayService`, `defaultManualServicePorts`

Also reviewed:
- `manifests/00-cluster-role.yaml`: Operator ClusterRole (RBAC posture)
- `pkg/manifests/assets/router/namespace.yaml`: `openshift-ingress` namespace definition
- `.GCC/branches/manual-service-creation/plan.md`: Feature design and documentation guidance

### Thought

Analyzed the implementation through the lens of an attacker who has:
1. Compromised a workload in a non-privileged namespace and is looking for lateral movement
2. Gained limited cluster access (e.g., developer role) and wants to hijack gateway traffic
3. Compromised the operator pod and wants to escalate

---

## 1. Summary: Risk Profile Assessment

The test implementation itself is well-scoped and defensively written. No hardcoded secrets, no overly permissive RBAC created by the tests, and proper cleanup via `t.Cleanup()`. The primary security concerns are not in the test code itself but in the production feature pattern that the tests validate, and in the existing operator RBAC posture which predates this change.

Overall risk: MEDIUM. The feature pattern (manual service creation in `openshift-ingress`) introduces a new attack surface that is adequately mitigated by existing OpenShift RBAC defaults but requires explicit documentation for operators.

## 2. Critical Vulnerabilities (BLOCKERS)

None identified. No blocking issues in the test implementation.

## 3. Risk Analysis: Attacker's Workflow

### 3.1 Service Hijacking via Label Injection (MEDIUM)

Attack vector: An attacker with `create` permissions on Services in the `openshift-ingress` namespace could pre-create a Service with the label `gateway.networking.k8s.io/gateway-name: <targetGatewayName>` and `gateway.istio.io/managed: openshift.io-gateway-controller-v1` BEFORE a legitimate gateway is created. This would cause Istio to use the attacker-controlled service (with a malicious selector pointing to attacker pods) instead of auto-provisioning a correct one.

Mitigating factors:
- The `openshift-ingress` namespace has `pod-security.kubernetes.io/enforce: privileged` and is a system namespace. Standard OpenShift RBAC does NOT grant developer/editor roles access to create Services in this namespace.
- Only `system:admin`, `cluster-admin`, and the ingress operator ServiceAccount have write access by default.
- The naming contract (`<gatewayName>-<gatewayClassName>`) is deterministic, making it possible to predict service names, but namespace access controls are the primary gate.

Residual risk: If a cluster administrator grants a non-admin user Service `create` permissions in `openshift-ingress` (even unintentionally via an overly broad ClusterRoleBinding), that user could hijack any future Gateway's traffic.

Severity: MEDIUM. The attack requires pre-existing namespace-level permissions that break OpenShift's default security model.

### 3.2 Selector Injection on Pre-Created Services (MEDIUM)

Attack vector: The manual service pattern requires the user to set the selector `gateway.networking.k8s.io/gateway-name: <gatewayName>`. The test correctly validates that Istio does not modify this selector. However, the feature documentation (plan.md Section 6) does not warn users that:
- Setting the selector to a value OTHER than the gateway name would route traffic to arbitrary pods
- An attacker who can modify the Service in `openshift-ingress` could change the selector post-creation to redirect traffic to attacker-controlled pods

Mitigating factors:
- Same namespace-level RBAC controls as 3.1 apply
- The selector targets pods with label `gateway.networking.k8s.io/gateway-name`, which is set by Istio on gateway deployment pods -- an attacker would also need to create pods with this label in the same namespace

Severity: MEDIUM. Requires write access to `openshift-ingress` namespace.

### 3.3 Operator ClusterRole Wildcard Verbs (INFORMATIONAL -- pre-existing)

Observation: The operator's ClusterRole (`manifests/00-cluster-role.yaml`) uses wildcard `"*"` verbs on core resources including `services`, `secrets`, `pods`, `configmaps`, `namespaces`, `serviceaccounts`, and on `apps/deployments`, `apps/daemonsets`, `apiextensions.k8s.io/customresourcedefinitions`, `operators.coreos.com/subscriptions`, and `sailoperator.io/istios`.

This is NOT introduced by this PR but is relevant context: if the ingress operator pod is compromised, the attacker inherits cluster-wide `*` on services, secrets, and pods. This is a known pre-existing risk flagged by the TODO comment in the manifest itself ("A lot of these should be replaced by roles in the namespaces for which the operator actually needs access").

Lateral movement path if operator is compromised:
1. Read all Secrets cluster-wide (TLS certs, SA tokens, image pull secrets)
2. Create pods in any namespace (pivot to node via hostPath/hostNetwork if SCC allows)
3. Create/modify CRDs (install backdoor admission webhooks)
4. Create Istio resources (inject sidecar proxies for traffic interception)

Severity: LOW (informational, pre-existing, out of scope for this PR).

### 3.4 Test Resource Cleanup (LOW)

Both test functions use `t.Cleanup()` to delete the Gateway and Service resources. The cleanup functions use `errors.IsNotFound(err)` guards. Go's `t.Cleanup()` runs in LIFO order, so the Service cleanup (registered first) runs after the Gateway cleanup (registered second). This is the correct order -- delete the consumer before the backing service. No leaked resources that could be exploited.

Severity: LOW. No security issue found.

### 3.5 Privileged Namespace Operations in Tests (LOW)

The e2e tests create Services directly in `openshift-ingress` (a privileged namespace). The test harness uses a `kclient` that has cluster-admin level access, which is expected for e2e tests. No additional RBAC is created by the tests. The tests do not create ServiceAccounts, Roles, or RoleBindings.

Severity: LOW. Standard e2e test pattern.

### 3.6 No Hardcoded Credentials (PASS)

Grep scan for `password`, `token`, `secret`, `credential`, `bearer`, `apikey`, `api_key` returned zero matches in the test files. No supply chain concerns -- no new dependencies were added (all packages already vendored).

Severity: PASS. No findings.

## 4. Recommendations

### For this PR (should fix -- MEDIUM priority):

1. **Documentation: Add RBAC/security warning to Section 6 of plan.md**. The documentation guidance should explicitly instruct tech writers to include a security callout:
   - "Manual service creation requires write access to the `openshift-ingress` namespace. Cluster administrators MUST NOT grant Service create/update permissions in this namespace to non-admin users, as this would allow traffic interception for Gateway workloads."
   - "Users who pre-create services are responsible for ensuring the selector correctly targets gateway pods. An incorrect selector will route traffic to unintended destinations."

2. **Documentation: Warn about deterministic naming**. The service naming pattern `<gatewayName>-<gatewayClassName>` is predictable. Documentation should note that anyone who knows the gateway name can predict the service name, reinforcing that namespace RBAC is the security boundary.

### For the broader project (consider fixing, out of scope for this PR):

3. **Descope operator ClusterRole wildcards** (pre-existing issue). Replace `"*"` verbs with explicit verb lists on core resources. Scope service/secret/pod access to `openshift-ingress` and `openshift-ingress-operator` namespaces using Roles instead of ClusterRoles where possible.

4. **Consider admission control for gateway services** (enhancement). A ValidatingAdmissionPolicy could enforce that Services in `openshift-ingress` with the `gateway.networking.k8s.io/gateway-name` label can only be created/modified by the operator SA or cluster-admin. This would harden against the label injection scenario even if namespace RBAC is misconfigured.

## 5. Approval Status

**SECURE** -- with the documentation recommendations noted above.

The test code itself is clean: no credentials, no RBAC escalation, proper cleanup, no injection vectors, no new dependencies. The feature pattern it validates introduces a MEDIUM-risk attack surface that is adequately mitigated by OpenShift's default namespace RBAC, but documentation should explicitly call this out for cluster administrators.

### Files Reviewed

- `/home/rpchevuz/codes/work/cluster-ingress-operator/test/e2e/gateway_api_test.go` (lines 1479-1775)
- `/home/rpchevuz/codes/work/cluster-ingress-operator/test/e2e/util_gatewayapi_test.go` (lines 1555-1663)
- `/home/rpchevuz/codes/work/cluster-ingress-operator/manifests/00-cluster-role.yaml`
- `/home/rpchevuz/codes/work/cluster-ingress-operator/manifests/01-role.yaml`
- `/home/rpchevuz/codes/work/cluster-ingress-operator/manifests/01-cluster-role-binding.yaml`
- `/home/rpchevuz/codes/work/cluster-ingress-operator/pkg/manifests/assets/router/namespace.yaml`
- `/home/rpchevuz/codes/work/cluster-ingress-operator/.GCC/branches/manual-service-creation/plan.md`
