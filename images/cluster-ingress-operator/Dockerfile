FROM centos:7

RUN yum install -y golang make

ENV GOPATH /go
RUN mkdir $GOPATH

COPY . $GOPATH/src/github.com/openshift/cluster-ingress-operator

RUN cd $GOPATH/src/github.com/openshift/cluster-ingress-operator \
    && make build \
    && cp $GOPATH/src/github.com/openshift/cluster-ingress-operator/cluster-ingress-operator /usr/bin/

RUN yum remove -y golang make

ENTRYPOINT ["/usr/bin/cluster-ingress-operator"]

USER 1001

LABEL io.k8s.display-name="OpenShift cluster-ingress-operator" \
      io.k8s.description="This is a component of OpenShift Container Platform and manages the lifecycle of cluster ingress components." \
      maintainer="Dan Mace <dmace@redhat.com>"
