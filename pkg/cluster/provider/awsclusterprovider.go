package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kubecost/cluster-turndown/pkg/cluster/helper"
	"github.com/kubecost/cluster-turndown/pkg/file"
	"github.com/kubecost/cluster-turndown/pkg/logging"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/s3"
)

const (
	AWSAccessKey                  = "/var/keys/service-key.json"
	AWSAccessKeyID                = "AWS_ACCESS_KEY_ID"
	AWSSecretAccessKey            = "AWS_SECRET_ACCESS_KEY"
	AWSClusterIDTagKey            = "KubernetesCluster"
	AWSGroupNameTagKey            = "aws:autoscaling:groupName"
	AWSRoleMasterTagKey           = "k8s.io/role/master"
	AWSRoleNodeTagKey             = "k8s.io/role/node"
	AWSNodeGroupPreviousKey       = "cluster.turndown.previous"
	AutoScalingGroupResourceType  = "auto-scaling-group"
	placeholderInstanceNamePrefix = "i-placeholder"

	AWSDefaultMachineType = "t2.medium"
	AWSDefaultDiskType    = "gp2"
	AWSDefaultDiskSizeGB  = 64
)

//--------------------------------------------------------------------------
//  AWS Default Values
//--------------------------------------------------------------------------

func GetAWSDefaultBlockDeviceMappings(diskType string, diskSizeGB int64) []*autoscaling.BlockDeviceMapping {
	return []*autoscaling.BlockDeviceMapping{
		&autoscaling.BlockDeviceMapping{
			DeviceName: aws.String("/dev/xvda"),
			Ebs: &autoscaling.Ebs{
				DeleteOnTermination: aws.Bool(true),
				VolumeSize:          aws.Int64(diskSizeGB),
				VolumeType:          aws.String(diskType),
			},
		},
	}
}

func GetAWSInstanceMonitoringDefaults() *autoscaling.InstanceMonitoring {
	return &autoscaling.InstanceMonitoring{
		Enabled: aws.Bool(false),
	}
}

//--------------------------------------------------------------------------
//  AWS NodePool Implementation
//--------------------------------------------------------------------------

// AWS NodePool based on AutoScalingGroup
type AWSNodePool struct {
	asg  *autoscaling.Group
	lc   *autoscaling.LaunchConfiguration
	tags map[string]string
}

func (np *AWSNodePool) Project() string         { return "" }
func (np *AWSNodePool) Name() string            { return aws.StringValue(np.asg.AutoScalingGroupName) }
func (np *AWSNodePool) Zone() string            { return aws.StringValue(np.asg.AvailabilityZones[0]) }
func (np *AWSNodePool) ClusterID() string       { return np.tags[AWSClusterIDTagKey] }
func (np *AWSNodePool) MinNodes() int32         { return int32(aws.Int64Value(np.asg.MinSize)) }
func (np *AWSNodePool) MaxNodes() int32         { return int32(aws.Int64Value(np.asg.MaxSize)) }
func (np *AWSNodePool) NodeCount() int32        { return int32(aws.Int64Value(np.asg.DesiredCapacity)) }
func (np *AWSNodePool) AutoScaling() bool       { return false }
func (np *AWSNodePool) MachineType() string     { return aws.StringValue(np.lc.InstanceType) }
func (np *AWSNodePool) Tags() map[string]string { return np.tags }
func (np *AWSNodePool) IsMaster() bool          { _, ok := np.tags["k8s.io/role/master"]; return ok }

//--------------------------------------------------------------------------
//  AWS Access Key Representation
//--------------------------------------------------------------------------

// Access
type AWSAccessKeyFile struct {
	AccessKeyID     string `json:"aws_access_key_id"`
	SecretAccessKey string `json:"aws_secret_access_key"`
}

//--------------------------------------------------------------------------
//  AWS Cluster Data
//--------------------------------------------------------------------------

// Holds portions of the NodeUp script used as user data on LaunchConfigurations
type awsNodeUpScript struct {
	first  string
	second string
	end    string
}

// Holds ImageID and Image Location for a specific AMI
type awsImageData struct {
	imageID       string
	imageLocation string
}

// Container for cluster specific data and configurations
type AWSClusterData struct {
	lock               *sync.RWMutex
	launchConfigs      map[string]*autoscaling.LaunchConfiguration
	securityGroups     []*string
	vpcSubnets         []*string
	userData           *awsNodeUpScript
	imageData          *awsImageData
	clusterBucket      string
	clusterName        string
	iamInstanceProfile string
	keyName            string
}

