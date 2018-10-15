package util

import (
	"fmt"
	"strings"

	"github.com/ghodss/yaml"
	corev1 "k8s.io/api/core/v1"
)

// IngressDomain returns the domain used in the default ClusterIngress instance.
func IngressDomain(cm *corev1.ConfigMap) (string, error) {
	if cm == nil {
		return "", fmt.Errorf("invalid installer configmap")
	}

	kco, ok := cm.Data["kco-config"]
	if !ok {
		return "", fmt.Errorf("missing kco-config in configmap")
	}

	kcoConfigMap := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(kco), &kcoConfigMap); err != nil {
		return "", fmt.Errorf("kco-config unmarshall error: %v", err)
	}

	routingConfig, ok := kcoConfigMap["routingConfig"]
	if !ok {
		return "", fmt.Errorf("missing routingConfig in kco-config")
	}
	routingConfigMap, ok := routingConfig.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid routingConfig in kco-config")
	}
	subdomain, ok := routingConfigMap["subdomain"]
	if !ok {
		return "", fmt.Errorf("missing subdomain in routingConfig")
	}
	routingConfigSubdomain, ok := subdomain.(string)
	if !ok {
		return "", fmt.Errorf("invalid subdomain in routingConfig")
	}
	if len(strings.TrimSpace(routingConfigSubdomain)) == 0 {
		return "", fmt.Errorf("empty subdomain in routingConfig")
	}

	return fmt.Sprintf("apps.%s", routingConfigSubdomain), nil
}
