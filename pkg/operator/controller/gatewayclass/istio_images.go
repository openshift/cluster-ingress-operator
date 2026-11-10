// Code generated manually. DO NOT EDIT.
// Overrides vendored upstream images with a staging registry reference for v1.31.1 proxy.
// Delete this file to use the vendored images from the sail library instead.

package gatewayclass

import "github.com/istio-ecosystem/sail-operator/pkg/config"

func init() {
	if images, ok := config.Config.ImageDigests["v1.31.1"]; ok {
		images.ProxyImage = "quay.io/redhat-user-workloads/service-mesh-tenant/ossm-3-5-proxy@sha256:aecacb9ee9bcc0d174f7647157a62b3f4f86897292d47e9a3ca51bd7bbd40578"
		config.Config.ImageDigests["v1.31.1"] = images
	}
}