// creates new aws cluster data instance for managing general reusable cluster configuration
func newAWSClusterData() *AWSClusterData {
	return &AWSClusterData{
		lock:               new(sync.RWMutex),
		launchConfigs:      make(map[string]*autoscaling.LaunchConfiguration),
		securityGroups:     []*string{},
		vpcSubnets:         []*string{},
		clusterBucket:      "",
		clusterName:        "",
		iamInstanceProfile: "",
		keyName:            "",
		imageData: &awsImageData{
			imageID:       "",
			imageLocation: "",
		},
		userData: &awsNodeUpScript{
			first:  "",
			second: "",
			end:    "",
		},
	}
}

// thread-safe update for launch configs
func (p *AWSClusterData) updateLaunchConfigs(launchConfigs map[string]*autoscaling.LaunchConfiguration) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.launchConfigs = launchConfigs
}

// thread-safe look up for a launch configuration for a specific machine type if it exists
// returns nil if does not exist
func (p *AWSClusterData) launchConfigFor(machineType string) *autoscaling.LaunchConfiguration {
	p.lock.RLock()
	defer p.lock.RUnlock()

	if p.launchConfigs == nil {
		return nil
	}

	if lc, ok := p.launchConfigs[machineType]; ok {
		return lc
	}

	return nil
}

// thread-safe update for VPC subnets
func (p *AWSClusterData) updateVPCSubnets(subnets []*string) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.vpcSubnets = subnets
}

// thread-safe look up for the vpc subnets
func (p *AWSClusterData) getVPCSubnets() []string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return aws.StringValueSlice(p.vpcSubnets)
}

// thread-safe update for security groups used in the cluster
func (p *AWSClusterData) updateSecurityGroups(sgs []*string) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.securityGroups = sgs
}

// thread-safe look up for security groups
func (p *AWSClusterData) getSecurityGroups() []string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return aws.StringValueSlice(p.securityGroups)
}

// thread-safe update for image id (ami)
func (p *AWSClusterData) updateImage(imageID *string, imageLocation *string) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.imageData.imageID = aws.StringValue(imageID)
	p.imageData.imageLocation = aws.StringValue(imageLocation)
}

// thread-safe look up for image id
func (p *AWSClusterData) getImageID() string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return p.imageData.imageID
}

// thread-safe look up for image location
func (p *AWSClusterData) getImageLocation() string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return p.imageData.imageLocation
}

// thread-safe update for IAM profile
func (p *AWSClusterData) updateIAMInstanceProfile(iamProfile *string) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.iamInstanceProfile = aws.StringValue(iamProfile)
}

// thread-safe look up for iam profile
func (p *AWSClusterData) getIAMInstanceProfile() string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return p.iamInstanceProfile
}

// thread-safe update for key name
func (p *AWSClusterData) updateKeyName(keyName *string) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.keyName = aws.StringValue(keyName)
}

// thread-safe look up for key name
func (p *AWSClusterData) getKeyName() string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return p.keyName
}

// update user data for the cluster by creating a "template". This template will
// be used by aws config-init when the nodes come online. Kops uses 'nodeup'
// which we'll need to just ensure is configured before the LC is used.
func (p *AWSClusterData) updateUserData(userData *string) {
	data := aws.StringValue(userData)
	if data == "" {
		klog.Infof("[Warning] Failed to update user data. String was empty!")
		return
	}

	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		klog.Infof("[Warning] Failed to decode base64 user data: %s", err.Error())
		return
	}

	ud := string(decoded)

	// NOTE: This is a hack that replaces two specific lines in the user data relating to
	// NOTE: the instance group name. We will parse the text before first insertion, between
	// NOTE: insertions, and after the 2nd insertion
	reader := bufio.NewReader(strings.NewReader(ud))
	first := []string{}
	second := []string{}
	end := []string{}

	var (
		clusterBucket string
		clusterName   string
		current       *[]string = &first
	)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				klog.V(1).Infof("Failed to read user data: %s", err.Error())
				return
			}

			break
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "kops.k8s.io/instancegroup") {
			current = &second
		} else if strings.HasPrefix(trimmed, "InstanceGroupName") {
			current = &end
		} else if strings.HasPrefix(trimmed, "ConfigBase") {
			pair := strings.Split(trimmed, ": ")
			if len(pair) == 2 {
				clusterPair := strings.Split(pair[1][5:], "/")
				clusterBucket = clusterPair[0]
				clusterName = clusterPair[1]
			}

			*current = append(*current, line)
		} else {
			*current = append(*current, line)
		}
	}

	p.lock.Lock()
	defer p.lock.Unlock()

	p.userData.first = strings.Join(first, "")
	p.userData.second = strings.Join(second, "")
	p.userData.end = strings.Join(end, "")
	p.clusterBucket = clusterBucket
	p.clusterName = clusterName
}

