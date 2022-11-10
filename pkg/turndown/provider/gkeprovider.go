package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/kubecost/cluster-turndown/v2/pkg/async"
	cp "github.com/kubecost/cluster-turndown/v2/pkg/cluster/provider"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	GKETurndownPoolName = "cluster-turndown"
)

// ComputeProvider for GKE
type GKEProvider struct {
	kubernetes      kubernetes.Interface
	clusterProvider cp.ClusterProvider
	metadata        *cp.GKEMetaData
	log             zerolog.Logger
}

func NewGKEProvider(kubernetes kubernetes.Interface, clusterProvider cp.ClusterProvider) TurndownProvider {
	return &GKEProvider{
		kubernetes:      kubernetes,
		clusterProvider: clusterProvider,
		metadata:        cp.NewGKEMetaData(),
		log:             log.With().Str("component", "GKEProvider").Logger(),
	}
}

func (p *GKEProvider) IsTurndownNodePool() bool {
	return p.clusterProvider.IsNodePool(GKETurndownPoolName)
}

func (p *GKEProvider) CreateSingletonNodePool(labels map[string]string) error {
	ctx := context.TODO()

	return p.clusterProvider.CreateNodePool(ctx, GKETurndownPoolName, "g1-small", 1, "pd-standard", 10, toTurndownNodePoolLabels(labels))
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
				p.log.Error().Msgf("Failed to resize NodePool: %s", nodePool.Name())
				return
			}

			p.log.Info().Msgf("Resized NodePool Successfully: %s", nodePool.Name())
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
