package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kubecost/cluster-turndown/v2/pkg/logging"

	cp "github.com/kubecost/cluster-turndown/v2/pkg/cluster/provider"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	AWSClusterIDTagKey      = "KubernetesCluster"
	AWSGroupNameTagKey      = "aws:autoscaling:groupName"
	AWSRoleMasterTagKey     = "k8s.io/role/master"
	AWSRoleNodeTagKey       = "k8s.io/role/node"
	AWSNodeGroupPreviousKey = "cluster.turndown.previous"
	AWSTurndownPoolName     = "cluster-turndown"
)

// ComputeProvider for AWS
type AWSProvider struct {
	kubernetes      kubernetes.Interface
	clusterProvider cp.ClusterProvider
	log             logging.NamedLogger
}

func NewAWSProvider(kubernetes kubernetes.Interface, clusterProvider cp.ClusterProvider) TurndownProvider {
	return &AWSProvider{
		kubernetes:      kubernetes,
		clusterProvider: clusterProvider,
		log:             logging.NamedLogger("AWSProvider"),
	}
}

func (p *AWSProvider) IsTurndownNodePool() bool {
	return p.clusterProvider.IsNodePool(AWSTurndownPoolName)
}

func (p *AWSProvider) CreateSingletonNodePool(labels map[string]string) error {
	ctx := context.TODO()

	return p.clusterProvider.CreateNodePool(ctx, AWSTurndownPoolName, "t2.small", 1, "gp2", 10, toTurndownNodePoolLabels(labels))
}

func (p *AWSProvider) GetPoolID(node *v1.Node) string {
	return p.clusterProvider.GetNodePoolName(node)
}

func (p *AWSProvider) GetNodePools() ([]cp.NodePool, error) {
	return p.clusterProvider.GetNodePools()
}

func (p *AWSProvider) SetNodePoolSizes(nodePools []cp.NodePool, size int32) error {
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
			AWSNodeGroupPreviousKey: rng,
		})
		if err != nil {
			p.log.Err("Creating or Updating Tags: %s", err.Error())

			return err
		}
	}

	return nil
}

func (p *AWSProvider) ResetNodePoolSizes(nodePools []cp.NodePool) error {
	if len(nodePools) == 0 {
		return nil
	}

	c, cancel := context.WithCancel(context.TODO())
	defer cancel()

	for _, np := range nodePools {
		tags := np.Tags()
		rangeTag, ok := tags[AWSNodeGroupPreviousKey]
		if !ok {
			p.log.Err("Failed to locate tag: %s for NodePool: %s", AWSNodeGroupPreviousKey, np.Name())
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

		err = p.clusterProvider.DeleteTags(c, np, []string{AWSNodeGroupPreviousKey})
		if err != nil {
			p.log.Err("Deleting Tags: %s", err.Error())

			return err
		}
	}

	return nil
}

func (p *AWSProvider) flatRange(min, max, count int32) string {
	return fmt.Sprintf("%d/%d/%d", min, max, count)
}

func (p *AWSProvider) expandRange(s string) (int32, int32, int32) {
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
