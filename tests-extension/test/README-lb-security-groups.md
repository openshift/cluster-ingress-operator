# AWS NLB Security Groups Tests

Tests in `test/lb_security_groups.go` validate the IngressController's ability to manage AWS NLB security groups through the `spec.endpointPublishingStrategy.loadBalancer.providerParameters.aws.networkLoadBalancerParameters.securityGroups` field. Shared utilities are in `test/lb_security_groups_util.go` and `test/aws_util.go`.

## Source

Written for the `IngressControllerLBSecurityGroupsAWS` OCP feature gate.

## Describe Block

```text
[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups
```

## Test Cases

| Spec | Description | Tags |
|------|-------------|------|
| managed through the IngressController spec / provisions a LoadBalancer service with the specified security group | Creates an NLB IngressController with a security group and verifies the LB Service annotation and status | Ordered, AWS-only |
| managed through the IngressController spec / effectuates an update to the security groups | Adds a second security group, recreates the LB Service, and verifies the updated annotation | Ordered, AWS-only |
| managed through the IngressController spec / removes the security groups using the auto-delete-load-balancer annotation | Removes security groups via the auto-delete annotation and verifies cleanup | Ordered, AWS-only |
| managed through the IngressController spec / reflects the effective security groups in the IngressController status | Verifies status.securityGroups matches spec.securityGroups after removal | Ordered, AWS-only |
| set directly on the LoadBalancer service (unmanaged) / preserves a security groups annotation and reconciles once the spec matches | Sets annotation directly on the LB Service, verifies the operator preserves it, then reconciles once spec is updated | AWS-only |

## Suites

These tests carry the `[OCPFeatureGate:IngressControllerLBSecurityGroupsAWS]` tag and are automatically skipped on non-AWS platforms. They run under the combined suites:

```text
parallel  -> openshift/conformance/parallel  (non-Serial, non-Disruptive, non-Slow)
serial    -> openshift/conformance/serial    ([Serial] or [Disruptive], non-Slow)
all       -> (all non-Slow, sequential execution)
```

## How to Run

```bash
cd tests-extension/
make build

# List security groups test names
./bin/cluster-ingress-operator-tests-ext list -o names | grep "security groups"

# Run a specific test
./bin/cluster-ingress-operator-tests-ext list -o names | grep "provisions a LoadBalancer" | ./bin/cluster-ingress-operator-tests-ext run-test

# Run all tests of security group only
./bin/cluster-ingress-operator-tests-ext list -o names | grep IngressControllerLBSecurityGroupsAWS  | ./bin/cluster-ingress-operator-tests-ext run-test

# Run all parallel tests (includes security groups tests)
./bin/cluster-ingress-operator-tests-ext run-suite parallel

# Run all tests sequentially
./bin/cluster-ingress-operator-tests-ext run-suite all
```

## Prerequisites

- AWS cluster with the `IngressControllerLBSecurityGroupsAWS` feature gate enabled
- Admin-scoped `KUBECONFIG`
- The tests create temporary EC2 security groups and NLB IngressControllers, then clean them up via `DeferCleanup`
