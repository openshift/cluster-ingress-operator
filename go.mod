module github.com/openshift/cluster-ingress-operator

go 1.12

require (
	github.com/Azure/azure-sdk-for-go v30.0.0+incompatible
	github.com/Azure/go-autorest/autorest v0.9.0
	github.com/Azure/go-autorest/autorest/adal v0.5.0
	github.com/Azure/go-autorest/autorest/to v0.2.0 // indirect
	github.com/Azure/go-autorest/autorest/validation v0.1.0 // indirect
	github.com/aws/aws-sdk-go v1.15.72
	github.com/davecgh/go-spew v1.1.1
	github.com/ghodss/yaml v1.0.0
	github.com/go-logr/logr v0.1.0
	github.com/go-logr/zapr v0.1.1
	github.com/golang/groupcache v0.0.0-20190129154638-5b532d6fd5ef // indirect
	github.com/google/go-cmp v0.3.0
	github.com/imdario/mergo v0.3.7 // indirect
	github.com/kevinburke/go-bindata v3.11.0+incompatible
	github.com/openshift/api v0.0.0-20200117162508-e7ccdda6ba67
	github.com/openshift/library-go v0.0.0-20190927184318-c355e2019bb3
	github.com/pkg/errors v0.8.1
	github.com/spf13/cobra v0.0.5
	go.uber.org/zap v1.9.1
	golang.org/x/sync v0.0.0-20190911185100-cd5d95a43a6e // indirect
	golang.org/x/time v0.0.0-20190308202827-9d24e82272b4 // indirect
	golang.org/x/xerrors v0.0.0-20191204190536-9bdfabe68543 // indirect
	google.golang.org/api v0.4.0
	gopkg.in/fsnotify.v1 v1.4.7
	gopkg.in/yaml.v2 v2.2.4

	// kubernetes-1.16.0
	k8s.io/api v0.17.1
	k8s.io/apimachinery v0.17.1
	k8s.io/apiserver v0.0.0-20190918160949-bfa5e2e684ad
	k8s.io/client-go v0.0.0-20190918160344-1fbdaa4c8d90

	sigs.k8s.io/controller-runtime v0.3.1-0.20191011155846-b2bc3490f2e3
	sigs.k8s.io/controller-tools v0.2.2-0.20190919191502-76a25b63325a
)
