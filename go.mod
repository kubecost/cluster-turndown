module github.com/kubecost/cluster-turndown

go 1.16

require (
	cloud.google.com/go v0.46.3
	github.com/aws/aws-sdk-go v1.28.7
	github.com/google/uuid v1.1.1
	github.com/imdario/mergo v0.3.8 // indirect
	google.golang.org/genproto v0.0.0-20190911173649-1774047e7e51
	google.golang.org/grpc v1.21.1
	k8s.io/api v0.0.0-20190913080256-21721929cffa
	k8s.io/apimachinery v0.0.0-20190913075812-e119e5e154b6
	k8s.io/client-go v0.0.0-20190620085101-78d2af792bab
	k8s.io/code-generator v0.17.3
	k8s.io/klog v1.0.0
	sigs.k8s.io/yaml v1.1.0
)