// thread-safe look up for user data
func (p *AWSClusterData) getUserData(groupName string) string {
	p.lock.Lock()
	f, s, e := p.userData.first, p.userData.second, p.userData.end
	p.lock.Unlock()

	// when we reconstruct user data, we insert our specific group name
	// replacements and reconstruct the full string
	ud := strings.Join([]string{
		f,
		fmt.Sprintf("  kops.k8s.io/instancegroup: %s\n", groupName),
		s,
		fmt.Sprintf("InstanceGroupName: %s\n", groupName),
		e,
	}, "")

	// user data is base64 encoded
	return base64.StdEncoding.EncodeToString([]byte(ud))
}

// thread-safe look up for the cluster bucket
func (p *AWSClusterData) getClusterBucket() string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return p.clusterBucket
}

// thread-safe look up for the cluster name
func (p *AWSClusterData) getClusterName() string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return p.clusterName
}

//--------------------------------------------------------------------------
//  AWS ClusterProvider Implementation
//--------------------------------------------------------------------------

// ClusterProvider for AWS
type AWSClusterProvider struct {
	kubernetes     kubernetes.Interface
	clusterManager *autoscaling.AutoScaling
	ec2Client      *ec2.EC2
	s3Client       *s3.S3
	clusterData    *AWSClusterData
	log            logging.NamedLogger
}

// NewAWSClusterProvider creates a new AWSClusterProvider instance as the ClusterProvider
func NewAWSClusterProvider(kubernetes kubernetes.Interface) (ClusterProvider, error) {
	region := findAWSRegion(kubernetes)
	clusterManager, ec2Client, s3Client, err := newAWSClusterManager(region)
	if err != nil {
		return nil, err
	}

	return &AWSClusterProvider{
		kubernetes:     kubernetes,
		clusterManager: clusterManager,
		ec2Client:      ec2Client,
		s3Client:       s3Client,
		clusterData:    newAWSClusterData(),
		log:            logging.NamedLogger("AWSClusterProvider"),
	}, nil
}

// IsNodePool determines if there is a node pool with the name or not.
func (p *AWSClusterProvider) IsNodePool(name string) bool {
	res, err := p.clusterManager.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []*string{aws.String(name)},
	})
	if err != nil {
		return false
	}

	return len(res.AutoScalingGroups) > 0
}

// GetNodePoolName returns the name of a NodePool for a specific kubernetes node.
func (p *AWSClusterProvider) GetNodePoolName(node *v1.Node) string {
	_, instanceID := p.instanceInfoFor(node)
	res, err := p.clusterManager.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{})
	if err != nil {
		return ""
	}

	for _, asg := range res.AutoScalingGroups {
		for _, instance := range asg.Instances {
			if strings.EqualFold(aws.StringValue(instance.InstanceId), instanceID) {
				return aws.StringValue(asg.AutoScalingGroupName)
			}
		}
	}

	return ""
}

// GetNodePools loads all of the provider NodePools in a cluster and returns them.
func (p *AWSClusterProvider) GetNodesFor(np NodePool) ([]*v1.Node, error) {
	awsNodeGroup, ok := np.(*AWSNodePool)
	if !ok {
		return nil, fmt.Errorf("NodePool is not from AWS")
	}

	allNodes, err := p.kubernetes.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	instanceIDs := make(map[string]bool)
	for _, instance := range awsNodeGroup.asg.Instances {
		instanceIDs[aws.StringValue(instance.InstanceId)] = true
	}

	nodes := []*v1.Node{}
	for _, n := range allNodes.Items {
		node := helper.NodePtr(n)

		_, instID := p.instanceInfoFor(node)
		if _, ok := instanceIDs[instID]; ok {
			nodes = append(nodes, node)
		}
	}

	return nodes, nil
}

