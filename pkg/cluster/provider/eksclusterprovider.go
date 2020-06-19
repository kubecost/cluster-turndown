package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kubecost/cluster-turndown/pkg/async"
	"github.com/kubecost/cluster-turndown/pkg/cluster/helper"
	"github.com/kubecost/cluster-turndown/pkg/logging"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/eks"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

const (
	LabelEKSNodePool = "eks.amazonaws.com/nodegroup"
)

//--------------------------------------------------------------------------
//  EKS NodePool Implementation
//--------------------------------------------------------------------------

// NodePool contains a node pool identifier and the initial number of nodes
// in the pool
type EKSNodePool struct {
	ng   *eks.Nodegroup
	asg  *autoscaling.Group
	tags map[string]string
}

func (np *EKSNodePool) Name() string            { return aws.StringValue(np.ng.NodegroupName) }
func (np *EKSNodePool) Project() string         { return "" }
func (np *EKSNodePool) Zone() string            { return aws.StringValue(np.asg.AvailabilityZones[0]) }
func (np *EKSNodePool) ClusterID() string       { return aws.StringValue(np.ng.ClusterName) }
func (np *EKSNodePool) MinNodes() int32         { return int32(aws.Int64Value(np.asg.MinSize)) }
func (np *EKSNodePool) MaxNodes() int32         { return int32(aws.Int64Value(np.asg.MaxSize)) }
func (np *EKSNodePool) NodeCount() int32        { return int32(aws.Int64Value(np.asg.DesiredCapacity)) }
func (np *EKSNodePool) AutoScaling() bool       { return false }
func (np *EKSNodePool) MachineType() string     { return aws.StringValue(np.ng.InstanceTypes[0]) }
func (np *EKSNodePool) Tags() map[string]string { return np.tags }
func (np *EKSNodePool) IsMaster() bool          { return false }

//--------------------------------------------------------------------------
//  EKS ClusterProvider Implementation
//--------------------------------------------------------------------------

// EKSClusterData is used to store cluster specific data
type EKSClusterData struct {
	ClusterName string
	NodeRole    string
	SubnetIDs   []string
	Cluster     *eks.Cluster
}

// ClusterProvider implementation for EKS
type EKSClusterProvider struct {
	kubernetes     kubernetes.Interface
	clusterManager *eks.EKS
	asgManager     *autoscaling.AutoScaling
	clusterData    *EKSClusterData
	log            logging.NamedLogger

	// Getting a list of node pools can be somewhat expensive for EKS
	// due to the nature of abstraction used by AWS. It's better if we
	// cache the last known list of nodepools for access on other helper
	// functions, but always overwrite the cache on GetNodePools().
	npLock    *sync.RWMutex
	nodePools []*EKSNodePool
}

// NewEKSClusterProvider creates a new EKSClusterProvider instance as the ClusterProvider
func NewEKSClusterProvider(kubernetes kubernetes.Interface) (ClusterProvider, error) {
	region := findAWSRegion(kubernetes)
	clusterManager, asgManager, err := newEKSClusterManager(region)
	if err != nil {
		return nil, err
	}

	cp := &EKSClusterProvider{
		kubernetes:     kubernetes,
		clusterManager: clusterManager,
		asgManager:     asgManager,
		npLock:         new(sync.RWMutex),
		nodePools:      []*EKSNodePool{},
		log:            logging.NamedLogger("EKSClusterProvider"),
	}

	err = cp.initClusterData()
	if err != nil {
		return nil, err
	}

	return cp, nil
}

// IsNodePool determines if there is a node pool with the name or not.
func (p *EKSClusterProvider) IsNodePool(name string) bool {
	resp, err := p.clusterManager.DescribeNodegroup(&eks.DescribeNodegroupInput{
		ClusterName:   aws.String(p.clusterData.ClusterName),
		NodegroupName: aws.String(name),
	})

	if err != nil {
		return false
	}

	return resp.Nodegroup != nil
}

// GetNodePoolName returns the name of a NodePool for a specific kubernetes node.
func (p *EKSClusterProvider) GetNodePoolName(node *v1.Node) string {
	nodePoolName, _, _ := eksInstanceInfoFor(node)
	return nodePoolName
}

