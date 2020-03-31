package provider

import (
	"context"
	"errors"
	"strings"

	"cloud.google.com/go/compute/metadata"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

// ClusterProvider contains methods used to manage cluster node resources
type ClusterProvider interface {
	GetNodePools() ([]NodePool, error)
	CreateNodePool(c context.Context, name, machineType string, nodeCount int32, diskType string, diskSizeGB int32, labels map[string]string) error
	CreateAutoScalingNodePool(c context.Context, name, machineType string, minNodes, nodeCount, maxNodes int32, diskType string, diskSizeGB int32, labels map[string]string) error
	UpdateNodePoolSize(c context.Context, nodePool NodePool, size int32) error
	UpdateNodePoolSizes(c context.Context, nodePools []NodePool, size int32) error
	DeleteNodePool(c context.Context, nodePool NodePool) error
}

// NodePool contains a node pool identifier and the initial number of nodes
// in the pool
type NodePool interface {
	Name() string
	Project() string
	Zone() string
	ClusterID() string
	MinNodes() int32
	MaxNodes() int32
	NodeCount() int32
	AutoScaling() bool
	MachineType() string
	Tags() map[string]string
}

var _ = klog.V(1)

func NewProvider(client kubernetes.Interface) (ClusterProvider, error) {
	if metadata.OnGCE() {
		return NewGKEProvider(client), nil
	}

	nodes, err := client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	provider := strings.ToLower(nodes.Items[0].Spec.ProviderID)
	if strings.HasPrefix(provider, "aws") {
		klog.V(2).Info("Found ProviderID starting with \"aws\", using AWS Provider")
		return NewAWSProvider(client), nil
	} else if strings.HasPrefix(provider, "azure") {
		klog.V(2).Info("Found ProviderID starting with \"azure\", using Azure Provider")
		return nil, errors.New("Azure Not Supported")
	} else {
		klog.V(2).Info("Unsupported provider, falling back to default")
		return nil, errors.New("Custom Not Supported")
	}
}
