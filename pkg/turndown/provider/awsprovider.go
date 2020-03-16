package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	"github.com/kubecost/cluster-turndown/pkg/file"
	"github.com/kubecost/cluster-turndown/pkg/logging"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
)

const (
	AWSAccessKey                  = "/var/keys/service-key.json"
	AWSClusterIDTagKey            = "KubernetesCluster"
	AWSGroupNameTagKey            = "aws:autoscaling:groupName"
	AWSRoleMasterTagKey           = "k8s.io/role/master"
	AWSRoleNodeTagKey             = "k8s.io/role/node"
	AWSNodeGroupPreviousKey       = "kubecost.turndown.previous"
	AutoScalingGroupResourceType  = "auto-scaling-group"
	placeholderInstanceNamePrefix = "i-placeholder"
)

// AWS NodePool based on AutoScalingGroup
type AWSNodePool struct {
	asg  *autoscaling.Group
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
func (np *AWSNodePool) Tags() map[string]string { return np.tags }

type AccessKey struct {
	AccessKeyID     string `json:"aws_access_key_id"`
	SecretAccessKey string `json:"aws_secret_access_key"`
}

// ComputeProvider for AWS
type AWSProvider struct {
	kubernetes     kubernetes.Interface
	clusterManager *autoscaling.AutoScaling
	log            logging.NamedLogger
}

func NewAWSProvider(kubernetes kubernetes.Interface) ComputeProvider {
	region := findAWSRegion(kubernetes)
	clusterManager, err := newAWSClusterManager(region)
	if err != nil {
		klog.V(1).Infof("Failed to load service account.")
	}

	return &AWSProvider{
		kubernetes:     kubernetes,
		clusterManager: clusterManager,
		log:            logging.NamedLogger("AWSProvider"),
	}
}

func (p *AWSProvider) IsServiceAccountKey() bool {
	return file.FileExists(AWSAccessKey)
}

func (p *AWSProvider) IsTurndownNodePool() bool {
	res, err := p.clusterManager.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []*string{aws.String("kubecost-turndown")},
	})
	if err != nil {
		return false
	}

	return len(res.AutoScalingGroups) > 0
}

func (p *AWSProvider) CreateSingletonNodePool() error {
	return errors.New("Creating a Singleton Node not supported on AWS!")
}

func (p *AWSProvider) GetPoolID(node *v1.Node) string {
	_, instanceID := p.instanceInfoFor(node)
	res, err := p.clusterManager.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{})
	if err != nil {
		return ""
	}

	for _, asg := range res.AutoScalingGroups {
		for _, instance := range asg.Instances {
			if aws.StringValue(instance.InstanceId) == instanceID {
				return aws.StringValue(asg.AutoScalingGroupName)
			}
		}
	}

	return ""
}

func (p *AWSProvider) GetNodePools() ([]NodePool, error) {
	res, err := p.clusterManager.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{})
	if err != nil {
		return nil, err
	}

	pools := []NodePool{}

	for _, np := range res.AutoScalingGroups {
		tags := tagsToMap(np.Tags)

		pools = append(pools, &AWSNodePool{
			asg:  np,
			tags: tags,
		})
	}

	return pools, nil
}

