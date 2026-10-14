package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

func TestIstioVersionDefaultMatchesDeployments(t *testing.T) {
	command := NewStartCommand()
	versionFlag := command.Flags().Lookup("istio-version")
	require.NotNil(t, versionFlag)
	require.Equal(t, "v1.31-latest", versionFlag.DefValue)
	for _, path := range []string{"../../manifests/02-deployment.yaml", "../../manifests/02-deployment-ibm-cloud-managed.yaml"} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var deployment appsv1.Deployment
			require.NoError(t, yaml.Unmarshal(data, &deployment))
			var versions []string
			for _, container := range deployment.Spec.Template.Spec.Containers {
				for _, variable := range container.Env {
					if variable.Name == "ISTIO_VERSION" {
						versions = append(versions, variable.Value)
					}
				}
			}
			assert.Equal(t, []string{versionFlag.DefValue}, versions)
		})
	}
}
