FROM openshift/origin-release:golang-1.10 as builder
COPY . /go/src/github.com/openshift/cluster-ingress-operator/
RUN cd /go/src/github.com/openshift/cluster-ingress-operator && make build

FROM centos:7
LABEL io.openshift.release.operator true
LABEL io.k8s.display-name="OpenShift cluster-ingress-operator" \
      io.k8s.description="This is a component of OpenShift Container Platform and manages the lifecycle of cluster ingress components." \
      maintainer="Dan Mace <dmace@redhat.com>"

COPY --from=builder /go/src/github.com/openshift/cluster-ingress-operator/cluster-ingress-operator /usr/bin/
COPY manifests /manifests

RUN useradd cluster-ingress-operator
USER cluster-ingress-operator

ENTRYPOINT ["/usr/bin/cluster-ingress-operator"]
