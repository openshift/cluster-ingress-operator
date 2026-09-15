# Gateway API Tests

Tests in `gatewayapi.go` validate Gateway API CRD lifecycle on OpenShift clusters.

## Source

Migrated from `openshift/origin`.

## Describe Block

```text
[sig-network][OCPFeatureGate:GatewayAPI][Feature:Router][apigroup:gateway.networking.k8s.io]
```

## Test Cases

| Test | Description |
|------|-------------|
| CRDs should already be installed | Verifies all required Gateway API CRDs exist |
| Existing CRDs can not be deleted | Ensures ValidatingAdmissionPolicy blocks CRD deletion |
| Existing CRDs can not be updated | Ensures ValidatingAdmissionPolicy blocks CRD modification |
| CRD of standard group can not be created | Blocks creation of new CRDs in the gateway.networking.k8s.io group |
| CRD of experimental group is not installed | Ensures no experimental-channel CRDs are present |

## Suites

```text
gateway-api/parallel  -> openshift/conformance/parallel  (non-Serial, non-Disruptive, non-Slow)
```

## Running

```bash
cd tests-extension/
make build

# Run only Gateway API parallel tests
./bin/cluster-ingress-operator-tests-ext run-suite gateway-api/parallel

# List Gateway API test names
./bin/cluster-ingress-operator-tests-ext list -o names | grep GatewayAPI
```

## Running All Tests (Gateway API + Ingress Operator)

```bash
# Run all non-slow tests (parallel + serial combined)
./bin/cluster-ingress-operator-tests-ext run-suite all

# Run all parallel tests (Gateway API + Ingress Operator)
./bin/cluster-ingress-operator-tests-ext run-suite parallel
```