// GetNodesFor returns a slice of kubernetes Node instances for the NodePool instance
// provided.
func (p *EKSClusterProvider) GetNodesFor(np NodePool) ([]*v1.Node, error) {
	name := np.Name()

	allNodes, err := p.kubernetes.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// NOTE: We just use the nodepool name comparison here. If we want to be
	// NOTE: exact, we could dive into the instance ids for the autoscaling groups,
	// NOTE: but is there any reason to exact match?
	nodes := []*v1.Node{}
	for _, n := range allNodes.Items {
		node := helper.NodePtr(n)

		nodePool, _, _ := eksInstanceInfoFor(node)
		if strings.EqualFold(nodePool, name) {
			nodes = append(nodes, node)
		}
	}

	return nodes, nil
}

// GetNodePools loads all of the provider NodePools in a cluster and returns them.
func (p *EKSClusterProvider) GetNodePools() ([]NodePool, error) {
	nodePools := []NodePool{}

	ngNames, err := p.clusterManager.ListNodegroups(&eks.ListNodegroupsInput{
		ClusterName: aws.String(p.clusterData.ClusterName),
	})
	if err != nil {
		return nodePools, err
	}

	for _, ngName := range ngNames.Nodegroups {
		ngResp, err := p.clusterManager.DescribeNodegroup(&eks.DescribeNodegroupInput{
			ClusterName:   aws.String(p.clusterData.ClusterName),
			NodegroupName: ngName,
		})
		if err != nil {
			return nodePools, err
		}

		nodeGroup := ngResp.Nodegroup

		asgResp, err := p.asgManager.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: []*string{nodeGroup.Resources.AutoScalingGroups[0].Name},
		})

		if err != nil {
			return nodePools, err
		}

		asg := asgResp.AutoScalingGroups[0]

		nodePools = append(nodePools, &EKSNodePool{
			ng:   nodeGroup,
			asg:  asg,
			tags: p.mergeLabelsAndTags(nodeGroup),
		})
	}

	// overwrite the node pool cache each time we make this call
	// the cache provides support to functionality leveraging the
	// node pool list without having to reload the list each time.
	p.cacheNodePools(nodePools)
	return nodePools, nil
}

// updates the cached node pools for the provider
func (p *EKSClusterProvider) cacheNodePools(nodePools []NodePool) {
	eksNodePools := make([]*EKSNodePool, len(nodePools))

	for i, np := range nodePools {
		eksNodePools[i] = np.(*EKSNodePool)
	}

	p.npLock.Lock()
	defer p.npLock.Unlock()

	p.nodePools = eksNodePools
}

// CreateNodePool creates a new node pool with the provided specs.
func (p *EKSClusterProvider) CreateNodePool(c context.Context, name, machineType string, nodeCount int32, diskType string, diskSizeGB int32, labels map[string]string) error {
	if diskSizeGB == 0 {
		diskSizeGB = 20
	}
	if labels == nil {
		labels = map[string]string{}
	}

	_, err := p.clusterManager.CreateNodegroupWithContext(c, &eks.CreateNodegroupInput{
		ClusterName:   aws.String(p.clusterData.ClusterName),
		NodeRole:      aws.String(p.clusterData.NodeRole),
		NodegroupName: aws.String(name),
		InstanceTypes: aws.StringSlice([]string{machineType}),
		Subnets:       aws.StringSlice(p.clusterData.SubnetIDs),
		DiskSize:      aws.Int64(int64(diskSizeGB)),
		Labels:        aws.StringMap(labels),
		ScalingConfig: &eks.NodegroupScalingConfig{
			DesiredSize: aws.Int64(int64(nodeCount)),
			MinSize:     aws.Int64(int64(nodeCount)),
			MaxSize:     aws.Int64(int64(nodeCount)),
		},
	})
	if err != nil {
		return err
	}

	// Instead of our kubernetes helper here, we can utilize the aws client to wait for the nodegroup to be
	// active
	return p.clusterManager.WaitUntilNodegroupActiveWithContext(c, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(p.clusterData.ClusterName),
		NodegroupName: aws.String(name),
	})
}

