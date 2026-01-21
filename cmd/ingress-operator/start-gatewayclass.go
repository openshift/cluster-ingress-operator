package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"

	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	gatewayclasscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayclass"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

type StartGatewayClassOptions struct {
	// OperatorNamespace is the namespace the operator is deployed to.
	OperatorNamespace string
	// IstioVersion is the version of Istio to install.
	IstioVersion string
}

// NewStartGatewayClassCommand creates a command to run only the gatewayclass controller.
// This is useful for POC testing of the Helm-based Istio installation.
func NewStartGatewayClassCommand() *cobra.Command {
	var options StartGatewayClassOptions

	cmd := &cobra.Command{
		Use:   "start-gatewayclass",
		Short: "Start only the gatewayclass controller (POC testing)",
		Long: `start-gatewayclass launches only the gatewayclass controller in the foreground.
This is a minimal command for testing the Helm-based Istio installation POC.
It does not start any other controllers or require OpenShift-specific APIs.`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := startGatewayClass(&options); err != nil {
				log.Error(err, "error starting gatewayclass controller")
				os.Exit(1)
			}
		},
	}

	cmd.Flags().StringVarP(&options.OperatorNamespace, "namespace", "n", operatorcontroller.DefaultOperatorNamespace, "operator namespace")
	cmd.Flags().StringVarP(&options.IstioVersion, "istio-version", "", defaultIstioVersion, "version of Istio to install")

	return cmd
}

func startGatewayClass(opts *StartGatewayClassOptions) error {
	kubeConfig, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kube config: %w", err)
	}

	log.Info("starting gatewayclass controller (POC mode)", "namespace", opts.OperatorNamespace, "istio-version", opts.IstioVersion)

	// Set up signal handler
	ctx := signals.SetupSignalHandler()

	// Create a minimal manager
	scheme := operatorclient.GetScheme()
	mgr, err := manager.New(kubeConfig, manager.Options{
		Scheme: scheme,
		Cache: cache.Options{
			// Only watch resources in the operator and operand namespaces
			// plus cluster-scoped resources like GatewayClass
			DefaultNamespaces: map[string]cache.Config{
				opts.OperatorNamespace:                     {},
				operatorcontroller.DefaultOperandNamespace: {},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create manager: %w", err)
	}

	// Create the gatewayclass controller
	gatewayClassController, err := gatewayclasscontroller.NewUnmanaged(mgr, gatewayclasscontroller.Config{
		OperatorNamespace: opts.OperatorNamespace,
		OperandNamespace:  operatorcontroller.DefaultOperandNamespace,
		IstioVersion:      opts.IstioVersion,
	})
	if err != nil {
		return fmt.Errorf("failed to create gatewayclass controller: %w", err)
	}

	// Start the gatewayclass controller manually (since it's unmanaged)
	// We need to start the manager's cache first
	go func() {
		if err := mgr.Start(ctx); err != nil {
			log.Error(err, "manager exited with error")
			os.Exit(1)
		}
	}()

	// Wait for cache to sync before starting the controller
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		return fmt.Errorf("failed to sync cache")
	}

	log.Info("cache synced, starting gatewayclass controller")

	// Start the gatewayclass controller
	go func() {
		if err := gatewayClassController.Start(ctx); err != nil {
			log.Error(err, "gatewayclass controller exited with error")
			os.Exit(1)
		}
	}()

	log.Info("gatewayclass controller started successfully")

	// Wait for shutdown signal
	<-ctx.Done()
	log.Info("shutting down")
	return nil
}
