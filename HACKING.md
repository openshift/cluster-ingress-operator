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

- [Pre-release script](https://github.com/openshift/cluster-ingress-operator/blob/master/hack/test-pre-release-ossm-images.sh) uses Konflux to pick up unreleased Index Image to create a custom catalog source.

- The Catalog Source can then be used to install an unreleased OSSM operator.

- Used by NI&D team to validate OSSM builds against GatewayAPI e2e tests before GA to catch bugs early.

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

- Obtain the Konflux Token 
1. Access the [CI Vault](https://vault.ci.openshift.org/ui/vault/secrets/kv/kv/list/selfservice/nid-ossm-token/).
2. Click on `Sign in with OIDC Provider` using Internal SSO credentials.
3. Click on kv.
4. Click on selfservice/nid-ossm-token.
5. Click on secrets.
6. Click on eyeball icon to see the `konflux-cluster-token` file
7. Cut and paste the token into a file named konflux.tmp

- Run the script

```shell
$ TOKEN="$(cat konflux.tmp)" AUTHSTAGE="$(cat /tmp/authstage)" AUTHBREW="$(cat /tmp/authbrew)" make test-pre-release-ossm
```

#### CI

- Post a test PR to this repository and look for `e2e-aws-pre-release-ossm` job ([job definition](https://github.com/openshift/release/blob/master/ci-operator/config/openshift/cluster-ingress-operator/openshift-cluster-ingress-operator-master.yaml#L146-L167)).

### Troubleshooting

- OSSM team provided a 1-year token to access Konflux cluster valid until 09/23/2026.

- Konflux Token, Brew and Stage secrets used by the CI job are stored in [CI Vault](https://vault.ci.openshift.org/) under `selfservice/nid-ossm-token/secrets` labelled `konflux-cluster-token`, `brew-secret`, `stage-secret` respectively.

- Custom catalog source relies on these secrets to become ready, you need to make sure that the Konflux token and pull secrets are valid.

### Konflux Applications

`ossm-fbc-v4-x`: Contains next unreleased (z-stream) of current released versions.

`ossm-fbc-next`: Initial builds of the next unreleased minor version.
