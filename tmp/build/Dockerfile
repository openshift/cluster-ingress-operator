FROM alpine:3.6

RUN adduser -D cluster-ingress-operator
USER cluster-ingress-operator

ADD tmp/_output/bin/cluster-ingress-operator /usr/local/bin/cluster-ingress-operator
