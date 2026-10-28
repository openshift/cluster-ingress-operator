# OSSM Subscription and Istio Version Overrides

Ingress Operator implements Gateway API through OpenShift Service Mesh, which is
based on Istio and Envoy (see the [gateway-api-with-cluster-ingress-operator](https://github.com/openshift/enhancements/blob/master/enhancements/ingress/gateway-api-with-cluster-ingress-operator.md)
enhancement proposal).  When Gateway API is enabled on a cluster, Ingress
Operator creates an OLM subscription to install OSSM.  In usual operation, the
source catalog, channel, and version that Ingress Operator specifies on the OSSM
subscription come from [the Ingress Operator deployment manifest](../manifests/02-deployment.yaml).  However,
Ingress Operator provides a set of **unsupported** annotations by which the
source catalog, channel, and version for the OSSM subscription and version of
Istio can be overridden.

**Note: These annotations are only intended for use by OpenShift developers.
Use of these annotations is unsupported**

To use these annotations, specify them when creating the GatewayClass:

``` shell
oc create -f -<<'EOF'
apiVersion: gateway.networking.k8s.io/v1beta1
kind: GatewayClass
metadata:
  name: openshift-default
  annotations:
    unsupported.do-not-use.openshift.io/ossm-catalog: redhat-operators
    unsupported.do-not-use.openshift.io/ossm-channel: stable
    unsupported.do-not-use.openshift.io/ossm-version: servicemeshoperator3.v3.3.8
    unsupported.do-not-use.openshift.io/istio-version: v1.28-latest
spec:
  controllerName: openshift.io/gateway-controller/v1
EOF
```

`v1.28-latest` is a Sail version alias, not an OCI image tag. In the direct
Sail Library path, Ingress Operator resolves the alias through Sail's embedded
version catalog before installing the concrete version's charts. In the OLM
path, the alias is passed unchanged to the Istio resource for OSSM to resolve.
The concrete version follows the bundled Sail catalog; this does not select a
mutable `:1.28-latest` image tag.

**Note: The OSSM override annotations are intended to be used when initially
creating the GatewayClass CR and associated Subscription CR.**  If you set the
annotations *after* the Subscription has already been created, then Ingress
Operator will update the Subscription, but OLM might ignore the change or report
an error.  In particular, OLM does not allow downgrading, and OLM typically only
allows upgrading to the next version after the currently installed version.
