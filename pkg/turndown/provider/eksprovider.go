package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kubecost/cluster-turndown/pkg/logging"

	cp "github.com/kubecost/cluster-turndown/pkg/cluster/provider"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	EKSNodeGroupPreviousKey = "cluster.turndown.previous"
	EKSTurndownPoolName     = "cluster-turndown"
)

// ComputeProvider for AWS EKS
type EKSProvider struct {
	kubernetes      kubernetes.Interface
	clusterProvider cp.ClusterProvider
	log             logging.NamedLogger
}

func NewEKSProvider(kubernetes kubernetes.Interface) ComputeProvider {
	return &EKSProvider{
		kubernetes:      kubernetes,
		clusterProvider: cp.NewEKSClusterProvider(kubernetes),
		log:             logging.NamedLogger("EKSProvider"),
	}
}

func (p *EKSProvider) IsTurndownNodePool() bool {
	return p.clusterProvider.IsNodePool(EKSTurndownPoolName)
}

func (p *EKSProvider) CreateSingletonNodePool() error {
	ctx := context.TODO()

	return p.clusterProvider.CreateNodePool(ctx, EKSTurndownPoolName, "t2.small", 1, "gp2", 10, map[string]string{
		TurndownNodeLabel: "true",
	})
}

func (p *EKSProvider) GetPoolID(node *v1.Node) string {
	return p.clusterProvider.GetNodePoolName(node)
}

func (p *EKSProvider) GetNodePools() ([]cp.NodePool, error) {
	return p.clusterProvider.GetNodePools()
}

func (p *EKSProvider) SetNodePoolSizes(nodePools []cp.NodePool, size int32) error {
	if len(nodePools) == 0 {
		return nil
	}

	c, cancel := context.WithCancel(context.TODO())
	defer cancel()

	for _, np := range nodePools {
		min, max, count := np.MinNodes(), np.MaxNodes(), np.NodeCount()
		rng := p.flatRange(min, max, count)

		err := p.clusterProvider.UpdateNodePoolSize(c, np, size)
		if err != nil {
			p.log.Err("Updating NodePool: %s", err.Error())
			return err
		}

		err = p.clusterProvider.CreateOrUpdateTags(c, np, false, map[string]string{
			EKSNodeGroupPreviousKey: rng,
		})
		if err != nil {
			p.log.Err("Creating or Updating Tags: %s", err.Error())

			return err
		}
	}

	return nil
}

func (p *EKSProvider) ResetNodePoolSizes(nodePools []cp.NodePool) error {
	if len(nodePools) == 0 {
		return nil
	}

	c, cancel := context.WithCancel(context.TODO())
	defer cancel()

	for _, np := range nodePools {
		tags := np.Tags()
		rangeTag, ok := tags[EKSNodeGroupPreviousKey]
		if !ok {
			p.log.Err("Failed to locate tag: %s for NodePool: %s", EKSNodeGroupPreviousKey, np.Name())
			continue
		}

		_, _, count := p.expandRange(rangeTag)
		if count < 0 {
			p.log.Err("Failed to parse range used to resize node pool.")
			continue
		}

		err := p.clusterProvider.UpdateNodePoolSize(c, np, count)
		if err != nil {
			p.log.Err("Updating NodePool: %s", err.Error())
			return err
		}

		err = p.clusterProvider.DeleteTags(c, np, []string{EKSNodeGroupPreviousKey})
		if err != nil {
			p.log.Err("Deleting Tags: %s", err.Error())

			return err
		}
	}

	return nil
}

func (p *EKSProvider) flatRange(min, max, count int32) string {
	return fmt.Sprintf("%d/%d/%d", min, max, count)
}

func (p *EKSProvider) expandRange(s string) (int32, int32, int32) {
	values := strings.Split(s, "/")

	count, err := strconv.Atoi(values[2])
	if err != nil {
		p.log.Err("Parsing Count: %s", err.Error())
		return -1, -1, -1
	}

	min, err := strconv.Atoi(values[0])
	if err != nil {
		p.log.Err("Parsing Min: %s", err.Error())
		min = count
	}

	max, err := strconv.Atoi(values[1])
	if err != nil {
		p.log.Err("Parsing Max: %s", err.Error())
		max = count
	}

	return int32(min), int32(max), int32(count)
}