func (p *AWSProvider) SetNodePoolSizes(nodePools []NodePool, size int32) error {
	if len(nodePools) == 0 {
		return nil
	}

	sz := int64(size)

	for _, np := range nodePools {
		min, max, count := np.MinNodes(), np.MaxNodes(), np.NodeCount()

		update := &autoscaling.UpdateAutoScalingGroupInput{
			AutoScalingGroupName: aws.String(np.Name()),
			MinSize:              aws.Int64(sz),
			MaxSize:              aws.Int64(sz),
			DesiredCapacity:      aws.Int64(sz),
		}

		_, err := p.clusterManager.UpdateAutoScalingGroup(update)
		if err != nil {
			p.log.Err("Updating AutoScalingGroup: %s", err.Error())
			return err
		}

		nodeRange := flatRange(min, max, count)
		tagsIn := &autoscaling.CreateOrUpdateTagsInput{
			Tags: []*autoscaling.Tag{
				&autoscaling.Tag{
					ResourceId:        aws.String(np.Name()),
					ResourceType:      aws.String(AutoScalingGroupResourceType),
					Key:               aws.String(AWSNodeGroupPreviousKey),
					Value:             nodeRange,
					PropagateAtLaunch: aws.Bool(false),
				},
			},
		}

		_, err = p.clusterManager.CreateOrUpdateTags(tagsIn)
		if err != nil {
			p.log.Err("Creating or Updating Tags: %s", err.Error())

			return err
		}

		np.Tags()[AWSNodeGroupPreviousKey] = aws.StringValue(nodeRange)
	}

	return nil
}

func (p *AWSProvider) ResetNodePoolSizes(nodePools []NodePool) error {
	if len(nodePools) == 0 {
		return nil
	}

	for _, np := range nodePools {
		tags := np.Tags()
		rangeTag, ok := tags[AWSNodeGroupPreviousKey]
		if !ok {
			p.log.Err("Failed to locate tag: %s for NodePool: %s", AWSNodeGroupPreviousKey, np.Name())
			continue
		}

		min, max, count := expandRange(rangeTag)
		if count < 0 {
			p.log.Err("Failed to parse range used to resize node pool.")
			continue
		}

		update := &autoscaling.UpdateAutoScalingGroupInput{
			AutoScalingGroupName: aws.String(np.Name()),
			MinSize:              aws.Int64(min),
			MaxSize:              aws.Int64(max),
			DesiredCapacity:      aws.Int64(count),
		}

		_, err := p.clusterManager.UpdateAutoScalingGroup(update)
		if err != nil {
			p.log.Err("Updating AutoScalingGroup: %s", err.Error())
			return err
		}

		deleteTagsIn := &autoscaling.DeleteTagsInput{
			Tags: []*autoscaling.Tag{
				&autoscaling.Tag{
					ResourceId:   aws.String(np.Name()),
					ResourceType: aws.String(AutoScalingGroupResourceType),
					Key:          aws.String(AWSNodeGroupPreviousKey),
				},
			},
		}

		_, err = p.clusterManager.DeleteTags(deleteTagsIn)
		if err != nil {
			p.log.Err("Deleting Tags: %s", err.Error())

			return err
		}

		delete(tags, AWSNodeGroupPreviousKey)
	}

	return nil
}

// Pulls the instance id and zone from the Node.Spec.ProviderID
func (p *AWSProvider) instanceInfoFor(node *v1.Node) (zone string, instanceID string) {
	id := node.Spec.ProviderID

	splitted := strings.Split(id[7:], "/")
	return splitted[0], splitted[1]
}

func newAWSClusterManager(region string) (*autoscaling.AutoScaling, error) {
	if !file.FileExists(AWSAccessKey) {
		return nil, fmt.Errorf("Failed to locate service account file: %s", AWSAccessKey)
	}

	result, err := ioutil.ReadFile(AWSAccessKey)
	if err != nil {
		return nil, err
	}

	var ak AccessKey
	err = json.Unmarshal(result, &ak)
	if err != nil {
		return nil, err
	}

	os.Setenv("AWS_ACCESS_KEY_ID", ak.AccessKeyID)
	os.Setenv("AWS_SECRET_ACCESS_KEY", ak.SecretAccessKey)

	c := aws.NewConfig().
		WithCredentials(credentials.NewEnvCredentials()).
		WithRegion(region)
	clusterManager := autoscaling.New(session.New(c))

	return clusterManager, nil
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

func flatRange(min, max, count int32) *string {
	return aws.String(fmt.Sprintf("%d/%d/%d", min, max, count))
}

func expandRange(s string) (int64, int64, int64) {
	log := logging.NamedLogger("AWSProvider")
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
	log := logging.NamedLogger("AWSProvider")
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
