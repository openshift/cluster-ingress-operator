# Cluster Ingress Operator Hacking

## Building

To build the operator, run:

```
$ make build
```

## Developing

### Prerequisites

* An [OpenShift cluster](https://github.com/openshift/installer)
* An admin-scoped `KUBECONFIG` for the cluster.

Uninstall the existing operator and all its managed components:

```
$ hack/uninstall.sh
```

Build a new image and custom manifests:

```
$ REPO=docker.io/you/origin-cluster-ingress-operator make release-local
```

Follow the instructions to install the operator, e.g.:

```
$ oc apply -f /tmp/manifests/path
```

## Tests

Run unit tests:

```
$ make test
```

Assuming `KUBECONFIG` is set, run end-to-end tests:

```
$ make test-e2e
```
