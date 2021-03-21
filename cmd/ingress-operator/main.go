package main

import (
	"os"

	"github.com/spf13/cobra"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
)

var log = logf.Logger.WithName("main")

func main() {
	var rootCmd = &cobra.Command{Use: "ingress-operator"}
	rootCmd.AddCommand(NewStartCommand())
	rootCmd.AddCommand(NewRenderCommand())
	rootCmd.AddCommand(NewServeHealthCheckCommand())

	if err := rootCmd.Execute(); err != nil {
		log.Error(err, "error")
		os.Exit(1)
	}
}
