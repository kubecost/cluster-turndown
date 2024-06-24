package provider

import (
	"context"
	"strings"

	v1 "k8s.io/api/core/v1"
)

type MockClusterProvider struct {
}

func NewMockClusterProvider() MockClusterProvider {
	return MockClusterProvider{}
}

func (p MockClusterProvider) IsNodePool(name string) bool {
	return strings.Contains(name, "test")
}

func (p MockClusterProvider) GetNodePoolName(node *v1.Node) string {
	return "test-pool"
}

func (p MockClusterProvider) GetNodesFor(np NodePool) ([]*v1.Node, error) {
	var n []*v1.Node
	return n, nil
}

func (p MockClusterProvider) GetNodePools() ([]NodePool, error) {
	var np []NodePool
	return np, nil
}

func (p MockClusterProvider) CreateNodePool(c context.Context, name, machineType string, nodeCount int32, diskType string, diskSizeGB int32, labels map[string]string) error {
	return nil
}

func (p MockClusterProvider) CreateAutoScalingNodePool(c context.Context, name, machineType string, minNodes, nodeCount, maxNodes int32, diskType string, diskSizeGB int32, labels map[string]string) error {
	return nil
}

func (p MockClusterProvider) UpdateNodePoolSize(c context.Context, nodePool NodePool, size int32) error {
	return nil
}

func (p MockClusterProvider) UpdateNodePoolSizes(c context.Context, nodePools []NodePool, size int32) error {
	return nil
}

func (p MockClusterProvider) DeleteNodePool(c context.Context, nodePool NodePool) error {
	return nil
}

func (p MockClusterProvider) CreateOrUpdateTags(c context.Context, nodePool NodePool, updateNodes bool, tags map[string]string) error {
	return nil
}

func (p MockClusterProvider) DeleteTags(c context.Context, nodePool NodePool, keys []string) error {
	return nil
}
