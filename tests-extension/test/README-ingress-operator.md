# Ingress Operator Tests

Tests in `ingress_operator.go` validate IngressController lifecycle, router deployment behavior, and ingress operator functionality. Shared utilities are in `ingress_operator_util.go`.

## Source

Migrated from `openshift-tests-private/test/extended/router/ingress-operator.go`.

## Describe Block

```text
[sig-network-edge][Feature:IngressOperator]
```

## Test Cases

| OCP ID | Original Title | Tags |
|--------|---------------|------|
| OCP-26150 | Author:shudili-ROSA-OSD_CCS-ARO-Medium-26150-misc tests for ingress operator | parallel |
| OCP-22633 | Author:hongli-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-Medium-22633-The nodeSelector and tolerations of router deployment are controlled by ingresscontrolle | parallel |
| OCP-22636 | Author:mjoseph-ROSA-OSD_CCS-ARO-Critical-22636-The namespaceSelector of router is controlled by ingresscontroller | parallel |
| OCP-22637 | Author:mjoseph-ROSA-OSD_CCS-ARO-High-22637-The routeSelector of router is controlled by ingresscontroller | parallel |
| OCP-56772 | Author:shudili-Medium-56772-Ingress Controller does not set allowPrivilegeEscalation in the router deployment | [Serial] |
| OCP-60012 | Author:shudili-ROSA-OSD_CCS-ARO-NonPreRelease-Medium-60012-matchExpressions for routeSelector defined in an ingress-controller | parallel |
| OCP-60013 | Author:shudili-ROSA-OSD_CCS-ARO-NonPreRelease-Medium-60013-matchExpressions for namespaceSelector defined in an ingress-controller | parallel |
| OCP-62530 | Author:shudili-ROSA-OSD_CCS-ARO-Critical-62530-openshift ingress operator is failing to update router-certs | [Serial] |
| OCP-63832 | Author:asood-NonHyperShiftHOST-ConnectedOnly-ROSA-OSD_CCS-Medium-63832-Cluster ingress health checks and routes fail on swapping application router between public and private | parallel, AWS-only |
| OCP-64611 | Author:mjoseph-NonHyperShiftHOST-Critical-64611-Ingress operator support for private hosted zones in Shared VPC clusters | parallel, AWS-only |
| OCP-75907 | Author:shudili-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-High-75907-Ingress Operator should not always remain in the progressing state | [Disruptive] |
| OCP-75908 | Author:shudili-ROSA-OSD_CCS-ARO-High-75908-http2 connection coalescing component routing should not be broken with single certificate | [Disruptive] |
| OCP-75909 | Author:shudili-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-High-75909-Ingress Operator should not always remain in the progressing state | [Disruptive] |
| OCP-77283 | Author:shudili-ROSA-OSD_CCS-ARO-Critical-77283-Router should support SHA1 CA certificates in the default certificate chain | parallel |

## Suites

```text
ingress-operator/all       -> (all non-Slow, sequential execution)
ingress-operator/parallel  -> openshift/conformance/parallel  (non-Serial, non-Disruptive, non-Slow)
ingress-operator/serial    -> openshift/conformance/serial    ([Serial] or [Disruptive], non-Slow, sequential)
ingress-operator/slow      -> openshift/optional/slow         ([Slow])
```

## How to Run

```bash
cd tests-extension/
make build

# List ingress operator test names
./bin/cluster-ingress-operator-tests-ext list -o names | grep IngressOperator

# Run a specific test by OCP ID
./bin/cluster-ingress-operator-tests-ext list -o names | grep "60012" | ./bin/cluster-ingress-operator-tests-ext run-test

# Run only ingress operator parallel tests
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/parallel

# Run only ingress operator serial tests
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/serial

# Run all ingress operator tests (parallel + serial combined, sequential execution)
./bin/cluster-ingress-operator-tests-ext run-suite ingress-operator/all
```

## Running All Tests (Gateway API + Ingress Operator)

```bash
# Run all non-slow tests (parallel + serial combined)
./bin/cluster-ingress-operator-tests-ext run-suite all

# Run all parallel tests (Gateway API + Ingress Operator)
./bin/cluster-ingress-operator-tests-ext run-suite parallel

# Run all serial/disruptive tests (Gateway API + Ingress Operator)
./bin/cluster-ingress-operator-tests-ext run-suite serial
```
