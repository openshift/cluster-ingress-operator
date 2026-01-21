package main

import (
	"os"

	"github.com/spf13/cobra"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	grpctestserver "github.com/openshift/cluster-ingress-operator/test/grpc"
	h2specclient "github.com/openshift/cluster-ingress-operator/test/h2spec"
	httphealthcheck "github.com/openshift/cluster-ingress-operator/test/http"
	http2testserver "github.com/openshift/cluster-ingress-operator/test/http2"
)

var log = logf.Logger.WithName("main")

func main() {
	var rootCmd = &cobra.Command{Use: "ingress-operator"}
	rootCmd.AddCommand(NewStartCommand())
	rootCmd.AddCommand(NewStartGatewayClassCommand())
	rootCmd.AddCommand(NewRenderCommand())
	rootCmd.AddCommand(httphealthcheck.NewServeHealthCheckCommand())
	rootCmd.AddCommand(&cobra.Command{
		Use:   "serve-grpc-test-server",
		Short: "serve gRPC interoperability test server",
		Long:  `serve-grpc-test-server runs a gRPC interoperability test server.`,
		Run: func(cmd *cobra.Command, args []string) {
			grpctestserver.Serve()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "serve-http2-test-server",
		Short: "serve HTTP/2 test server",
		Long:  "serve-http2-test-server runs a HTTP/2 test server.",
		Run: func(cmd *cobra.Command, args []string) {
			http2testserver.Serve()
		},
	})
	rootCmd.AddCommand(h2specclient.NewClientCommand())
	rootCmd.AddCommand(httphealthcheck.NewServeDelayConnectCommand())

	if err := rootCmd.Execute(); err != nil {
		log.Error(err, "error")
		os.Exit(1)
	}
}
