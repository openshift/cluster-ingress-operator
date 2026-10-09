package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/util/yaml"
)

const ingressCRDName = "ingresses.operator.openshift.io"

type manifestIdentity struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s MANIFEST_DIRECTORY\n", os.Args[0])
		os.Exit(2)
	}

	matches, err := findIngressCRDs(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := printMatches(os.Stdout, matches); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func printMatches(w io.Writer, matches []string) error {
	for _, match := range matches {
		if _, err := fmt.Fprintln(w, match); err != nil {
			return fmt.Errorf("write match: %w", err)
		}
	}
	return nil
}

func findIngressCRDs(root string) ([]string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
			return nil
		}

		manifestMatches, err := containsIngressCRD(path)
		if err != nil {
			return err
		}
		if manifestMatches {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan manifests: %w", err)
	}
	return matches, nil
}

func containsIngressCRD(path string) (bool, error) {
	manifest, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	defer manifest.Close()

	decoder := yaml.NewYAMLOrJSONDecoder(manifest, 4096)
	found := false
	for document := 1; ; document++ {
		var identity manifestIdentity
		if err := decoder.Decode(&identity); err != nil {
			if errors.Is(err, io.EOF) {
				return found, nil
			}
			return false, fmt.Errorf("parse %s document %d: %w", path, document, err)
		}
		if strings.HasPrefix(identity.APIVersion, "apiextensions.k8s.io/") &&
			identity.Kind == "CustomResourceDefinition" &&
			identity.Metadata.Name == ingressCRDName {
			found = true
		}
	}
}
