# Cluster Ingress Operator Hacking


## Local development

It's possible (and useful) to develop the operator locally targeting a remote cluster.

### Prerequisites

* An OpenShift cluster with at least a master, infra, and compute node. **Note**: in most cases, the master and infra nodes should *not be colocated* for testing the ingress operator. This is because Kubernetes will reject master nodes as LoadBalancer Service endpoints.
* An admin-scoped `KUBECONFIG` for the cluster.
* The [operator-sdk](https://github.com/operator-framework/operator-sdk).

#### GCP test clusters

One reliable and hands-free way to create a suitable test cluster and `KUBECONFIG` in GCP is to use the [openshift/release](https://github.com/openshift/release/tree/master/cluster/test-deploy) tooling. For a multi-node environment with isolated infra nodes, add the following to your `zz_vars.yaml` in the `gcp-dev` profile, adjusting the `scale` keys to suit your needs:

```yaml
openshift_gcp_node_group_config:
  - name: master
    suffix: m
    tags: ocp-master,ocp-node
    machine_type: n1-standard-1
    boot_disk_size: 150
    scale: 1
    bootstrap: true
    wait_for_stable: true
  - name: infra
    suffix: i
    tags: ocp-infra-node,oc-node
    machine_type: n1-standard-1
    boot_disk_size: 150
    scale: 2
    bootstrap: true
  - name: node
    suffix: n
    tags: ocp-node
    machine_type: n1-standard-2
    boot_disk_size: 150
    scale: 2
    bootstrap: true

openshift_node_groups:
- name: node-config-master
  labels:
  - 'node-role.kubernetes.io/master=true'
- name: node-config-infra
  labels:
  - 'node-role.kubernetes.io/infra=true'
- name: node-config-node
  labels:
  - 'node-role.kubernetes.io/compute=true'
- name: node-config-compute
  labels:
  - 'node-role.kubernetes.io/compute=true'

openshift_gcp_infra_network_instance_group: ig-i
```

### Building

To build the operator during development, use the standard Go toolchain:

```
$ go build ./...
```

### Container image

To build the operator docker image:

##### Using docker

```
docker build -f images/cluster-ingress-operator/Dockerfile  -t openshift/cluster-ingress-operator:latest .
```

##### Using buildah

```
sudo buildah bud -f images/cluster-ingress-operator/Dockerfile  -t openshift/cluster-ingress-operator:latest .
```

##### Push it to a registry if needed

```
sudo buildah push openshift/cluster-ingress-operator:latest docker-daemon:openshift/cluster-ingress-operator:latest
```


### Running

To run the operator, first deploy the custom resource definitions:

```
$ oc create -f deploy/crd.yaml
```

Then, use the operator-sdk to launch the operator:

```
$ operator-sdk up local namespace default --kubeconfig=$KUBECONFIG
```

If you're using the `openshift/release` tooling, `KUBECONFIG` will be something like `$RELEASE_REPO/cluster/test-deploy/gcp-dev/admin.kubeconfig`.

To test the default `ClusterIngress` manifest:

```
$ oc create -f deploy/cr.yaml
```