// CreateAutoScalingNodePool creates a new autoscaling node pool. The semantics behind autoscaling depend on the provider.
func (p *EKSClusterProvider) CreateAutoScalingNodePool(c context.Context, name, machineType string, minNodes, nodeCount, maxNodes int32, diskType string, diskSizeGB int32, labels map[string]string) error {
	if diskSizeGB == 0 {
		diskSizeGB = 20
	}
	if labels == nil {
		labels = map[string]string{}
	}

	_, err := p.clusterManager.CreateNodegroupWithContext(c, &eks.CreateNodegroupInput{
		ClusterName:   aws.String(p.clusterData.ClusterName),
		NodeRole:      aws.String(p.clusterData.NodeRole),
		NodegroupName: aws.String(name),
		InstanceTypes: aws.StringSlice([]string{machineType}),
		Subnets:       aws.StringSlice(p.clusterData.SubnetIDs),
		DiskSize:      aws.Int64(int64(diskSizeGB)),
		Labels:        aws.StringMap(labels),
		ScalingConfig: &eks.NodegroupScalingConfig{
			DesiredSize: aws.Int64(int64(nodeCount)),
			MinSize:     aws.Int64(int64(minNodes)),
			MaxSize:     aws.Int64(int64(maxNodes)),
		},
	})

	if err != nil {
		return err
	}

	// Wait for the nodegroup to be active
	return p.clusterManager.WaitUntilNodegroupActiveWithContext(c, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(p.clusterData.ClusterName),
		NodegroupName: aws.String(name),
	})
}

// UpdateNodePoolSize updates the number of nodes in a NodePool
func (p *EKSClusterProvider) UpdateNodePoolSize(c context.Context, nodePool NodePool, size int32) error {
	// NOTE: NodeGroups on EKS have a hard size limit of 1, even though AutoScalingGroups allow size: 0.
	// NOTE: To work-around this, we can just update the autoscaling group and the nodegroup parameters will
	// NOTE: automatically adjust.
	if size == 0 {
		eksNodePool, ok := nodePool.(*EKSNodePool)
		if !ok {
			return fmt.Errorf("Failed to update node pool size. NodePool was not an EKSNodePool.")
		}

		asgName := aws.StringValue(eksNodePool.asg.AutoScalingGroupName)

		_, err := p.asgManager.UpdateAutoScalingGroupWithContext(c, &autoscaling.UpdateAutoScalingGroupInput{
			AutoScalingGroupName: aws.String(asgName),
			DesiredCapacity:      aws.Int64(int64(size)),
			MinSize:              aws.Int64(int64(size)),
			MaxSize:              aws.Int64(int64(size)),
		})
		if err != nil {
			return err
		}

		return nil
	}

	// Any other sizes other than 0 can be made directly with the EKS API
	resp, err := p.clusterManager.UpdateNodegroupConfigWithContext(c, &eks.UpdateNodegroupConfigInput{
		ClusterName:   aws.String(nodePool.ClusterID()),
		NodegroupName: aws.String(nodePool.Name()),
		ScalingConfig: &eks.NodegroupScalingConfig{
			DesiredSize: aws.Int64(int64(size)),
			MinSize:     aws.Int64(int64(size)),
			MaxSize:     aws.Int64(int64(size)),
		},
	})

	if err != nil {
		// TODO: look into reporting errors from the update object
		var _ = resp.Update
	}

	return err
}

// UpdateNodePoolSizes updates the number of nodes in multiple NodePool instances.
func (p *EKSClusterProvider) UpdateNodePoolSizes(c context.Context, nodePools []NodePool, size int32) error {
	if len(nodePools) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(c)

	waitChannel := async.NewWaitChannel()
	waitChannel.Add(len(nodePools))

	for _, np := range nodePools {
		go func(nodePool NodePool) {
			defer waitChannel.Done()

			p.UpdateNodePoolSize(ctx, nodePool, size)
		}(np)
	}

	defer cancel()

	select {
	case <-waitChannel.Wait():
		return nil
	case <-time.After(30 * time.Minute):
		return fmt.Errorf("Resize Requests timed out after 30 minutes.")
	}
}

