module github.com/kubecost/cluster-turndown/v2

go 1.16

require (
	cloud.google.com/go v0.54.0
	github.com/aws/aws-sdk-go v1.28.7
	github.com/google/uuid v1.1.2
	github.com/imdario/mergo v0.3.8 // indirect
	github.com/rs/zerolog v1.28.0
	google.golang.org/genproto v0.0.0-20201019141844-1ed22bb0c154
	google.golang.org/grpc v1.27.1
	k8s.io/api v0.20.4
	k8s.io/apimachinery v0.20.4
	k8s.io/client-go v0.20.4
	k8s.io/code-generator v0.20.15
	sigs.k8s.io/yaml v1.2.0
)
