package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/kubecost/cluster-turndown/pkg/async"
	cp "github.com/kubecost/cluster-turndown/pkg/cluster/provider"
	"github.com/kubecost/cluster-turndown/pkg/logging"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	GKETurndownPoolName = "cluster-turndown"
)

// ComputeProvider for GKE
type GKEProvider struct {
	kubernetes      kubernetes.Interface
	clusterProvider cp.ClusterProvider
	metadata        *cp.GKEMetaData
	log             logging.NamedLogger
}

func NewGKEProvider(kubernetes kubernetes.Interface) ComputeProvider {
	return &GKEProvider{
		kubernetes:      kubernetes,
		clusterProvider: cp.NewGKEClusterProvider(kubernetes),
		metadata:        cp.NewGKEMetaData(),
		log:             logging.NamedLogger("GKEProvider"),
	}
}

func (p *GKEProvider) IsTurndownNodePool() bool {
	return p.clusterProvider.IsNodePool(GKETurndownPoolName)
}

func (p *GKEProvider) CreateSingletonNodePool() error {
	ctx := context.TODO()

	return p.clusterProvider.CreateNodePool(ctx, GKETurndownPoolName, "g1-small", 1, "pd-standard", 10, map[string]string{
		TurndownNodeLabel: "true",
	})
}

func (p *GKEProvider) GetPoolID(node *v1.Node) string {
	return p.clusterProvider.GetNodePoolName(node)
}

func (p *GKEProvider) GetNodePools() ([]cp.NodePool, error) {
	return p.clusterProvider.GetNodePools()
}

func (p *GKEProvider) SetNodePoolSizes(nodePools []cp.NodePool, size int32) error {
	if len(nodePools) == 0 {
		return nil
	}

	ctx := context.TODO()

	return p.clusterProvider.UpdateNodePoolSizes(ctx, nodePools, size)
}

func (p *GKEProvider) ResetNodePoolSizes(nodePools []cp.NodePool) error {
	if len(nodePools) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(context.TODO())

	waitChannel := async.NewWaitChannel()
	waitChannel.Add(len(nodePools))

	for _, np := range nodePools {
		go func(nodePool cp.NodePool) {
			defer waitChannel.Done()

			err := p.clusterProvider.UpdateNodePoolSize(ctx, nodePool, nodePool.NodeCount())
			if err != nil {
				p.log.Log("[Error] Failed to resize NodePool: %s", nodePool.Name())
				return
			}

			p.log.Log("Resized NodePool Successfully: %s", nodePool.Name())
		}(np)
	}

	defer cancel()

	select {
	case <-waitChannel.Wait():
		return nil
	case <-time.After(30 * time.Minute):
		return fmt.Errorf("Resizing node requests timed out after 30 minutes.")
	}
}
