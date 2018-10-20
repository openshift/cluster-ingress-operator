package util

import (
	"fmt"

	"github.com/ghodss/yaml"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
	Metadata   InstallConfigMetadata `json:"metadata"`
	BaseDomain string                `json:"baseDomain"`
}

type InstallConfigMetadata struct {
	Name string `json:"name"`
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

// GetInstallConfig looks up the install config in the cluster.
func GetInstallConfig(client kubernetes.Interface) (*InstallConfig, error) {
	cm, err := client.CoreV1().ConfigMaps(ClusterConfigNamespace).Get(ClusterConfigName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("couldn't get clusterconfig %s/%s: %v", ClusterConfigNamespace, ClusterConfigName, err)
	}

	return UnmarshalInstallConfig(cm)
}
