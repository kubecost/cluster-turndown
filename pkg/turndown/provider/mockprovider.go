package provider

import (
	cp "github.com/kubecost/cluster-turndown/v2/pkg/cluster/provider"
	v1 "k8s.io/api/core/v1"
)

type MockTurndownProvider struct {
	clusterProvider cp.ClusterProvider
}

func NewMockTurndownProvider(clusterProvider cp.ClusterProvider) MockTurndownProvider {
	return MockTurndownProvider{
		clusterProvider: clusterProvider,
	}
}

func (tp MockTurndownProvider) IsTurndownNodePool() bool {
	return tp.clusterProvider.IsNodePool("")
}

func (tp MockTurndownProvider) CreateSingletonNodePool(labels map[string]string) error {
	return nil
}

func (tp MockTurndownProvider) GetNodePools() ([]cp.NodePool, error) {
	return tp.clusterProvider.GetNodePools()
}

func (tp MockTurndownProvider) GetPoolID(node *v1.Node) string {
	return tp.clusterProvider.GetNodePoolName(node)
}

func (tp MockTurndownProvider) SetNodePoolSizes(nodePools []cp.NodePool, size int32) error {
	return nil
}

func (tp MockTurndownProvider) ResetNodePoolSizes(nodePools []cp.NodePool) error {
	return nil
}
