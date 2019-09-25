module github.com/openshift/cluster-ingress-operator

go 1.12

require (
	github.com/Azure/azure-sdk-for-go v30.0.0+incompatible
	github.com/Azure/go-autorest/autorest v0.9.0
	github.com/Azure/go-autorest/autorest/adal v0.5.0
	github.com/Azure/go-autorest/autorest/to v0.2.0 // indirect
	github.com/Azure/go-autorest/autorest/validation v0.1.0 // indirect
	github.com/aws/aws-sdk-go v1.15.72
	github.com/ghodss/yaml v1.0.0
	github.com/go-logr/logr v0.1.0
	github.com/go-logr/zapr v0.1.1
	github.com/golang/groupcache v0.0.0-20190129154638-5b532d6fd5ef // indirect
	github.com/google/go-cmp v0.3.0
	github.com/imdario/mergo v0.3.7 // indirect
	github.com/kevinburke/go-bindata v3.11.0+incompatible
	github.com/openshift/api v3.9.1-0.20190925205819-e39b0dc4e188+incompatible
	github.com/openshift/library-go v0.0.0-20190925211623-9b918c218a4e
	github.com/pkg/errors v0.8.1
	github.com/prometheus/client_golang v0.9.3-0.20190127221311-3c4408c8b829 // indirect
	github.com/prometheus/client_model v0.0.0-20190129233127-fd36f4220a90 // indirect
	github.com/prometheus/procfs v0.0.0-20190403104016-ea9eea638872 // indirect
	github.com/securego/gosec v0.0.0-20190709033609-4b59c948083c
	go.uber.org/zap v1.9.1
	golang.org/x/time v0.0.0-20190308202827-9d24e82272b4 // indirect
	google.golang.org/api v0.4.0
	gopkg.in/yaml.v2 v2.2.2

	// kubernetes-1.16.0
	k8s.io/api v0.0.0-20190918155943-95b840bb6a1f
	k8s.io/apimachinery v0.0.0-20190913080033-27d36303b655
	k8s.io/apiserver v0.0.0-20190918160949-bfa5e2e684ad
	k8s.io/client-go v0.0.0-20190918160344-1fbdaa4c8d90

	sigs.k8s.io/controller-runtime v0.2.0-beta.1.0.20190918234459-801e12a50160
	sigs.k8s.io/controller-tools v0.2.2-0.20190919191502-76a25b63325a
)

// Remove when the following merges:
// https://github.com/kubernetes-sigs/controller-runtime/pull/588
replace sigs.k8s.io/controller-runtime => github.com/munnerz/controller-runtime v0.1.8-0.20190907105316-d02b94982e57