// DeleteNodePool deletes the NodePool.
func (p *EKSClusterProvider) DeleteNodePool(c context.Context, nodePool NodePool) error {
	_, err := p.clusterManager.DeleteNodegroupWithContext(c, &eks.DeleteNodegroupInput{
		ClusterName:   aws.String(nodePool.ClusterID()),
		NodegroupName: aws.String(nodePool.Name()),
	})

	if err != nil {
		return err
	}

	return p.clusterManager.WaitUntilNodegroupDeletedWithContext(c, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(nodePool.ClusterID()),
		NodegroupName: aws.String(nodePool.Name()),
	})
}

// CreateOrUpdateTags creates or updates the tags for NodePool instances.
func (p *EKSClusterProvider) CreateOrUpdateTags(c context.Context, nodePool NodePool, updateNodes bool, tags map[string]string) error {
	eksNodePool, ok := nodePool.(*EKSNodePool)
	if !ok {
		return fmt.Errorf("NodePool provided was not from EKS.")
	}

	// Default underlying labels and tags if nil
	if eksNodePool.ng.Labels == nil {
		eksNodePool.ng.Labels = make(map[string]*string)
	}
	if eksNodePool.ng.Tags == nil {
		eksNodePool.ng.Tags = make(map[string]*string)
	}

	// For Updating Node Labels, we just update the nodegroup config labels
	if updateNodes {
		_, err := p.clusterManager.UpdateNodegroupConfigWithContext(c, &eks.UpdateNodegroupConfigInput{
			ClusterName: aws.String(nodePool.ClusterID()),
			Labels: &eks.UpdateLabelsPayload{
				AddOrUpdateLabels: aws.StringMap(tags),
			},
		})

		for k, v := range tags {
			eksNodePool.ng.Labels[k] = aws.String(v)
			nodePool.Tags()[k] = v
		}

		return err
	}

	// For updating the tags, we apply the tags directly to the resource
	_, err := p.clusterManager.TagResourceWithContext(c, &eks.TagResourceInput{
		ResourceArn: eksNodePool.ng.NodegroupArn,
		Tags:        aws.StringMap(tags),
	})

	if err != nil {
		return err
	}

	for k, v := range tags {
		eksNodePool.ng.Tags[k] = aws.String(v)
		nodePool.Tags()[k] = v
	}

	return nil
}

// DeleteTags deletes the tags by key on a NodePool instance.
func (p *EKSClusterProvider) DeleteTags(c context.Context, nodePool NodePool, keys []string) error {
	eksNodePool, ok := nodePool.(*EKSNodePool)
	if !ok {
		return fmt.Errorf("NodePool provided was not from EKS.")
	}

	// Default underlying labels and tags if nil
	if eksNodePool.ng.Labels == nil {
		eksNodePool.ng.Labels = make(map[string]*string)
	}
	if eksNodePool.ng.Tags == nil {
		eksNodePool.ng.Tags = make(map[string]*string)
	}

	labels := eksNodePool.ng.Labels
	tags := eksNodePool.ng.Tags

	labelsToDelete := []string{}
	tagsToDelete := []string{}

	for _, key := range keys {
		if _, ok := labels[key]; ok {
			labelsToDelete = append(labelsToDelete, key)
		}
		if _, ok := tags[key]; ok {
			tagsToDelete = append(tagsToDelete, key)
		}
	}

	if len(tagsToDelete) > 0 {
		_, err := p.clusterManager.UntagResourceWithContext(c, &eks.UntagResourceInput{
			ResourceArn: eksNodePool.ng.NodegroupArn,
			TagKeys:     aws.StringSlice(tagsToDelete),
		})

		if err != nil {
			return err
		}
	}

	if len(labelsToDelete) > 0 {
		_, err := p.clusterManager.UpdateNodegroupConfigWithContext(c, &eks.UpdateNodegroupConfigInput{
			ClusterName: aws.String(nodePool.ClusterID()),
			Labels: &eks.UpdateLabelsPayload{
				RemoveLabels: aws.StringSlice(labelsToDelete),
			},
		})

		if err != nil {
			return err
		}
	}

	for _, k := range keys {
		delete(nodePool.Tags(), k)
		delete(eksNodePool.ng.Labels, k)
		delete(eksNodePool.ng.Tags, k)
	}

	return nil
}

