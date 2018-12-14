package util

import (
	"fmt"

	"github.com/ghodss/yaml"

	corev1 "k8s.io/api/core/v1"
)

const (
	// ClusterConfigNamespace is the namespace containing the cluster config.
	ClusterConfigNamespace = "kube-system"
	// ClusterConfigName is the name of the cluster config configmap.
	ClusterConfigName = "cluster-config-v1"
	// InstallConfigKey is the key in the cluster config configmap containing a
	// serialized InstallConfig.
	InstallConfigKey = "install-config"
)

type InstallConfig struct {
	BaseDomain string                `json:"baseDomain"`
	Platform   InstallConfigPlatform `json:"platform"`
}

type InstallConfigPlatform struct {
	AWS *InstallConfigPlatformAWS `json:"aws"`
}

type InstallConfigPlatformAWS struct {
	Region string `json:"region"`
}

// UnmarshalInstallConfig builds an install config from the cluster config.
func UnmarshalInstallConfig(clusterConfig *corev1.ConfigMap) (*InstallConfig, error) {
	icJson, ok := clusterConfig.Data[InstallConfigKey]
	if !ok {
		return nil, fmt.Errorf("missing %q in configmap", InstallConfigKey)
	}
	var ic InstallConfig
	if err := yaml.Unmarshal([]byte(icJson), &ic); err != nil {
		return nil, fmt.Errorf("invalid InstallConfig: %v\njson:\n%s", err, icJson)
	}
	return &ic, nil
}
