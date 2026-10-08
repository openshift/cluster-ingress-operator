package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestPrintMatchesPropagatesErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	if err := printMatches(errorWriter{err: writeErr}, []string{"manifest.yaml"}); !errors.Is(err, writeErr) {
		t.Fatalf("printMatches error = %v, want %v", err, writeErr)
	}
}

func TestFindIngressCRDs(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		matches  bool
	}{
		{
			name: "block YAML",
			manifest: `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: ingresses.operator.openshift.io
`,
			matches: true,
		},
		{
			name:     "flow YAML",
			manifest: `{apiVersion: apiextensions.k8s.io/v1, kind: CustomResourceDefinition, metadata: {name: ingresses.operator.openshift.io}}`,
			matches:  true,
		},
		{
			name: "later YAML document",
			manifest: `apiVersion: v1
kind: ConfigMap
metadata:
  name: unrelated
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: ingresses.operator.openshift.io}
`,
			matches: true,
		},
		{
			name: "nested name",
			manifest: `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: another.example.io
spec:
  names:
    name: ingresses.operator.openshift.io
`,
		},
		{
			name: "suffixed name",
			manifest: `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: ingresses.operator.openshift.io#suffix
`,
		},
		{
			name: "wrong API group",
			manifest: `apiVersion: v1
kind: CustomResourceDefinition
metadata: {name: ingresses.operator.openshift.io}
`,
		},
		{
			name: "wrong kind",
			manifest: `apiVersion: apiextensions.k8s.io/v1
kind: ConfigMap
metadata: {name: ingresses.operator.openshift.io}
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "fixture.yaml")
			if err := os.WriteFile(path, []byte(test.manifest), 0600); err != nil {
				t.Fatal(err)
			}

			matches, err := findIngressCRDs(root)
			if err != nil {
				t.Fatalf("findIngressCRDs returned an error: %v", err)
			}
			if got := len(matches) == 1; got != test.matches {
				t.Errorf("match = %v, want %v; matches: %v", got, test.matches, matches)
			}
		})
	}
}

func TestFindIngressCRDsPropagatesErrors(t *testing.T) {
	t.Run("parse error", func(t *testing.T) {
		root := t.TempDir()
		manifest := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: ingresses.operator.openshift.io}
---
metadata: [
`
		if err := os.WriteFile(filepath.Join(root, "invalid.yaml"), []byte(manifest), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := findIngressCRDs(root); err == nil {
			t.Fatal("findIngressCRDs unexpectedly succeeded")
		}
	})

	t.Run("read error", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken.yaml")); err != nil {
			t.Fatal(err)
		}
		if _, err := findIngressCRDs(root); err == nil {
			t.Fatal("findIngressCRDs unexpectedly succeeded")
		}
	})
}
