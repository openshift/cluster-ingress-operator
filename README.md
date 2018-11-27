# Cluster Ingress Operator

Cluster Ingress Operator deploys and manages [HAProxy](http://www.haproxy.org) to provide highly available network ingress in [OpenShift](https://openshift.io/), enabling both OpenShift [Routes](https://docs.okd.io/latest/rest_api/apis-route.openshift.io/v1.Route.html) and Kubernetes [Ingress](https://kubernetes.io/docs/concepts/services-networking/ingress/).

The operator tries to be useful out of the box by creating a working default deployment based on the cluster's configuration.

* On cloud platforms, ingress is exposed externally using Kubernetes [LoadBalancer Services](https://kubernetes.io/docs/concepts/services-networking/#loadbalancer).
* On Amazon AWS, wildcard DNS for the ingress domain is automatically managed.

## How to help

See [HACKING.md](HACKING.md) for development topics.
