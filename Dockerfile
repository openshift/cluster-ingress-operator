#############################################################################
#
# First build stage
#
# Here is where the binary compile runs. This will probably actually be a
# common go-builder image with the tools already installed, but for now
# we build the builder (from the same base image) before using it.
#
# In theory there could be multiple build stages to compile different artifacts
# that will end up in the final image. In practice, one will be the most common
# scenario.
#
FROM openshift/origin-base AS builder

# Install build tools and prepare environment.  In production builds, layers
# will be squashed after the build runs so there is no need to combine RUN
# statements, though Origin builds could still benefit from doing that.
#
ENV GOPATH /go
RUN mkdir $GOPATH
RUN yum install -y golang make

# Install the source in GOPATH on the image.
#
COPY . $GOPATH/src/github.com/openshift/cluster-ingress-operator

# Perform the binary build.
#
RUN cd $GOPATH/src/github.com/openshift/cluster-ingress-operator \
 && make build

# No need to delete anything; this stage of the build is discarded afterward.

#############################################################################
#
# Second (and final) build stage
#
# This just adds the binary from the builder stage into the desired base image.
#
# origin-base is a base image we never ship. It forms a base layer shared by all images
# and thus minimizes aggregate disk usage. In production builds automation will rewrite
# this as openshift3/ose-base
#
# It might seem like a good idea to build FROM scratch. However, this is not
# currently supported by OSBS and in any case makes containers harder to debug.
# Since the base image is shared between containers, it effectively uses no
# extra space or bandwidth.
#
FROM openshift/origin-base

# Copy the binary to a standard location where it will run.
#
COPY --from=builder /go/src/github.com/openshift/cluster-ingress-operator/cluster-ingress-operator /usr/bin/
ENTRYPOINT ["/usr/bin/cluster-ingress-operator"]

# This image doesn't need to run as root user.
USER 1001

# Apply labels as needed. ART build automation fills in others required for
# shipping, including component NVR (name-version-release) and image name. OSBS
# applies others at build time. So most required labels need not be in the source.
#
# io.k8s.display-name is required and is displayed in certain places in the
# console (someone correct this if that's no longer the case)
#
# io.k8s.description is equivalent to "description" and should be defined per
# image; otherwise the parent image's description is inherited which is
# confusing at best when examining images.
#
LABEL io.k8s.display-name="OpenShift cluster-ingress-operator" \
      io.k8s.description="This is a component of OpenShift Container Platform and manages the lifecycle of cluster ingress components." \
      maintainer="Dan Mace <dmace@redhat.com>"
