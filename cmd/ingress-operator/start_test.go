package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	imagev1 "github.com/openshift/api/image/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

func TestHAProxyManifestStartup(t *testing.T) {
	imageData, err := os.ReadFile(filepath.Join("..", "..", "manifests", "image-references"))
	if err != nil {
		t.Fatal(err)
	}
	var references imagev1.ImageStream
	if err := yaml.Unmarshal(imageData, &references); err != nil {
		t.Fatal(err)
	}
	images := make(map[string]string)
	for _, tag := range references.Spec.Tags {
		if tag.From != nil {
			images[tag.Name] = tag.From.Name
		}
	}
	for _, manifest := range []string{"02-deployment.yaml", "02-deployment-ibm-cloud-managed.yaml"} {
		t.Run(manifest, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "manifests", manifest))
			if err != nil {
				t.Fatal(err)
			}
			var deployment appsv1.Deployment
			if err := yaml.Unmarshal(data, &deployment); err != nil {
				t.Fatal(err)
			}
			container := deployment.Spec.Template.Spec.Containers[0]
			arguments := append([]string(nil), container.Command[2:]...)
			for _, variable := range container.Env {
				for index := range arguments {
					arguments[index] = strings.ReplaceAll(arguments[index], "$("+variable.Name+")", variable.Value)
				}
			}
			command := NewStartCommand()
			if err := command.Flags().Parse(arguments); err != nil {
				t.Fatal(err)
			}
			configured, err := command.Flags().GetStringToString("haproxy-image")
			if err != nil {
				t.Fatal(err)
			}
			for version, key := range map[operatorv1.HAProxyVersion]string{
				operatorv1.HAProxyVersion32: "haproxy-router-haproxy32",
				operatorv1.HAProxyVersion34: "haproxy-router-haproxy34",
			} {
				if images[key] == "" || configured[string(version)] != images[key] {
					t.Errorf("HAProxy %s startup image %q does not match image reference %q", version, configured[string(version)], images[key])
				}
			}
			if len(configured) != 2 {
				t.Errorf("expected two HAProxy image versions, got %v", configured)
			}
			defaultVersion, err := command.Flags().GetString("default-haproxy-version")
			if err != nil {
				t.Fatal(err)
			}
			if defaultVersion != string(operatorv1.HAProxyVersion32) {
				t.Errorf("default HAProxy version changed to %q", defaultVersion)
			}
		})
	}
}
