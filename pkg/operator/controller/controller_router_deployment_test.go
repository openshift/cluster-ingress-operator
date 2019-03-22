package controller

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
)

func TestDesiredRouterDeployment(t *testing.T) {
	var one int32 = 1
	ci := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: operatorv1.IngressControllerSpec{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
			Replicas: &one,
			RouteSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"baz": "quux",
				},
			},
		},
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.PrivateStrategyType,
			},
		},
	}
	routerImage := "quay.io/openshift/router:latest"
	infraConfig := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			Platform: configv1.AWSPlatform,
		},
	}

	deployment, err := desiredRouterDeployment(ci, routerImage, infraConfig)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}

	namespaceSelector := ""
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "NAMESPACE_LABELS" {
			namespaceSelector = envVar.Value
			break
		}
	}
	if namespaceSelector == "" {
		t.Error("router Deployment has no namespace selector")
	} else if namespaceSelector != "foo=bar" {
		t.Errorf("router Deployment has unexpected namespace selectors: %v",
			namespaceSelector)
	}

	routeSelector := ""
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTE_LABELS" {
			routeSelector = envVar.Value
			break
		}
	}
	if routeSelector == "" {
		t.Error("router Deployment has no route selector")
	} else if routeSelector != "baz=quux" {
		t.Errorf("router Deployment has unexpected route selectors: %v",
			routeSelector)
	}

	if deployment.Spec.Replicas == nil {
		t.Error("router Deployment has nil replicas")
	}
	if *deployment.Spec.Replicas != 1 {
		t.Errorf("expected replicas to be 1, got %d", *deployment.Spec.Replicas)
	}

	if len(deployment.Spec.Template.Spec.NodeSelector) == 0 {
		t.Error("router Deployment has no default node selector")
	}

	proxyProtocolEnabled := false
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTER_USE_PROXY_PROTOCOL" {
			if v, err := strconv.ParseBool(envVar.Value); err == nil {
				proxyProtocolEnabled = v
			}
			break
		}
	}
	if proxyProtocolEnabled {
		t.Errorf("router Deployment unexpected proxy protocol")
	}

	canonicalHostname := ""
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTER_CANONICAL_HOSTNAME" {
			canonicalHostname = envVar.Value
			break
		}
	}
	if canonicalHostname != "" {
		t.Errorf("router Deployment has unexpected canonical hostname: %q", canonicalHostname)
	}

	if deployment.Spec.Template.Spec.Volumes[0].Secret == nil {
		t.Error("router Deployment has no secret volume")
	}

	defaultSecretName := fmt.Sprintf("router-certs-%s", ci.Name)
	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != defaultSecretName {
		t.Errorf("router Deployment expected volume with secret %s, got %s",
			defaultSecretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}

	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}

	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}

	ci.Status.Domain = "example.com"
	ci.Status.EndpointPublishingStrategy.Type = operatorv1.LoadBalancerServiceStrategyType
	deployment, err = desiredRouterDeployment(ci, routerImage, infraConfig)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}
	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}

	proxyProtocolEnabled = false
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTER_USE_PROXY_PROTOCOL" {
			if v, err := strconv.ParseBool(envVar.Value); err == nil {
				proxyProtocolEnabled = v
			}
			break
		}
	}
	if !proxyProtocolEnabled {
		t.Errorf("router Deployment expected proxy protocol")
	}

	canonicalHostname = ""
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTER_CANONICAL_HOSTNAME" {
			canonicalHostname = envVar.Value
			break
		}
	}
	if canonicalHostname == "" {
		t.Error("router Deployment has no canonical hostname")
	} else if canonicalHostname != ci.Status.Domain {
		t.Errorf("router Deployment has unexpected canonical hostname: %q, expected %q", canonicalHostname, ci.Status.Domain)
	}

	secretName := fmt.Sprintf("secret-%v", time.Now().UnixNano())
	ci.Spec.DefaultCertificate = &corev1.LocalObjectReference{
		Name: secretName,
	}
	ci.Spec.NodePlacement = &operatorv1.NodePlacement{
		NodeSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"xyzzy": "quux",
			},
		},
	}
	var expectedReplicas int32 = 3
	ci.Spec.Replicas = &expectedReplicas
	ci.Status.EndpointPublishingStrategy.Type = operatorv1.HostNetworkStrategyType
	deployment, err = desiredRouterDeployment(ci, routerImage, infraConfig)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	if len(deployment.Spec.Template.Spec.NodeSelector) != 1 ||
		deployment.Spec.Template.Spec.NodeSelector["xyzzy"] != "quux" {
		t.Errorf("router Deployment has unexpected node selector: %#v",
			deployment.Spec.Template.Spec.NodeSelector)
	}
	if deployment.Spec.Replicas == nil {
		t.Error("router Deployment has nil replicas")
	}
	if *deployment.Spec.Replicas != expectedReplicas {
		t.Errorf("expected replicas to be %d, got %d", expectedReplicas, *deployment.Spec.Replicas)
	}
	if e, a := routerImage, deployment.Spec.Template.Spec.Containers[0].Image; e != a {
		t.Errorf("expected router Deployment image %q, got %q", e, a)
	}

	if deployment.Spec.Template.Spec.HostNetwork != true {
		t.Error("expected host network to be true")
	}
	if deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host != "localhost" {
		t.Errorf("expected liveness probe host to be \"localhost\", got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host != "localhost" {
		t.Errorf("expected liveness probe host to be \"localhost\", got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}

	if deployment.Spec.Template.Spec.Volumes[0].Secret == nil {
		t.Error("router Deployment has no secret volume")
	}
	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != secretName {
		t.Errorf("expected router Deployment volume with secret %s, got %s",
			secretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}
}

func TestDeploymentConfigChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*appsv1.Deployment)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *appsv1.Deployment) {},
			expect:      false,
		},
		{
			description: "if .uid changes",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.UID = "2"
			},
			expect: false,
		},
		{
			description: "if .spec.template.spec.volumes is set to empty",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Volumes = []corev1.Volume{}
			},
			expect: true,
		},
		{
			description: "if .spec.template.spec.volumes is set to nil",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Volumes = nil
			},
			expect: true,
		},
		{
			description: "if .spec.template.spec.nodeSelector changes",
			mutate: func(deployment *appsv1.Deployment) {
				ns := map[string]string{"xyzzy": "quux"}
				deployment.Spec.Template.Spec.NodeSelector = ns
			},
			expect: true,
		},
		{
			description: "if ROUTER_CANONICAL_HOSTNAME changes",
			mutate: func(deployment *appsv1.Deployment) {
				envs := deployment.Spec.Template.Spec.Containers[0].Env
				for i, env := range envs {
					if env.Name == "ROUTER_CANONICAL_HOSTNAME" {
						envs[i].Value = "mutated.example.com"
					}
				}
				deployment.Spec.Template.Spec.Containers[0].Env = envs
			},
			expect: true,
		},
		{
			description: "if ROUTER_USE_PROXY_PROTOCOL changes",
			mutate: func(deployment *appsv1.Deployment) {
				envs := deployment.Spec.Template.Spec.Containers[0].Env
				for i, env := range envs {
					if env.Name == "ROUTER_USE_PROXY_PROTOCOL" {
						envs[i].Value = "true"
					}
				}
				deployment.Spec.Template.Spec.Containers[0].Env = envs
			},
			expect: true,
		},
		{
			description: "if NAMESPACE_LABELS is added",
			mutate: func(deployment *appsv1.Deployment) {
				envs := deployment.Spec.Template.Spec.Containers[0].Env
				env := corev1.EnvVar{
					Name:  "NAMESPACE_LABELS",
					Value: "x=y",
				}
				envs = append(envs, env)
				deployment.Spec.Template.Spec.Containers[0].Env = envs
			},
			expect: true,
		},
		{
			description: "if ROUTE_LABELS is deleted",
			mutate: func(deployment *appsv1.Deployment) {
				oldEnvs := deployment.Spec.Template.Spec.Containers[0].Env
				newEnvs := []corev1.EnvVar{}
				for _, env := range oldEnvs {
					if env.Name != "ROUTE_LABELS" {
						newEnvs = append(newEnvs, env)
					}
				}
				deployment.Spec.Template.Spec.Containers[0].Env = newEnvs
			},
			expect: true,
		},
		{
			description: "if the container image is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].Image = "openshift/origin-cluster-ingress-operator:latest"
			},
			expect: true,
		},
		{
			description: "if the volumes change ordering",
			mutate: func(deployment *appsv1.Deployment) {
				vols := deployment.Spec.Template.Spec.Volumes
				vols[1], vols[0] = vols[0], vols[1]
			},
			expect: false,
		},
	}

	for _, tc := range testCases {
		nineteen := int32(19)
		original := appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "router-original",
				Namespace: "openshift-ingress",
				UID:       "1",
			},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Volumes: []corev1.Volume{
							{
								Name: "default-certificate",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName:  "secrets-volume",
									},
								},
							},
							{
								Name: "metrics-certs",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName: "router-metrics-certs-default",
									},
								},
							},
						},
						Containers: []corev1.Container{
							{
								Env: []corev1.EnvVar{
									{
										Name:  "ROUTER_CANONICAL_HOSTNAME",
										Value: "example.com",
									},
									{
										Name:  "ROUTER_USE_PROXY_PROTOCOL",
										Value: "false",
									},
									{
										Name:  "ROUTE_LABELS",
										Value: "foo=bar",
									},
								},
								Image: "openshift/origin-cluster-ingress-operator:v4.0",
							},
						},
					},
				},
				Replicas: &nineteen,
			},
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := deploymentConfigChanged(&original, mutated); changed != tc.expect {
			t.Errorf("%s, expect deploymentConfigChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := deploymentConfigChanged(mutated, updated); changedAgain {
				t.Errorf("%s, deploymentConfigChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}
