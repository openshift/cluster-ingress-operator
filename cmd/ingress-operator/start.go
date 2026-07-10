package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/fsnotify.v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator"

	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	canarycontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/canary"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	routemetricscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/route-metrics"
	statuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/status"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	operatorv1 "github.com/openshift/api/operator/v1"
	unidlingapi "github.com/openshift/api/unidling/v1alpha1"
)

const (
	// defaultTrustedCABundle is the fully qualified path of the trusted CA bundle
	// that is mounted from configmap openshift-ingress-operator/trusted-ca.
	defaultTrustedCABundle           = "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"
	defaultGatewayAPIOperatorCatalog = "redhat-operators"
	defaultGatewayAPIOperatorChannel = "stable"
	defaultGatewayAPIOperatorVersion = "servicemeshoperator3.v3.4.0"
	defaultIstioVersion              = "v1.28-latest"
)

type StartOptions struct {
	// When this file changes, the operator will shut down. This is useful for simple
	// reloading when things like a certificate changes.
	ShutdownFile string
	// MetricsBindAddr is the address on which to expose the metrics endpoint.
	MetricsBindAddr string
	// MetricsCertDir is the directory containing tls.crt and tls.key
	// for the metrics server. When set, the metrics server uses TLS
	// with the cluster-wide TLS security profile and requires
	// authentication via TokenReview/SubjectAccessReview.
	MetricsCertDir string
	// OperatorNamespace is the namespace the operator should watch for
	// ingresscontroller resources.
	OperatorNamespace string
	// IngressControllerImage is the pullspec of the ingress controller image to
	// be managed.
	IngressControllerImage string
	// HAProxyImages is the pullspec of the HAProxy images to be managed, as a hashmap of `<version>:<pullspec>`.
	HAProxyImages map[string]string
	// DefaultHAProxyVersion is the default HAProxy version.
	DefaultHAProxyVersion string
	// CanaryImage is the pullspec of the ingress operator image
	CanaryImage string
	// ReleaseVersion is the cluster version which the operator will converge to.
	ReleaseVersion string
	// GatewayAPIOperatorCatalog is the catalog source to use to install the Gateway API implementation.
	GatewayAPIOperatorCatalog string
	// GatewayAPIOperatorChannel is the release channel of the Gateway API implementation to install.
	GatewayAPIOperatorChannel string
	// GatewayAPIOperatorVersion is the name and release of the Gateway API implementation to install.
	GatewayAPIOperatorVersion string
	// IstioVersion is the version Istio to install.
	IstioVersion string
}

func NewStartCommand() *cobra.Command {
	var options StartOptions

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the operator",
		Long:  `starts launches the operator in the foreground.`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := start(&options); err != nil {
				log.Error(err, "error starting")
				os.Exit(1)
			}
		},
	}

	cmd.Flags().StringVarP(&options.OperatorNamespace, "namespace", "n", operatorcontroller.DefaultOperatorNamespace, "namespace the operator is deployed to (required)")
	cmd.Flags().StringVarP(&options.IngressControllerImage, "image", "i", "", "image of the ingress controller the operator will manage (required)")
	cmd.Flags().StringToStringVarP(&options.HAProxyImages, "haproxy-image", "", nil, "HAProxy images as version=pullspec (optional)")
	cmd.Flags().StringVarP(&options.DefaultHAProxyVersion, "default-haproxy-version", "", "", "defines the default HAProxy version, required if --haproxy-image is also provided (optional)")
	cmd.Flags().StringVarP(&options.CanaryImage, "canary-image", "c", "", "image of the canary container that the operator will manage (optional)")
	cmd.Flags().StringVarP(&options.ReleaseVersion, "release-version", "", statuscontroller.UnknownVersionValue, "the release version the operator should converge to (required)")
	cmd.Flags().StringVarP(&options.MetricsBindAddr, "metrics-bind-addr", "", "127.0.0.1:60000", "metrics endpoint bind address")
	// Keep --metrics-listen-addr as a deprecated alias so that existing
	// deployments (e.g. the HyperShift control-plane-operator) that still
	// pass the old flag name continue to work.
	cmd.Flags().StringVar(&options.MetricsBindAddr, "metrics-listen-addr", "127.0.0.1:60000", "deprecated: use --metrics-bind-addr")
	_ = cmd.Flags().MarkDeprecated("metrics-listen-addr", "use --metrics-bind-addr instead")
	cmd.Flags().StringVarP(&options.MetricsCertDir, "metrics-cert-dir", "", "", "directory containing tls.crt and tls.key for the metrics endpoint")
	cmd.Flags().StringVarP(&options.ShutdownFile, "shutdown-file", "s", defaultTrustedCABundle, "if provided, shut down the operator when this file changes")
	cmd.Flags().StringVarP(&options.GatewayAPIOperatorCatalog, "gateway-api-operator-catalog", "", defaultGatewayAPIOperatorCatalog, "catalog source for the Gateway API implementation to install")
	cmd.Flags().StringVarP(&options.GatewayAPIOperatorChannel, "gateway-api-operator-channel", "", defaultGatewayAPIOperatorChannel, "release channel of the Gateway API implementation to install")
	cmd.Flags().StringVarP(&options.GatewayAPIOperatorVersion, "gateway-api-operator-version", "", defaultGatewayAPIOperatorVersion, "name and release of the Gateway API implementation to install")
	cmd.Flags().StringVarP(&options.IstioVersion, "istio-version", "", defaultIstioVersion, "version Istio to install")

	if err := cmd.MarkFlagRequired("namespace"); err != nil {
		panic(err)
	}
	if err := cmd.MarkFlagRequired("image"); err != nil {
		panic(err)
	}

	return cmd
}

