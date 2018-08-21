package stub

import (
	"context"

	"github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	"github.com/operator-framework/operator-sdk/pkg/sdk"

	"github.com/sirupsen/logrus"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func NewHandler() sdk.Handler {
	return &Handler{}
}

type Handler struct {
	// Fill me
}

func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	if event.Deleted {
		return nil
	}
	switch o := event.Object.(type) {
	case *v1alpha1.ClusterIngress:
		err := sdk.Create(newRouterDaemonSet(o))
		if err != nil && !errors.IsAlreadyExists(err) {
			logrus.Errorf("Failed to create daemonset: %v", err)
			return err
		}
		err = sdk.Create(newRouterService(o))
		if err != nil && !errors.IsAlreadyExists(err) {
			logrus.Errorf("Failed to create service: %v", err)
			return err
		}
	}
	return nil
}

func newRouterDaemonSet(cr *v1alpha1.ClusterIngress) *appsv1.DaemonSet {
	name := "router-" + cr.Name
	return &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cr.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cr, schema.GroupVersionKind{
					Group:   v1alpha1.SchemeGroupVersion.Group,
					Version: v1alpha1.SchemeGroupVersion.Version,
					Kind:    "ClusterIngress",
				}),
			},
			Labels: map[string]string{
				"app": "router",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": name,,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "router",
					NodeSelector: map[string]string{
						"node-role.kubernetes.io/infra": "true",
					},
					Containers: []corev1.Container{
						{
							Name:  "router",
							Image: "docker.io/openshift/origin-haproxy-router:v3.11.0",
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: 80,
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "https",
									ContainerPort: 443,
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "stats",
									ContainerPort: 1936,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env: []corev1.EnvVar{
								{Name: "STATS_PORT", Value: "1936"},
							},
							LivenessProbe: &corev1.Probe{
								InitialDelaySeconds: 10,
								Handler: corev1.Handler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.IntOrString{
											Type:   intstr.Int,
											IntVal: int32(1936),
										},
									},
								},
							},
							ReadinessProbe: &corev1.Probe{
								InitialDelaySeconds: 10,
								Handler: corev1.Handler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.IntOrString{
											Type:   intstr.Int,
											IntVal: int32(1936),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func newRouterService(cr *v1alpha1.ClusterIngress) *corev1.Service {
	name := "router-" + cr.Name
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cr.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cr, schema.GroupVersionKind{
					Group:   v1alpha1.SchemeGroupVersion.Group,
					Version: v1alpha1.SchemeGroupVersion.Version,
					Kind:    "ClusterIngress",
				}),
			},
			Labels: map[string]string{
				"app": "router",
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Selector: map[string]string{
				"app": name,
			},
			// This also has the effect of marking LB pool targets as unhealthy when
			// no router pods are present on a node behind the service.
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyTypeLocal,
			Ports: []corev1.ServicePort{
				{
					Name:     "http",
					Protocol: corev1.ProtocolTCP,
					Port:     80,
					TargetPort: intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "http",
					},
				},
			},
		},
	}
}
