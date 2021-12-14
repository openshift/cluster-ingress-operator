module github.com/openshift/cluster-ingress-operator

go 1.16

require (
	github.com/Azure/azure-sdk-for-go v30.0.0+incompatible
	github.com/Azure/go-autorest/autorest v0.11.18
	github.com/Azure/go-autorest/autorest/adal v0.9.13
	github.com/Azure/go-autorest/autorest/to v0.2.0 // indirect
	github.com/Azure/go-autorest/autorest/validation v0.1.0 // indirect
	github.com/IBM/go-sdk-core/v4 v4.9.0
	github.com/IBM/networking-go-sdk v0.14.0
	github.com/aliyun/alibaba-cloud-sdk-go v1.61.1286
	github.com/aws/aws-sdk-go v1.38.49
	github.com/davecgh/go-spew v1.1.1
	github.com/ghodss/yaml v1.0.0
	github.com/go-logr/logr v0.4.0
	github.com/go-logr/zapr v0.4.0
	github.com/google/go-cmp v0.5.6
	github.com/kevinburke/go-bindata v3.11.0+incompatible
	github.com/openshift/api v0.0.0-20211021122928-16dd969d5550
	github.com/openshift/build-machinery-go v0.0.0-20211213093930-7e33a7eb4ce3
	github.com/openshift/library-go v0.0.0-20210331235027-66936e2fcc52
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.11.0
	github.com/spf13/cobra v1.1.3
	github.com/stretchr/testify v1.7.0
	github.com/summerwind/h2spec v0.0.0-20200804131034-70ac22940108
	github.com/tcnksm/go-httpstat v0.2.1-0.20191008022543-e866bb274419
	go.uber.org/zap v1.17.0
	google.golang.org/api v0.49.0
	google.golang.org/grpc v1.38.0
	gopkg.in/fsnotify.v1 v1.4.7
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.22.1
	k8s.io/apimachinery v0.22.1
	k8s.io/apiserver v0.22.1
	k8s.io/client-go v0.22.1
	k8s.io/utils v0.0.0-20210707171843-4b05e18ac7d9
	sigs.k8s.io/controller-runtime v0.9.0
	sigs.k8s.io/controller-tools v0.4.1
)
