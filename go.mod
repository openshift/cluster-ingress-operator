module github.com/openshift/cluster-ingress-operator

go 1.13

require (
	github.com/Azure/azure-sdk-for-go v30.0.0+incompatible
	github.com/Azure/go-autorest/autorest v0.9.6
	github.com/Azure/go-autorest/autorest/adal v0.8.2
	github.com/Azure/go-autorest/autorest/to v0.2.0 // indirect
	github.com/Azure/go-autorest/autorest/validation v0.1.0 // indirect
	github.com/aws/aws-sdk-go v1.15.72
	github.com/davecgh/go-spew v1.1.1
	github.com/ghodss/yaml v1.0.0
	github.com/go-logr/logr v0.2.0
	github.com/go-logr/zapr v0.2.0
	github.com/google/go-cmp v0.4.0
	github.com/kevinburke/go-bindata v3.11.0+incompatible
	github.com/openshift/api v0.0.0-20200609191024-dca637550e8c
	github.com/openshift/library-go v0.0.0-20200324092245-db2a8546af81
	github.com/pkg/errors v0.9.1
	github.com/spf13/cobra v0.0.5
	go.uber.org/zap v1.10.0
	google.golang.org/api v0.15.0
	gopkg.in/fsnotify.v1 v1.4.7
	gopkg.in/yaml.v2 v2.3.0
	k8s.io/api v0.19.0-rc.2
	k8s.io/apimachinery v0.19.0-rc.2
	k8s.io/apiserver v0.19.0-rc.2
	k8s.io/client-go v0.19.0-rc.2
	sigs.k8s.io/controller-runtime v0.6.1
	sigs.k8s.io/controller-tools v0.3.0
)

// Remove when https://github.com/kubernetes-sigs/controller-runtime/issues/1033
// is fixed (controller-runtime milestone v0.7.x).
replace sigs.k8s.io/controller-runtime => github.com/zchee/sigs.k8s-controller-runtime v0.6.1-0.20200623114430-46812d3a0a50
