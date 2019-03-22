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

#### Building Locally

To build the operator on your local machine and deploy it to the cluster, first uninstall the existing operator and all its managed components:

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

#### Building Remotely on the Cluster

To build the operator on the remote cluster, first create a buildconfig on the cluster:

```
$ make buildconfig
```

The above command will create a buildconfig using the current branch and the URL for the default push remote.  You can also specify an explicit branch or repository URL:

```
$ make buildconfig GIT_BRANCH=<branch> \
     GIT_URL=https://github.com/<username>/cluster-ingress-operator.git
```

Note: If a buildconfig already exists from an earlier `make buildconfig` command, `make buildconfig` will update the existing buildconfig.

Next, start a build from this buildconfig:

```
$ make cluster-build
```

Alternatively, if you want to see the logs during the build, specify the `V` flag:

```
$ make cluster-build V=1
```

Use the `DEPLOY` flag to start a build and then patch the operator to use the newly built image:

```
$ make cluster-build DEPLOY=1
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
