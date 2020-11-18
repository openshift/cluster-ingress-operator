package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/fsnotify.v1"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"
	statuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/status"

	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
)

const (
	// defaultTrustedCABundle is the fully qualified path of the trusted CA bundle
	// that is mounted from configmap openshift-ingress-operator/trusted-ca.
	defaultTrustedCABundle = "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"
)

type StartOptions struct {
	// When this file changes, the operator will shut down. This is useful for simple
	// reloading when things like a certificate changes.
	ShutdownFile string
	// MetricsListenAddr is the address on which to expose the metrics endpoint.
	MetricsListenAddr string
	// OperatorNamespace is the namespace the operator should watch for
	// ingresscontroller resources.
	OperatorNamespace string
	// IngressControllerImage is the pullspec of the ingress controller image to
	// be managed.
	IngressControllerImage string
	// CanaryImage is the pullspec of the canary tester server image to
	// be managed.
	CanaryImage string
	// ReleaseVersion is the cluster version which the operator will converge to.
	ReleaseVersion string
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

	cmd.Flags().StringVarP(&options.OperatorNamespace, "namespace", "n", manifests.DefaultOperatorNamespace, "namespace the operator is deployed to (required)")
	cmd.Flags().StringVarP(&options.IngressControllerImage, "image", "i", "", "image of the ingress controller the operator will manage (required)")
	cmd.Flags().StringVarP(&options.CanaryImage, "canary-image", "c", "", "image of the canary container that the operator will manage (optional)")
	cmd.Flags().StringVarP(&options.ReleaseVersion, "release-version", "", statuscontroller.UnknownVersionValue, "the release version the operator should converge to (required)")
	cmd.Flags().StringVarP(&options.MetricsListenAddr, "metrics-listen-addr", "", "127.0.0.1:60000", "metrics endpoint listen address (required)")
	cmd.Flags().StringVarP(&options.ShutdownFile, "shutdown-file", "s", defaultTrustedCABundle, "if provided, shut down the operator when this file changes")

	if err := cmd.MarkFlagRequired("namespace"); err != nil {
		panic(err)
	}
	if err := cmd.MarkFlagRequired("image"); err != nil {
		panic(err)
	}

	return cmd
}

func start(opts *StartOptions) error {
	metrics.DefaultBindAddress = opts.MetricsListenAddr

	kubeConfig, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kube config: %v", err)
	}

	log.Info("using operator namespace", "namespace", opts.OperatorNamespace)

	if opts.ReleaseVersion == statuscontroller.UnknownVersionValue {
		log.Info("Warning: no release version is specified", "release version", statuscontroller.UnknownVersionValue)
	}

	operatorConfig := operatorconfig.Config{
		OperatorReleaseVersion: opts.ReleaseVersion,
		Namespace:              opts.OperatorNamespace,
		IngressControllerImage: opts.IngressControllerImage,
		CanaryImage:            opts.CanaryImage,
	}

	// Set up the channels for the watcher and operator.
	stop := make(chan struct{})
	signal := signals.SetupSignalHandler()

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
			case <-signal:
				close(stop)
				return
			case _, ok := <-watcher.Events:
				if !ok {
					log.Info("file watch events channel closed")
					close(stop)
					return
				}
				latest, err := ioutil.ReadFile(opts.ShutdownFile)
				if err != nil {
					log.Error(err, "failed to read watched file", "filename", opts.ShutdownFile)
					close(stop)
					return
				}
				if !bytes.Equal(orig, latest) {
					log.Info("watched file changed, stopping operator", "filename", opts.ShutdownFile)
					close(stop)
					return
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					log.Info("file watch error channel closed")
					close(stop)
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
	return op.Start(stop)
}