// GetNodePools for AWS needs to do a bit more work to acquire a list of "correct" node pools. Since
// the asusmption is that we're working with kops, a specific region could have many autoscaling groups,
// so we'll need to refine the list down to the groups associated with kubernetes nodes.
func (p *AWSClusterProvider) GetNodePools() ([]NodePool, error) {
	nodes, err := p.kubernetes.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Build instance-id -> node mapping
	instances := make(map[string]*v1.Node)
	for _, node := range nodes.Items {
		_, id := p.instanceInfoFor(&node)
		if id == "" {
			continue
		}

		instances[id] = &node
	}

	// Lookup autoscaling groups
	res, err := p.clusterManager.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{})
	if err != nil {
		return nil, err
	}

	asgs := []*autoscaling.Group{}
	launchConfigNames := []*string{}
	vpcSubnets := []string{}

	// Filter AutoScalingGroups to ensure instances match our kubernetes nodes
	// Collect the names of LaunchConfigs for the Groups
	for _, asg := range res.AutoScalingGroups {
		var found bool = false
		for _, instance := range asg.Instances {
			instanceID := aws.StringValue(instance.InstanceId)
			if _, ok := instances[instanceID]; ok {
				found = true
				break
			}
		}

		if !found {
			continue
		}

		asgs = append(asgs, asg)
		launchConfigNames = append(launchConfigNames, asg.LaunchConfigurationName)
		vpcSubnets, _ = appendIfNotExists(vpcSubnets, aws.StringValue(asg.VPCZoneIdentifier))
	}

	// To determine specific instance types for AutoScalingGroups, we need to load
	// the specific LaunchConfigurations
	dlcResponse, err := p.clusterManager.DescribeLaunchConfigurations(&autoscaling.DescribeLaunchConfigurationsInput{
		LaunchConfigurationNames: launchConfigNames,
		MaxRecords:               aws.Int64(100),
	})
	if err != nil {
		return nil, err
	}

	templateSet := false
	configsByMachineType := make(map[string]*autoscaling.LaunchConfiguration)
	launchConfigs := make(map[string]*autoscaling.LaunchConfiguration)
	securityGroups := []*string{}

	var (
		iamProfile *string
		imageID    *string
		keyName    *string
		userData   *string
	)

	// Create a mapping from instanceType -> launch configurations,
	// and collect data required to recreate a launch configuration
	// later.
	for _, lc := range dlcResponse.LaunchConfigurations {
		lcName := aws.StringValue(lc.LaunchConfigurationName)
		launchConfigs[lcName] = lc

		// TODO: We probably need something more robust here.
		if strings.Contains(lcName, "master") {
			continue
		}

		if !templateSet {
			templateSet = true
			iamProfile = lc.IamInstanceProfile
			imageID = lc.ImageId
			keyName = lc.KeyName
			userData = lc.UserData
		}

		instanceType := aws.StringValue(lc.InstanceType)

		// Overwrite the lc if newer
		if existing, ok := configsByMachineType[instanceType]; ok {
			if lc.CreatedTime.After(*existing.CreatedTime) {
				configsByMachineType[instanceType] = lc
			}
		}

		// Add Unique SecurityGroups
		for _, lcSG := range aws.StringValueSlice(lc.SecurityGroups) {
			var foundSG bool = false
			for _, sg := range aws.StringValueSlice(securityGroups) {
				if sg == lcSG {
					foundSG = true
					break
				}
			}
			if !foundSG {
				securityGroups = append(securityGroups, &lcSG)
			}
		}
	}

	// find the image location for the id
	imageLocation := p.imageLocationFor(imageID)

	// update cached cluster data
	p.clusterData.updateLaunchConfigs(configsByMachineType)
	p.clusterData.updateSecurityGroups(securityGroups)
	p.clusterData.updateIAMInstanceProfile(iamProfile)
	p.clusterData.updateImage(imageID, imageLocation)
	p.clusterData.updateKeyName(keyName)
	p.clusterData.updateUserData(userData)
	p.clusterData.updateVPCSubnets(aws.StringSlice(vpcSubnets))

	// Create the NodePool implementations using the filtered AutoScalingGroups
	// and LaunchConfigurations
	pools := []NodePool{}

	for _, np := range asgs {
		tags := tagsToMap(np.Tags)

		launchKey := aws.StringValue(np.LaunchConfigurationName)
		launchConfig := launchConfigs[launchKey]

		pools = append(pools, &AWSNodePool{
			asg:  np,
			lc:   launchConfig,
			tags: tags,
		})
	}

	return pools, nil
}

