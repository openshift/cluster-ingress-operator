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

Build a new image and custom manifests using default Dockerfile:

```
$ REPO=docker.io/you/cluster-ingress-operator make release-local
```
or using the UBI based Dockerfile:
```
$ DOCKERFILE=Dockerfile.ubi REPO=docker.io/you/cluster-ingress-operator make release-local
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

## OSSM Pre-release Image Testing

### Purpose

- [Pre-release script](https://github.com/openshift/cluster-ingress-operator/blob/master/hack/setup-ossm-pre-release-testing.sh) applies brew and stage pull secrets, along with mirror sets to
be used when running pre-release testing for OSSM.

- Used by NI&D team to setup validation for OSSM builds against GatewayAPI e2e tests before GA to 
catch bugs early.

### Getting Started

#### Locally

- Connect to Red Hat VPN.

- Create new service accounts for [stage registry](https://access.stage.redhat.com/terms-based-registry/accounts) and [brew registry](https://access.redhat.com/terms-based-registry/accounts).

- Obtain Stage Pull Secret using the Service Account credentials
```shell
$ podman login --authfile=/tmp/authstage --username="${STAGE_USER}" --password="${STAGE_PASS}" registry.stage.redhat.io
```

- Obtain Brew Pull Secret using the Service Account credentials
```shell
$ podman login --authfile=/tmp/authbrew --username="${BREW_USER}" --password="${BREW_PASS}" brew.registry.redhat.io
```

- Run the script

```shell
$ AUTHSTAGE="$(cat /tmp/authstage)" AUTHBREW="$(cat /tmp/authbrew)" make setup-ossm-pre-release
```

### Troubleshooting

- Brew and Stage secrets used by the CI job are stored in [CI Vault](https://vault.ci.openshift.org/) under `selfservice/nid-ossm-token/secrets` labelled `brew-secret`, `stage-secret` respectively.

- The pre-release images for the CI jobs rely on these secrets to become ready, you need to make sure that the pull secrets are valid.