// Creates a new EKS based cluster manager API to execute cluster commands
func newEKSClusterManager(region string) (*eks.EKS, *autoscaling.AutoScaling, error) {
	accessKey, err := loadAWSAccessKey(AWSAccessKey)
	if err == nil {
		os.Setenv(AWSAccessKeyID, accessKey.AccessKeyID)
		os.Setenv(AWSSecretAccessKey, accessKey.SecretAccessKey)
	} else {
		klog.Infof("[Warning] Failed to load valid access key from secret. Err=%s", err)
	}

	c := aws.NewConfig().
		WithRegion(region).
		WithCredentialsChainVerboseErrors(true)

	clusterManager := eks.New(session.New(c))
	asgManager := autoscaling.New(session.New(c))

	return clusterManager, asgManager, nil
}

// initClusterData looks up specific cluster data for use with all EKS APIs.
func (p *EKSClusterProvider) initClusterData() error {
	currentNodeName := os.Getenv("NODE_NAME")
	if currentNodeName == "" {
		return fmt.Errorf("NODE_NAME env variable was not set.")
	}

	currentNode, err := p.kubernetes.CoreV1().Nodes().Get(currentNodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	nodePoolName, _, instanceID := eksInstanceInfoFor(currentNode)

	clustersResp, err := p.clusterManager.ListClusters(&eks.ListClustersInput{})
	if err != nil {
		return err
	}

	var (
		clusterName *string
		nodeRole    string
	)

	clusterNames := aws.StringValueSlice(clustersResp.Clusters)
	for _, cName := range clusterNames {
		nodeGroupResp, err := p.clusterManager.DescribeNodegroup(&eks.DescribeNodegroupInput{
			ClusterName:   aws.String(cName),
			NodegroupName: aws.String(nodePoolName),
		})
		if err != nil || nodeGroupResp.Nodegroup == nil {
			klog.Infof("Could not find NodeGroup: %s in Cluster: %s", nodePoolName, cName)
			continue
		}

		ng := nodeGroupResp.Nodegroup

		if p.hasInstance(ng, instanceID) {
			clusterName = aws.String(cName)
			nodeRole = aws.StringValue(ng.NodeRole)
			break
		}
	}

	if clusterName == nil {
		return fmt.Errorf("Failed to locate Clusters which have node groups containing the current instance: %s", instanceID)
	}

	dcr, err := p.clusterManager.DescribeCluster(&eks.DescribeClusterInput{
		Name: clusterName,
	})
	if err != nil {
		return err
	}

	p.clusterData = &EKSClusterData{
		ClusterName: aws.StringValue(clusterName),
		NodeRole:    nodeRole,
		SubnetIDs:   aws.StringValueSlice(dcr.Cluster.ResourcesVpcConfig.SubnetIds),
		Cluster:     dcr.Cluster,
	}
	return nil
}

// hasInstance determines if the autoscaling group fronted by the nodegroup contains
// node instances equal to the provided instanceID.
func (p *EKSClusterProvider) hasInstance(ng *eks.Nodegroup, instanceID string) bool {
	asgNames := []*string{}
	for _, asgShim := range ng.Resources.AutoScalingGroups {
		asgNames = append(asgNames, asgShim.Name)
	}

	asgResp, err := p.asgManager.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: asgNames,
	})
	if err != nil {
		klog.Infof("Could not load autoscaling groups for names: %+v", aws.StringValueSlice(asgNames))
		return false
	}

	for _, group := range asgResp.AutoScalingGroups {
		for _, instance := range group.Instances {
			if strings.EqualFold(aws.StringValue(instance.InstanceId), instanceID) {
				return true
			}
		}
	}

	return false
}

// helper function that merges nodepool labels (appear on nodes) and tags (specific to aws)
func (p *EKSClusterProvider) mergeLabelsAndTags(ng *eks.Nodegroup) map[string]string {
	m := make(map[string]string)

	for k, v := range aws.StringValueMap(ng.Labels) {
		m[k] = v
	}
	for k, v := range aws.StringValueMap(ng.Tags) {
		m[k] = v
	}

	return m
}

// Pulls the instance id and zone from the Node.Spec.ProviderID
func eksInstanceInfoFor(node *v1.Node) (nodePoolName string, zone string, instanceID string) {
	name := node.Labels[LabelEKSNodePool]
	id := node.Spec.ProviderID

	splitted := strings.Split(id[7:], "/")
	return name, splitted[0], splitted[1]
}
