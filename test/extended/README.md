# OpenShift Cluster Ingress Operator Tests Extension
========================

This repository contains the tests for the OpenShift Cluster Ingress Operator for OpenShift.
These tests run against OpenShift clusters and are meant to be used in the OpenShift CI/CD pipeline.
They use the framework: https://github.com/openshift-eng/openshift-tests-extension

## How to Run the Tests Locally

| Command                                                                    | Description                                                              |
|----------------------------------------------------------------------------|--------------------------------------------------------------------------|
| `make tests-ext-build`                                                     | Builds the test extension binary.                                        |
| `./ingress-operator-ext-tests list`                                        | Lists all available test cases.                                         |
| `./ingress-operator-ext-tests run-suite <suite-name>`                      | Runs a test suite. e.g., `cluster-ingress-operator/parallel` |
| `./ingress-operator-ext-tests run <test-name>`                             | Runs one specific test.                                                  |


## How to Run the Tests Locally

The tests can be run locally using the `ingress-operator-ext-tests` binary against an OpenShift cluster.
Use the environment variable `KUBECONFIG` to point to your cluster configuration file such as:

```shell
export KUBECONFIG=path/to/kubeconfig
./ingress-operator-ext-tests run <test-name>
```

### Local Test using OCP

1. Use the `Cluster Bot` to create an OpenShift cluster.

**Example:**

```shell
launch 4.20 gcp,techpreview
```

2. Set the `KUBECONFIG` environment variable to point to your OpenShift cluster configuration file.

**Example:**

```shell
mv ~/Downloads/cluster-bot-2025-08-06-082741.kubeconfig ~/.kube/cluster-bot.kubeconfig
export KUBECONFIG=~/.kube/cluster-bot.kubeconfig
```

3. Run the tests using the `ingress-operator-ext-tests` binary.

**Example:**
```shell
./ingress-operator-ext-tests run-suite "cluster-ingress-operator/parallel"
```

## Writing Tests

You can write tests in the `test/extended/specs` directory.

## Development Workflow

- Add or update tests in: `test/extended/specs`
- Run `make build` to build the operator binary and `make tests-ext-build` for the test binary.
- You can run the full suite or one test using the commands in the table above.
- Before committing your changes:
    - Run `make tests-ext-update`
    - Run `make verify` to check formatting, linting, and validation

**Note:** Only add the label once. Do not update it again after future renames.