func start(opts *StartOptions) error {

	kubeConfig, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kube config: %v", err)
	}

	log.Info("using operator namespace", "namespace", opts.OperatorNamespace)

	if opts.ReleaseVersion == statuscontroller.UnknownVersionValue {
		log.Info("Warning: no release version is specified", "release version", statuscontroller.UnknownVersionValue)
	}

	// verify that all idled services have the correct idle annotations
	// mirrored over from the corresponding endpoints resources.
	// This is to ensure that applications idled with an older version of oc
	// (and thus do not have the idle annotations on the service) are still
	// safely un-idleable afer an upgrade that affects `oc idle` functionality.
	// use a single-use client here separate from the client used by the operator.
	cl, err := client.New(kubeConfig, client.Options{})
	if err != nil {
		return fmt.Errorf("failed to create client from kube config: %v", kubeConfig)
	}
	if err := ensureServicesHaveIdleAnnotation(cl); err != nil {
		log.Error(err, "failed to verify idling endpoints between endpoints and services")
	}

	// Set up the channels for the watcher, operator, and metrics using
	// the context provided from the controller runtime.
	signal, cancel := context.WithCancel(signals.SetupSignalHandler())
	defer cancel()

	// Convert the --haproxy-image input into the haproxyImages hashmap.
	// Controller uses router image if not provided, which is the pre 5.0/4.23 behavior.
	haproxyImages := make(map[operatorv1.HAProxyVersion]string)
	for version, image := range opts.HAProxyImages {
		if version == "" || image == "" {
			return fmt.Errorf("invalid HAProxy version=pullspec: '%s=%s'", version, image)
		}
		haproxyImages[operatorv1.HAProxyVersion(version)] = image
	}

	// Validates the default HAProxy version - it must be provided if HAProxy image
	// is provided, and must match one of the provided image versions.
	defaultHAProxyVersion := operatorv1.HAProxyVersion(opts.DefaultHAProxyVersion)
	if len(haproxyImages) > 0 {
		if defaultHAProxyVersion == "" {
			return fmt.Errorf("--default-haproxy-version is required when --haproxy-image is provided")
		}
		if _, found := haproxyImages[defaultHAProxyVersion]; !found {
			return fmt.Errorf("HAProxy image does not provide the default version %q", defaultHAProxyVersion)
		}
	}

	// Build the TLS options for the metrics server from the
	// cluster-wide TLS security profile when a cert directory is provided.
	var metricsTLSOpts []func(*tls.Config)
	if opts.MetricsCertDir != "" {
		apiConfig := &configv1.APIServer{}
		if err := cl.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, apiConfig); err != nil {
			log.Info("failed to get APIServer 'cluster' for metrics TLS; falling back to intermediate profile", "error", err)
		}
		tlsProfileSpec := operatorcontroller.TLSProfileSpecForSecurityProfile(apiConfig.Spec.TLSSecurityProfile)
		tlsCfg, err := operatorcontroller.TLSConfigFromProfile(tlsProfileSpec)
		if err != nil {
			return fmt.Errorf("failed to build TLS config for metrics: %v", err)
		}
		metricsTLSOpts = []func(*tls.Config){
			func(c *tls.Config) {
				c.CipherSuites = tlsCfg.CipherSuites
				c.MinVersion = tlsCfg.MinVersion
				c.CurvePreferences = tlsCfg.CurvePreferences
			},
		}
	}

	operatorConfig := operatorconfig.Config{
		OperatorReleaseVersion:    opts.ReleaseVersion,
		Namespace:                 opts.OperatorNamespace,
		IngressControllerImage:    opts.IngressControllerImage,
		HAProxyImages:             haproxyImages,
		DefaultHAProxyVersion:     defaultHAProxyVersion,
		CanaryImage:               opts.CanaryImage,
		GatewayAPIOperatorCatalog: opts.GatewayAPIOperatorCatalog,
		GatewayAPIOperatorChannel: opts.GatewayAPIOperatorChannel,
		GatewayAPIOperatorVersion: opts.GatewayAPIOperatorVersion,
		IstioVersion:              opts.IstioVersion,
		MetricsBindAddress:        opts.MetricsBindAddr,
		MetricsCertDir:            opts.MetricsCertDir,
		MetricsTLSOpts:            metricsTLSOpts,
	}

	log.Info("registering Prometheus metrics for canary_controller")
	if err := canarycontroller.RegisterMetrics(); err != nil {
		log.Error(err, "unable to register metrics for canary_controller")
	}
	log.Info("registering Prometheus metrics for ingress_controller")
	if err := ingresscontroller.RegisterMetrics(); err != nil {
		log.Error(err, "unable to register metrics for ingress_controller")
	}
	log.Info("registering Prometheus metrics for route_metrics_controller")
	if err := routemetricscontroller.RegisterMetrics(); err != nil {
		log.Error(err, "unable to register metrics for route_metrics_controller")
	}
	log.Info("registering Prometheus metrics for status_controller")
	if err := statuscontroller.RegisterMetrics(); err != nil {
		log.Error(err, "unable to register metrics for status_controller")
	}

	// Set up and start the file watcher.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %v", err)
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			log.V(1).Info("warning: watcher close returned an error: %v", err)
		}
	}()

	var orig []byte
	if len(opts.ShutdownFile) > 0 {
		if err := watcher.Add(opts.ShutdownFile); err != nil {
			return fmt.Errorf("failed to add file %q to watcher: %v", opts.ShutdownFile, err)
		}
		log.Info("watching file", "filename", opts.ShutdownFile)
		orig, err = ioutil.ReadFile(opts.ShutdownFile)
		if err != nil {
			return fmt.Errorf("failed to read watcher file %q: %v", opts.ShutdownFile, err)
		}
	}
	go func() {
		for {
			select {
			case <-signal.Done():
				return
			case _, ok := <-watcher.Events:
				if !ok {
					log.Info("file watch events channel closed")
					cancel()
					return
				}
				latest, err := ioutil.ReadFile(opts.ShutdownFile)
				if err != nil {
					log.Error(err, "failed to read watched file", "filename", opts.ShutdownFile)
					cancel()
					return
				}
				if !bytes.Equal(orig, latest) {
					log.Info("watched file changed, stopping operator", "filename", opts.ShutdownFile)
					cancel()
					return
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					log.Info("file watch error channel closed")
					cancel()
					return
				}
				log.Error(err, "file watch error")
			}
		}
	}()

	// Set up and start the operator.
	op, err := operator.New(operatorConfig, kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create operator: %v", err)
	}
	return op.Start(signal)
}

