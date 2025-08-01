FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.24-openshift-4.20 AS builder
WORKDIR /ingress-operator
COPY . .
RUN make build

FROM registry.ci.openshift.org/ocp/4.20:base-rhel9
COPY --from=builder /ingress-operator/ingress-operator /usr/bin/
COPY manifests /manifests
ENTRYPOINT ["/usr/bin/ingress-operator"]
LABEL io.openshift.release.operator="true"
LABEL io.k8s.display-name="OpenShift ingress-operator" \
      io.k8s.description="This is a component of OpenShift Container Platform and manages the lifecycle of ingress controller components." \
      maintainer="Miciah Masters <mmasters@redhat.com>"
