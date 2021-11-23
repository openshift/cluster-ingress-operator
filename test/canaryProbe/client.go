package canaryProbe

import (
	"context"
	"fmt"
	"log"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/canary"
	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

var kclient client.Client

func NewClientCommand() *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "send-canary-probe [--report-stats=true]",
		Short: "Operational testing tool for canary healthcheck debugging",
		Long:  "Operational testing tool for canary healthcheck debugging.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, args)
		},
	}

	cmd.SilenceErrors = true

	flags := cmd.Flags()
	flags.Bool("report-stats", false, "Use the httpstats wrapper")
	flags.Bool("help", false, "Display this help and exit")

	return cmd
}

func run(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()

	getStats, err := flags.GetBool("report-stats")
	if err != nil {
		return err
	}

	kubeConfig, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kube config: %s\n", err)
	}
	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kube client: %s\n", err)
	}
	kclient = kubeClient

	// Get the current canary route
	haveRoute, route, err := currentCanaryRoute()
	if err != nil {
		log.Fatalf("failed to get current canary route for canary check: %v", err)
		return err
	} else if !haveRoute {
		return fmt.Errorf("error performing canary route check: route does not exist")
	}

	err = canary.ProbeRouteEndpoint(route, getStats, true)
	if err != nil {
		return fmt.Errorf("error performing canary route check: %v", err)
	}

	return err
}

// currentCanaryRoute gets the current canary route resource
func currentCanaryRoute() (bool, *routev1.Route, error) {
	canaryRoute := &routev1.Route{}
	name := controller.CanaryRouteName()
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, canaryRoute); err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			log.Printf("failed to get canary route %s: %v", name, err)
			return false, err
		}
		return true, nil
	})

	if err != nil {
		return false, nil, fmt.Errorf("failed to get canary route %s: %v", name, err)
	}
	return true, canaryRoute, nil
}