// Creates a new NodePool for a specific parameter list using a kops driven bootstrap.
func (p *AWSClusterProvider) CreateNodePool(c context.Context, name, machineType string, nodeCount int32, diskType string, diskSizeGB int32, labels map[string]string) error {
	// Fix any optional empty parameters
	if diskType == "" {
		diskType = AWSDefaultDiskType
	}
	if diskSizeGB <= 0 {
		diskSizeGB = AWSDefaultDiskSizeGB
	}
	if labels == nil {
		labels = make(map[string]string)
	}

	launchConfig := p.clusterData.launchConfigFor(machineType)
	if launchConfig == nil {
		var err error

		// No launch config found for machine type: Create one
		// NOTE: Launch configurations don't actually contain the node count. However,
		// NOTE: we also create a kops instance group, which does contain the node count
		launchConfig, err = p.createLaunchConfiguration(name, machineType, nodeCount, diskType, diskSizeGB)
		if err != nil {
			return err
		}
	}

	if launchConfig == nil {
		return fmt.Errorf("Failed to create or locate a LaunchConfiguration for the machine type: %s", machineType)
	}

	clusterName := p.clusterData.getClusterName()
	vpcSubnets := p.clusterData.getVPCSubnets()
	size := int64(nodeCount)

	// Get the baseline kops IG labels + the labels passed in
	mergedLabels := GetKopsInstanceGroupTags(clusterName, name)
	for k, v := range labels {
		mergedLabels[k] = v
	}

	fullyQualifiedName := fmt.Sprintf("%s.%s", name, clusterName)

	// Create the AutoScalingGroup
	_, err := p.clusterManager.CreateAutoScalingGroup(&autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName:    aws.String(fullyQualifiedName),
		HealthCheckGracePeriod:  aws.Int64(0),
		LaunchConfigurationName: launchConfig.LaunchConfigurationName,
		MinSize:                 aws.Int64(size),
		MaxSize:                 aws.Int64(size),
		DesiredCapacity:         aws.Int64(size),
		Tags:                    mapToTags(mergedLabels),
		VPCZoneIdentifier:       aws.String(vpcSubnets[0]),
	})

	if err != nil {
		return err
	}

	// Wait for the nodes to be created
	return helper.WaitUntilNodesCreated(p.kubernetes, KopsInstanceGroupTag, name, int(nodeCount), 5*time.Second, 5*time.Minute)
}

// CreateAutoScalingNodePool creates a new autoscaling node pool. The semantics behind autoscaling depend on the provider.
func (p *AWSClusterProvider) CreateAutoScalingNodePool(c context.Context, name, machineType string, minNodes, nodeCount, maxNodes int32, diskType string, diskSizeGB int32, labels map[string]string) error {
	return fmt.Errorf("CreateAutoScalingNodePool is not yet supported in AWS!")
}

// Updates a node pool size to the size parameter.
func (p *AWSClusterProvider) UpdateNodePoolSize(c context.Context, nodePool NodePool, size int32) error {
	sz := int64(size)

	update := &autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(nodePool.Name()),
		MinSize:              aws.Int64(sz),
		MaxSize:              aws.Int64(sz),
		DesiredCapacity:      aws.Int64(sz),
	}

	_, err := p.clusterManager.UpdateAutoScalingGroupWithContext(c, update)
	if err != nil {
		p.log.Err("Updating AutoScalingGroup: %s", err.Error())
		return err
	}

	return nil
}

// Updates the node pool sizes for all the provided node pools to the size parameter.
func (p *AWSClusterProvider) UpdateNodePoolSizes(c context.Context, nodePools []NodePool, size int32) error {
	if len(nodePools) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(c)
	defer cancel()

	for _, np := range nodePools {
		err := p.UpdateNodePoolSize(ctx, np, size)
		if err == nil {
			break
		}
	}

	return nil
}