func ensureServicesHaveIdleAnnotation(cl client.Client) error {
	endpointsList := &corev1.EndpointsList{}
	err := cl.List(context.TODO(), endpointsList, &client.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list endpoints in all namespaces: %v", err)
	}

	for _, endpoints := range endpointsList.Items {
		idledAt, haveIdledAt := endpoints.Annotations[unidlingapi.IdledAtAnnotation]
		unidleTarget, haveUnidleTarget := endpoints.Annotations[unidlingapi.UnidleTargetAnnotation]
		// If the endpoints don't have the idle annotations, continue since we aren't idled.
		if !haveIdledAt || !haveUnidleTarget {
			continue
		}
		service := &corev1.Service{}
		serviceName := types.NamespacedName{
			Name:      endpoints.Name,
			Namespace: endpoints.Namespace,
		}
		if err := cl.Get(context.TODO(), serviceName, service); err != nil {
			log.Error(err, "failed to get service for endpoints", "namespace", service.Namespace, "name", service.Name)
			continue
		}

		_, haveIdledAt = service.Annotations[unidlingapi.IdledAtAnnotation]
		_, haveUnidleTarget = service.Annotations[unidlingapi.UnidleTargetAnnotation]
		// If the service already has the correct annotations, continue.
		if haveIdledAt && haveUnidleTarget {
			continue
		}

		annotations := service.Annotations
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[unidlingapi.IdledAtAnnotation] = idledAt
		annotations[unidlingapi.UnidleTargetAnnotation] = unidleTarget
		updated := service.DeepCopy()
		updated.Annotations = annotations

		if err := cl.Update(context.TODO(), updated); err != nil {
			log.Error(err, "failed to update service to have endpoint idling annotations", "namespace", updated.Namespace, "name", updated.Name)
			continue
		}

		log.Info("added idle annotations from endpoint to service", "namespace", updated.Namespace, "name", updated.Name)
	}

	return nil
}
