package util

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

const (
	validInstallConfig = `
metadata:
  name: user
baseDomain: clusters.openshift.com
`
)

func TestUnmarshalInstallConfig(t *testing.T) {
	cm := &corev1.ConfigMap{}
	cm.Data = map[string]string{"install-config": validInstallConfig}

	_, err := UnmarshalInstallConfig(cm)
	if err != nil {
		t.Fatal(err)
	}
}