func (p *AWSClusterProvider) DeleteNodePool(c context.Context, nodePool NodePool) error {
	tags := nodePool.Tags()

	instanceGroup, ok := tags[KopsInstanceGroupTag]
	if !ok {
		return fmt.Errorf("Failed to locate the instance group from AutoScalingGroup tags")
	}

	clusterName, ok := tags[KopsClusterTag]
	if !ok {
		clusterName = p.clusterData.getClusterName()
	}
	if clusterName == "" {
		return fmt.Errorf("Failed to locate the cluster name from AutoScalingGroup tags")
	}

	clusterBucket := p.clusterData.getClusterBucket()
	if clusterBucket == "" {
		return fmt.Errorf("Failed to locate the cluster bucket from user data.")
	}

	awsNodePool, ok := nodePool.(*AWSNodePool)
	if !ok {
		return fmt.Errorf("NodePool is not implemented by AWSNodePool.")
	}

	if awsNodePool.lc == nil {
		return fmt.Errorf("NodePool has no reference to a launch configuration.")
	}

	launchConfigName := awsNodePool.lc.LaunchConfigurationName

	// Delete AutoscalingGroup
	_, err := p.clusterManager.DeleteAutoScalingGroup(&autoscaling.DeleteAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(nodePool.Name()),
		ForceDelete:          aws.Bool(true),
	})
	if err != nil {
		return err
	}

	// Delete Launch Configuration
	_, err = p.clusterManager.DeleteLaunchConfiguration(&autoscaling.DeleteLaunchConfigurationInput{
		LaunchConfigurationName: launchConfigName,
	})
	if err != nil {
		return err
	}

	// Delete S3 GroupInstance
	err = p.deleteInstanceGroup(clusterBucket, clusterName, instanceGroup)
	if err != nil {
		return err
	}

	return nil
}

// CreateOrUpdateTags creates or updates the tags for NodePool instances.
func (p *AWSClusterProvider) CreateOrUpdateTags(c context.Context, nodePool NodePool, updateNodes bool, tags map[string]string) error {
	ts := make([]*autoscaling.Tag, len(tags))
	index := 0
	for k, v := range tags {
		ts[index] = &autoscaling.Tag{
			ResourceId:        aws.String(nodePool.Name()),
			ResourceType:      aws.String(AutoScalingGroupResourceType),
			Key:               aws.String(k),
			Value:             aws.String(v),
			PropagateAtLaunch: aws.Bool(updateNodes),
		}
	}

	_, err := p.clusterManager.CreateOrUpdateTagsWithContext(c, &autoscaling.CreateOrUpdateTagsInput{
		Tags: ts,
	})
	if err != nil {
		return err
	}

	// Update NodePool instance
	for k, v := range tags {
		nodePool.Tags()[k] = v
	}

	return nil
}

// DeleteTags deletes the tags by key on a NodePool instance.
func (p *AWSClusterProvider) DeleteTags(c context.Context, nodePool NodePool, keys []string) error {
	tags := make([]*autoscaling.Tag, len(keys))
	for index, key := range keys {
		tags[index] = &autoscaling.Tag{
			ResourceId:   aws.String(nodePool.Name()),
			ResourceType: aws.String(AutoScalingGroupResourceType),
			Key:          aws.String(key),
		}
	}

	_, err := p.clusterManager.DeleteTagsWithContext(c, &autoscaling.DeleteTagsInput{
		Tags: tags,
	})
	if err != nil {
		return err
	}

	// Update tags on node pool instance
	for _, key := range keys {
		delete(nodePool.Tags(), key)
	}

	return nil
}

func (p *AWSClusterProvider) isLaunchConfiguration(fullyQualifiedName string) bool {
	lcs, err := p.clusterManager.DescribeLaunchConfigurations(&autoscaling.DescribeLaunchConfigurationsInput{
		LaunchConfigurationNames: aws.StringSlice([]string{fullyQualifiedName}),
	})
	return err == nil && len(lcs.LaunchConfigurations) > 0
}

