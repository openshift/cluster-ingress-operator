# Cluster Ingress Operator

Cluster Ingress Operator deploys and manages [HAProxy](http://www.haproxy.org) to provide highly available network ingress in [OpenShift](https://openshift.io/), enabling both OpenShift [Routes](https://docs.okd.io/latest/rest_api/apis-route.openshift.io/v1.Route.html) and Kubernetes [Ingresses](https://kubernetes.io/docs/concepts/services-networking/ingress/).

The operator tries to be useful out of the box by creating a working default deployment based on the cluster's configuration.

* On cloud platforms, ingress is exposed externally using Kubernetes [LoadBalancer Services](https://kubernetes.io/docs/concepts/services-networking/#loadbalancer).
* On Amazon AWS, the operator publishes a wildcard DNS record based on the [cluster ingress domain](https://github.com/openshift/api/blob/master/config/v1/types_ingress.go) and pointing to the service's load-balancer.

## How to help

See [HACKING.md](HACKING.md) for development topics.
