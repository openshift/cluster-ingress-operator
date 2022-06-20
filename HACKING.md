# Ingress Controller Operator Hacking

## Building

To build the operator, run:

```
$ make build
```

## Developing

### Prerequisites

* An [OpenShift cluster](https://github.com/openshift/installer)
* An admin-scoped `KUBECONFIG` for the cluster.

#### Building Locally & Deploying to the Cluster

To build the operator on your local machine and deploy it to the cluster, first uninstall the existing operator and all its managed components:

```
$ make uninstall
```

Build a new image and custom manifests:

```
$ REPO=docker.io/you/cluster-ingress-operator make release-local
```

Follow the instructions to install the operator, e.g.:

```
$ oc apply -f /tmp/manifests/path
```

Note, `make uninstall` scales the CVO to 0 replicas. To scale the CVO back up when testing is complete, run:

```
$ oc scale --replicas 1 -n openshift-cluster-version deployments/cluster-version-operator
```

#### Building & Running the Operator Locally

This allows you to quickly test changes to the operator without pushing any code or images to the cluster.

To build the operator binary locally:

```
$ make build
```

To run the operator binary in the cluster from your local machine (as opposed to on the cluster in a pod):

```
$ make run-local
```

Set `ENABLE_CANARY=true` in your environment (or inline with the `run-local` command) to enable the ingress canary.


Note, to rescale the operator on the cluster after local testing is complete, scale the CVO back up with:

```
$ oc scale --replicas 1 -n openshift-cluster-version deployments/cluster-version-operator
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

## IBM Cloud notes

The cluster-version-operator (CVO) overrides changes made to the ingress operator configuration.

Some of the scripts above scale down the CVO to 0 replica so that your hacks to the ingress configuration are not overwritten by the CVO.

On IBM Cloud, the CVO deployment cannot be scaled down as running on a managed master node. As a workaround, to avoid overriding the changes to the ingress operator configuration, you can instead use overrides in the CVO configuration (ClusterVersion version configuration) - eg:

``` yaml
  overrides:
    - group: apps
      kind: Deployment
      name: ingress-operator
      namespace: openshift-ingress-operator
      unmanaged: true
    - group: apiextensions.k8s.io
      kind: CustomResourceDefinition
      name: ingresscontrollers.operator.openshift.io
      namespace: openshift-ingress-operator
      unmanaged: true
```
