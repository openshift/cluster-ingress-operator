package h2spec

// This is a slightly modified version of https://github.com/summerwind/h2spec/blob/70ac2294010887f48b18e2d64f5cccd48421fad1/cmd/h2spec/h2spec.go

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/summerwind/h2spec"
	h2specconfig "github.com/summerwind/h2spec/config"
)

var (
	version string = "2.6.0"
	commit  string = "70ac2294010887f48b18e2d64f5cccd48421fad1"
)

func NewClientCommand() *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "h2spec [args...]",
		Short: "Conformance testing tool for HTTP/2 implementation",
		Long:  "Conformance testing tool for HTTP/2 implementation.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, args)
		},
	}

	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	flags := cmd.Flags()
	flags.StringP("host", "h", "127.0.0.1", "Target host")
	flags.IntP("port", "p", 0, "Target port")
	flags.StringP("path", "P", "/", "Target path")
	flags.IntP("timeout", "o", 2, "Time seconds to test timeout")
	flags.Int("max-header-length", 4000, "Maximum length of HTTP header")
	flags.StringP("junit-report", "j", "", "Path for JUnit test report")
	flags.BoolP("strict", "S", false, "Run all test cases including strict test cases")
	flags.Bool("dryrun", false, "Display only the title of test cases")
	flags.BoolP("tls", "t", false, "Connect over TLS")
	flags.StringP("ciphers", "c", "", "List of colon-separated TLS cipher names")
	flags.BoolP("insecure", "k", false, "Don't verify server's certificate")
	flags.BoolP("verbose", "v", false, "Output verbose log")
	flags.Bool("version", false, "Display version information and exit")
	flags.Bool("help", false, "Display this help and exit")

	return cmd
}

func run(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()

	v, err := flags.GetBool("version")
	if err != nil {
		return err
	}

	if v {
		fmt.Printf("Version: %s (%s)\n", version, commit)
		return nil
	}

	host, err := flags.GetString("host")
	if err != nil {
		return err
	}

	port, err := flags.GetInt("port")
	if err != nil {
		return err
	}

	path, err := flags.GetString("path")
	if err != nil {
		return err
	}

	timeout, err := flags.GetInt("timeout")
	if err != nil {
		return err
	}

	maxHeaderLen, err := flags.GetInt("max-header-length")
	if err != nil {
		return err
	}

	junitReport, err := flags.GetString("junit-report")
	if err != nil {
		return err
	}

	strict, err := flags.GetBool("strict")
	if err != nil {
		return err
	}

	dryRun, err := flags.GetBool("dryrun")
	if err != nil {
		return err
	}

	tls, err := flags.GetBool("tls")
	if err != nil {
		return err
	}

	ciphers, err := flags.GetString("ciphers")
	if err != nil {
		return err
	}

	insecure, err := flags.GetBool("insecure")
	if err != nil {
		return err
	}

	verbose, err := flags.GetBool("verbose")
	if err != nil {
		return err
	}

	if port == 0 {
		if tls {
			port = 443
		} else {
			port = 80
		}
	}

	c := &h2specconfig.Config{
		Host:         host,
		Port:         port,
		Path:         path,
		Timeout:      time.Duration(timeout) * time.Second,
		MaxHeaderLen: maxHeaderLen,
		JUnitReport:  junitReport,
		Strict:       strict,
		DryRun:       dryRun,
		TLS:          tls,
		Ciphers:      ciphers,
		Insecure:     insecure,
		Verbose:      verbose,
		Sections:     args,
	}

	success, err := h2spec.Run(c)
	if !success {
		os.Exit(1)
	}

	return err
}