func (p *AWSClusterProvider) createLaunchConfiguration(name, machineType string, nodeCount int32, diskType string, diskSizeGB int32) (*autoscaling.LaunchConfiguration, error) {
	clusterData := p.clusterData

	encoded := clusterData.getUserData(name)
	clusterBucket := clusterData.getClusterBucket()
	clusterName := clusterData.getClusterName()
	vpcSubnets := clusterData.getVPCSubnets()
	imageLocation := clusterData.getImageLocation()
	keyName := clusterData.getKeyName()
	size := int64(nodeCount)

	// Check to see if a launch configuration already exists with the name
	fullyQualifiedName := fmt.Sprintf("%s.%s", name, clusterName)
	if p.isLaunchConfiguration(fullyQualifiedName) {
		return nil, fmt.Errorf("Existing Launch Configuration Name")
	}

	// We need to use the subnet's name in the Kops InstanceGroup
	subnetResult, err := p.ec2Client.DescribeSubnets(&ec2.DescribeSubnetsInput{
		SubnetIds: aws.StringSlice(vpcSubnets),
	})

	if err != nil {
		return nil, err
	}

	subnetNames := []string{}
	for _, sn := range subnetResult.Subnets {
		m := ec2TagsToMap(sn.Tags)

		if name, ok := m["Name"]; ok {
			i := strings.Index(name, clusterName)
			if i != -1 {
				name = name[:i-1]
			}
			subnetNames = append(subnetNames, name)
		}
	}

	// Generate a Kops InstanceGroup YAML definition
	kopsYaml, err := GetKopsInstanceGroupYaml(clusterName, name, imageLocation, machineType, size, size, subnetNames)
	if err != nil {
		return nil, err
	}

	// Upload the Kops InstanceGroup to S3 bucket
	err = p.uploadInstanceGroup(kopsYaml, clusterBucket, clusterName, name)
	if err != nil {
		return nil, err
	}

	// Create Launch Configuration Request Input...
	createLCRequest := &autoscaling.CreateLaunchConfigurationInput{
		AssociatePublicIpAddress: aws.Bool(true),
		BlockDeviceMappings:      GetAWSDefaultBlockDeviceMappings(diskType, int64(diskSizeGB)),
		IamInstanceProfile:       aws.String(clusterData.getIAMInstanceProfile()),
		ImageId:                  aws.String(clusterData.getImageID()),
		InstanceMonitoring:       GetAWSInstanceMonitoringDefaults(),
		InstanceType:             aws.String(machineType),
		LaunchConfigurationName:  aws.String(fullyQualifiedName),
		SecurityGroups:           aws.StringSlice(clusterData.getSecurityGroups()),
		UserData:                 aws.String(encoded),
		KeyName:                  aws.String(keyName),
	}

	_, err = p.clusterManager.CreateLaunchConfiguration(createLCRequest)
	if err != nil {
		return nil, err
	}

	// Retrieve the full LC definition to return in a retry loop
	for retries := 0; retries < 5; retries++ {
		lcs, err := p.clusterManager.DescribeLaunchConfigurations(&autoscaling.DescribeLaunchConfigurationsInput{
			LaunchConfigurationNames: aws.StringSlice([]string{fullyQualifiedName}),
		})
		if err != nil || len(lcs.LaunchConfigurations) == 0 {
			if err != nil {
				p.log.Warn("Error: %s", err.Error())
			}
			p.log.Warn("Failed to retrieve newly created LaunchConfiguration. Retrying...")

			time.Sleep(5 * time.Second)
			continue
		}

		return lcs.LaunchConfigurations[0], nil
	}

	return nil, fmt.Errorf("Failed to retrieve newly created launch configuration: %s", fullyQualifiedName)
}

// Uploads the InstanceGroup yaml to S3 using the bucket and cluster name located in the launch
// configurations user data.
func (p *AWSClusterProvider) uploadInstanceGroup(yaml string, clusterBucket string, clusterName string, groupName string) error {
	buffer := []byte(yaml)
	key := fmt.Sprintf("/%s/instancegroup/%s", clusterName, groupName)

	// Config settings: this is where you choose the bucket, filename, content-type etc.
	// of the file you're uploading.
	_, err := p.s3Client.PutObject(&s3.PutObjectInput{
		Bucket:               aws.String(clusterBucket),
		Key:                  aws.String(key),
		ACL:                  aws.String("private"),
		Body:                 bytes.NewReader(buffer),
		ContentLength:        aws.Int64(int64(len(buffer))),
		ContentType:          aws.String("binary/octet-stream"),
		ServerSideEncryption: aws.String("AES256"),
	})

	return err
}

// Uploads the InstanceGroup yaml to S3 using the bucket and cluster name located in the launch
// configurations user data.
func (p *AWSClusterProvider) deleteInstanceGroup(clusterBucket string, clusterName string, groupName string) error {
	key := fmt.Sprintf("/%s/instancegroup/%s", clusterName, groupName)

	// Delete the S3 object for instance group
	_, err := p.s3Client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(clusterBucket),
		Key:    aws.String(key),
	})

	return err
}

