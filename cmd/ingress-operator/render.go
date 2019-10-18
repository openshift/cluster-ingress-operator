package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
)

func NewRenderCommand() *cobra.Command {
	var options struct {
		OutputDir string
		Prefix    string
	}

	var command = &cobra.Command{
		Use:   "render",
		Short: "Render base manifests",
		Long:  `render emits the base manifest files necessary to support the creation of an ingresscontroller resource.`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := render(options.OutputDir, options.Prefix); err != nil {
				log.Error(err, "error rendering")
				os.Exit(1)
			}
		},
	}

	command.Flags().StringVarP(&options.OutputDir, "output-dir", "o", "", "manifest output directory.")
	command.Flags().StringVarP(&options.Prefix, "prefix", "p", "", "optional prefix for rendered filenames.")
	if err := command.MarkFlagRequired("output-dir"); err != nil {
		panic(err)
	}

	return command
}

func render(dir string, prefix string) error {
	files := []string{
		manifests.CustomResourceDefinitionManifest,
		manifests.NamespaceManifest,
	}

	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create output directory %q: %v", dir, err)
	}

	for _, file := range files {
		outputFile := filepath.Join(dir, prefix+filepath.Base(file))
		if err := ioutil.WriteFile(outputFile, manifests.MustAsset(file), 0640); err != nil {
			return fmt.Errorf("failed to write %q: %v", outputFile, err)
		}
		fmt.Printf("wrote %s\n", outputFile)
	}
	return nil
}
