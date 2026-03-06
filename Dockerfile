FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.25-openshift-4.22 AS builder
WORKDIR /ingress-operator
COPY . .
RUN make build && \
    # Build the Cluster Ingress Operator Test Extension binary.
    # This is used by openshift-tests to run ingress operator tests as an OTE extension.
    cd tests-extension && make build && \
    cp bin/cluster-ingress-operator-tests-ext /tmp/ && \
    gzip -f /tmp/cluster-ingress-operator-tests-ext

FROM registry.ci.openshift.org/ocp/4.22:base-rhel9
COPY --from=builder /ingress-operator/ingress-operator /usr/bin/
COPY --from=builder /tmp/cluster-ingress-operator-tests-ext.gz /usr/bin/
COPY manifests /manifests
ENTRYPOINT ["/usr/bin/ingress-operator"]
LABEL io.openshift.release.operator="true"
LABEL io.k8s.display-name="OpenShift ingress-operator" \
      io.k8s.description="This is a component of OpenShift Container Platform and manages the lifecycle of ingress controller components." \
      maintainer="Miciah Masters <mmasters@redhat.com>"
