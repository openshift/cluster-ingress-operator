package util

import (
	corev1 "k8s.io/api/core/v1"
)

const (
	// emptyRoutingConfigYAML is a yaml snippet with an empty routingConfig.
	emptyRoutingConfigYAML = `routingConfig:`

	// invalidRoutingConfigYAML is a yaml snippet with an invalid routingConfig.
	invalidRoutingConfigYAML = `routingConfig: bad`

	// blankSubdomainYAML is a yaml snippet with a blank routingConfig subdomain.
	blankSubdomainYAML = `
routingConfig:
  subdomain:
`

	// emptySubdomainYAML is a yaml snippet with an empty routingConfig subdomain (spaces).
	emptySubdomainYAML = `
routingConfig:
  subdomain: "     "
`

	// goodSubdomainYAML is a yaml snippet with a valid routingConfig subdomain.
	goodSubdomainYAML = `
routingConfig:
  subdomain: "mycluster.unit.test"
`
)

// ConfigTest is a test case scenario used for testing the config code.
type ConfigTest struct {
	Name             string
	ConfigMap        *corev1.ConfigMap
	ErrorExpectation bool
}

// makeConfig creates a config [map] based on the yaml snippet passed to it.
func makeConfig(snippet string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		Data: map[string]string{
			"kco-config": snippet,
		},
	}
}

// ConfigTestDefaultConfigMap returns the default/best case scenario config map.
func ConfigTestDefaultConfigMap() *corev1.ConfigMap {
	return makeConfig(goodSubdomainYAML)
}

// ConfigTestScenarios returns the all the test scenarios.
func ConfigTestScenarios() []ConfigTest {
	return []ConfigTest{
		{
			Name:             "no config map",
			ErrorExpectation: true,
		},
		{
			Name:             "empty config map",
			ConfigMap:        &corev1.ConfigMap{},
			ErrorExpectation: true,
		},
		{
			Name:             "missing kco-config map",
			ConfigMap:        &corev1.ConfigMap{Data: map[string]string{"install-config": ""}},
			ErrorExpectation: true,
		},
		{
			Name:             "empty kco-config map",
			ConfigMap:        makeConfig(""),
			ErrorExpectation: true,
		},
		{
			Name:             "empty kco-config map routingConfig",
			ConfigMap:        makeConfig(emptyRoutingConfigYAML),
			ErrorExpectation: true,
		},
		{
			Name:             "invalid kco-config map routingConfig",
			ConfigMap:        makeConfig(invalidRoutingConfigYAML),
			ErrorExpectation: true,
		},
		{
			Name:             "empty kco-config map routingConfig subdomain",
			ConfigMap:        makeConfig(blankSubdomainYAML),
			ErrorExpectation: true,
		},
		{
			Name:             "empty kco-config map routingConfig subdomain with spaces",
			ConfigMap:        makeConfig(emptySubdomainYAML),
			ErrorExpectation: true,
		},
		{
			Name:             "ok kco-config map routingConfig subdomain",
			ConfigMap:        ConfigTestDefaultConfigMap(),
			ErrorExpectation: false,
		},
	}
}
