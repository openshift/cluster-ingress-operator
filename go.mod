module github.com/openshift/cluster-ingress-operator

go 1.13

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
	github.com/google/go-cmp v0.3.1
	github.com/kevinburke/go-bindata v3.11.0+incompatible

	github.com/openshift/api v0.0.0-20200522173408-17ada6e4245b
	github.com/openshift/library-go v0.0.0-20200324092245-db2a8546af81

	github.com/pkg/errors v0.8.1
	github.com/spf13/cobra v0.0.5
	go.uber.org/zap v1.10.0
	golang.org/x/xerrors v0.0.0-20191204190536-9bdfabe68543 // indirect
	google.golang.org/api v0.4.0
	gopkg.in/fsnotify.v1 v1.4.7
	gopkg.in/yaml.v2 v2.2.8

	k8s.io/api v0.18.3
	k8s.io/apimachinery v0.18.3
	k8s.io/apiserver v0.18.0
	k8s.io/client-go v0.18.0

	// Update when a tagged release includes https://github.com/kubernetes-sigs/controller-runtime/pull/836
	sigs.k8s.io/controller-runtime v0.5.1-0.20200330174416-a11a908d91e0
	sigs.k8s.io/controller-tools v0.2.2-0.20190919191502-76a25b63325a
)

// Remove when https://github.com/kubernetes-sigs/controller-tools/pull/424 merges.
replace sigs.k8s.io/controller-tools => github.com/munnerz/controller-tools v0.1.10-0.20200323145043-a2d268fbf03d
