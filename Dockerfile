FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.17-openshift-4.10 AS builder
WORKDIR /ingress-operator
COPY . .
RUN make build

FROM registry.ci.openshift.org/ocp/4.10:base
COPY --from=builder /ingress-operator/ingress-operator /usr/bin/
COPY manifests /manifests
ENTRYPOINT ["/usr/bin/ingress-operator"]
LABEL io.openshift.release.operator="true"
LABEL io.k8s.display-name="OpenShift ingress-operator" \
      io.k8s.description="This is a component of OpenShift Container Platform and manages the lifecycle of ingress controller components." \
      maintainer="Dan Mace <dmace@redhat.com>"