// gets the image location for the AMI id
func (p *AWSClusterProvider) imageLocationFor(imageID *string) *string {
	di, err := p.ec2Client.DescribeImages(&ec2.DescribeImagesInput{
		ImageIds: []*string{imageID},
	})
	if err != nil || len(di.Images) == 0 {
		p.log.Err("Image Location: %s", err.Error())
		return nil
	}

	imageLoc := aws.StringValue(di.Images[0].ImageLocation)
	if strings.Contains(imageLoc, "/") {
		locSplit := strings.Split(imageLoc, "/")
		loc := imageLoc[len(locSplit[0])+1:]
		imageLoc = fmt.Sprintf("kope.io/%s", loc)
	}

	return aws.String(imageLoc)
}

// Pulls the instance id and zone from the Node.Spec.ProviderID
func (p *AWSClusterProvider) instanceInfoFor(node *v1.Node) (zone string, instanceID string) {
	id := node.Spec.ProviderID

	splitted := strings.Split(id[7:], "/")
	return splitted[0], splitted[1]
}

// attempts to load the AWS access key from secret
func loadAWSAccessKey(keyFile string) (*AWSAccessKeyFile, error) {
	if !file.FileExists(keyFile) {
		return nil, fmt.Errorf("Failed to locate service account file: %s", keyFile)
	}

	result, err := ioutil.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}

	var ak AWSAccessKeyFile
	err = json.Unmarshal(result, &ak)
	if err != nil {
		return nil, err
	}

	if ak.AccessKeyID == "" || ak.SecretAccessKey == "" {
		return nil, fmt.Errorf("Failed to populate access from service key. Empty values.")
	}

	return &ak, nil
}

func newAWSClusterManager(region string) (*autoscaling.AutoScaling, *ec2.EC2, *s3.S3, error) {
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

	clusterManager := autoscaling.New(session.New(c))
	ec2Client := ec2.New(session.New(c))
	s3Client := s3.New(session.New(c))

	return clusterManager, ec2Client, s3Client, nil
}

func tagsToMap(tags []*autoscaling.TagDescription) map[string]string {
	m := make(map[string]string)
	for _, tag := range tags {
		if tag == nil {
			continue
		}

		m[aws.StringValue(tag.Key)] = aws.StringValue(tag.Value)
	}

	return m
}

func mapToTags(m map[string]string) []*autoscaling.Tag {
	tags := []*autoscaling.Tag{}

	for k, v := range m {
		tags = append(tags, &autoscaling.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	return tags
}

func ec2TagsToMap(tags []*ec2.Tag) map[string]string {
	m := make(map[string]string)
	for _, tag := range tags {
		if tag == nil {
			continue
		}

		m[aws.StringValue(tag.Key)] = aws.StringValue(tag.Value)
	}

	return m
}

func flatRange(min, max, count int32) *string {
	return aws.String(fmt.Sprintf("%d/%d/%d", min, max, count))
}

func expandRange(s string) (int64, int64, int64) {
	log := logging.NamedLogger("AWSClusterProvider")
	values := strings.Split(s, "/")

	count, err := strconv.Atoi(values[2])
	if err != nil {
		log.Err("Parsing Count: %s", err.Error())
		return -1, -1, -1
	}

	min, err := strconv.Atoi(values[0])
	if err != nil {
		log.Err("Parsing Min: %s", err.Error())
		min = count
	}

	max, err := strconv.Atoi(values[1])
	if err != nil {
		log.Err("Parsing Max: %s", err.Error())
		max = count
	}

	return int64(min), int64(max), int64(count)
}

func findAWSRegion(c kubernetes.Interface) string {
	// Locate AWS region -- TODO: Use metadata?
	log := logging.NamedLogger("AWSClusterProvider")
	nodes, err := c.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		log.Err("Failed to locate AWS Region: %s", err.Error())
		return ""
	}

	if len(nodes.Items) == 0 {
		log.Err("Failed to locate any kubernetes nodes.")
		return ""
	}

	id := nodes.Items[0].Spec.ProviderID

	splitted := strings.Split(id[7:], "/")
	zone := splitted[0]
	return zone[:len(zone)-1]
}

// Appends a string to a slice if it doesn't already exist
func appendIfNotExists(ss []string, ele string) ([]string, bool) {
	for _, s := range ss {
		if s == ele {
			return ss, false
		}
	}

	return append(ss, ele), true
}
