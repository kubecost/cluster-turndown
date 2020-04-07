package provider

import (
	"context"
	"errors"
	"strings"

	"cloud.google.com/go/compute/metadata"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

// ClusterProvider contains methods used to manage cluster node resources
type ClusterProvider interface {
	// IsNodePool determines if there is a node pool with the name or not.
	IsNodePool(name string) bool

	// GetNodePoolName returns the name of a NodePool for a specific kubernetes node.
	GetNodePoolName(node *v1.Node) string

	// GetNodesFor returns a slice of kubernetes Node instances for the NodePool instance provided.
	GetNodesFor(np NodePool) ([]*v1.Node, error)

	// GetNodePools loads all of the provider NodePools in a cluster and returns them.
	GetNodePools() ([]NodePool, error)

	// CreateNodePool creates a new node pool with the provided specs.
	CreateNodePool(c context.Context, name, machineType string, nodeCount int32, diskType string, diskSizeGB int32, labels map[string]string) error

	// CreateAutoScalingNodePool creates a new autoscaling node pool. The semantics behind autoscaling depend on the provider.
	CreateAutoScalingNodePool(c context.Context, name, machineType string, minNodes, nodeCount, maxNodes int32, diskType string, diskSizeGB int32, labels map[string]string) error

	// UpdateNodePoolSize updates the number of nodes in a NodePool
	UpdateNodePoolSize(c context.Context, nodePool NodePool, size int32) error

	// UpdateNodePoolSizes updates the number of nodes in multiple NodePool instances.
	UpdateNodePoolSizes(c context.Context, nodePools []NodePool, size int32) error

	// DeleteNodePool deletes the NodePool.
	DeleteNodePool(c context.Context, nodePool NodePool) error

	// CreateOrUpdateTags creates or updates the tags for NodePool instances.
	CreateOrUpdateTags(c context.Context, nodePool NodePool, updateNodes bool, tags map[string]string) error

	// DeleteTags deletes the tags by key on a NodePool instance.
	DeleteTags(c context.Context, nodePool NodePool, keys []string) error
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
	IsMaster() bool
}

func NewClusterProvider(client kubernetes.Interface) (ClusterProvider, error) {
	if metadata.OnGCE() {
		return NewGKEClusterProvider(client), nil
	}

	nodes, err := client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	provider := strings.ToLower(nodes.Items[0].Spec.ProviderID)
	if strings.HasPrefix(provider, "aws") {
		klog.V(2).Info("Found ProviderID starting with \"aws\", using AWS Provider")
		return NewAWSClusterProvider(client), nil
	} else if strings.HasPrefix(provider, "azure") {
		klog.V(2).Info("Found ProviderID starting with \"azure\", using Azure Provider")
		return nil, errors.New("Azure Not Supported")
	} else {
		klog.V(2).Info("Unsupported provider, falling back to default")
		return nil, errors.New("Custom Not Supported")
	}
}
