package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kubecost/cluster-turndown/pkg/async"
	"github.com/kubecost/cluster-turndown/pkg/file"
	"github.com/kubecost/cluster-turndown/pkg/logging"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	container "google.golang.org/genproto/googleapis/container/v1"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	gke "cloud.google.com/go/container/apiv1"

	"k8s.io/klog"
)

const (
	LabelGKENodePool      = "cloud.google.com/gke-nodepool"
	GKECredsEnvVar        = "GOOGLE_APPLICATION_CREDENTIALS"
	GKEAuthServiceAccount = "/var/keys/service-key.json"

	GKEDefaultMachineType = "n1-standard-1"
	GKEDefaultDiskType    = "pd-standard"
	GKEDefaultDiskSizeGB  = 100
)

//--------------------------------------------------------------------------
//  Default GKE Values
//--------------------------------------------------------------------------

// GetGKEDefaultOAuthScopes returns the default oauth scopes used when creating
// a new node pool
func GetGKEDefaultOAuthScopes() []string {
	return []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/devstorage.read_only",
		"https://www.googleapis.com/auth/logging.write",
		"https://www.googleapis.com/auth/monitoring",
		"https://www.googleapis.com/auth/servicecontrol",
		"https://www.googleapis.com/auth/service.management.readonly",
		"https://www.googleapis.com/auth/trace.append",
	}
}

// GetGKEDefaultMetadata returns the default metadata used when creating
// a new node pool
func GetGKEDefaultMetadata() map[string]string {
	return map[string]string{
		"disable-legacy-endpoints": "true",
	}
}

// GetGKEDefaultNodeManagement returns the default nod management used when
// creating a new node pool
func GetGKEDefaultNodeManagement() *container.NodeManagement {
	return &container.NodeManagement{
		AutoUpgrade: true,
		AutoRepair:  true,
	}
}

//--------------------------------------------------------------------------
//  GKE NodePool Implementation
//--------------------------------------------------------------------------

// NodePool contains a node pool identifier and the initial number of nodes
// in the pool
type GKENodePool struct {
	name        string
	project     string
	zone        string
	clusterID   string
	min         int32
	max         int32
	count       int32
	autoscaling bool
	machineType string
	tags        map[string]string
}

func (np *GKENodePool) Name() string            { return np.name }
func (np *GKENodePool) Project() string         { return np.project }
func (np *GKENodePool) Zone() string            { return np.zone }
func (np *GKENodePool) ClusterID() string       { return np.clusterID }
func (np *GKENodePool) MinNodes() int32         { return np.min }
func (np *GKENodePool) MaxNodes() int32         { return np.max }
func (np *GKENodePool) NodeCount() int32        { return np.count }
func (np *GKENodePool) AutoScaling() bool       { return np.autoscaling }
func (np *GKENodePool) MachineType() string     { return np.machineType }
func (np *GKENodePool) Tags() map[string]string { return np.tags }
func (np *GKENodePool) IsMaster() bool          { return false }

//--------------------------------------------------------------------------
//  GKE ClusterProvider Implementation
//--------------------------------------------------------------------------

// ClusterProvider implementation for GKE
type GKEProvider struct {
	kubernetes     kubernetes.Interface
	clusterManager *gke.ClusterManagerClient
	metadata       *GKEMetaData
	log            logging.NamedLogger
}

// NewGKEProvider creates a new GKEProvider instance as the ClusterProvider
func NewGKEProvider(kubernetes kubernetes.Interface) ClusterProvider {
	clusterManager, err := newGKEClusterManager()
	if err != nil {
		klog.V(1).Infof("Failed to load service account.")
	}

	return &GKEProvider{
		kubernetes:     kubernetes,
		clusterManager: clusterManager,
		metadata:       NewGKEMetaData(),
		log:            logging.NamedLogger("GKEProvider"),
	}
}

func (p *GKEProvider) GetNodesFor(np NodePool) ([]*v1.Node, error) {
	name := np.Name()

	allNodes, err := p.kubernetes.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	nodes := []*v1.Node{}
	for _, n := range allNodes.Items {
		_, _, nodePool := p.projectInfoFor(&n)
		if strings.EqualFold(nodePool, name) {
			nodes = append(nodes, &n)
		}
	}

	return nodes, nil
}

// GetNodePools returns all of the node pools for the cluster provider.
func (p *GKEProvider) GetNodePools() ([]NodePool, error) {
	ctx := context.TODO()

	projectID := p.metadata.GetProjectID()
	zone := p.metadata.GetMasterZone()
	cluster := p.metadata.GetClusterID()

	req := &container.ListNodePoolsRequest{
		ProjectId: projectID,
		Zone:      zone,
		ClusterId: cluster,
	}
	p.log.Log("Loading node pools for: [ProjectID: %s, Zone: %s, ClusterID: %s]", projectID, zone, cluster)

	resp, err := p.clusterManager.ListNodePools(ctx, req)
	if err != nil {
		return nil, err
	}

	pools := []NodePool{}

	for _, np := range resp.GetNodePools() {
		nodeCount := np.GetInitialNodeCount()
		autoscaling := np.Autoscaling.GetEnabled()

		var min int32 = nodeCount
		var max int32 = nodeCount
		if autoscaling {
			min = np.Autoscaling.GetMinNodeCount()
			max = np.Autoscaling.GetMaxNodeCount()
		}

		tags := np.GetConfig().GetLabels()
		if tags == nil {
			tags = make(map[string]string)
		}

		pools = append(pools, &GKENodePool{
			name:        np.GetName(),
			project:     projectID,
			clusterID:   cluster,
			zone:        zone,
			min:         min,
			max:         max,
			count:       nodeCount,
			autoscaling: autoscaling,
			machineType: np.GetConfig().GetMachineType(),
			tags:        tags,
		})
	}

	return pools, nil
}

func (p *GKEProvider) CreateNodePool(c context.Context, name, machineType string, nodeCount int32, diskType string, diskSizeGB int32, labels map[string]string) error {
	// Fix any optional empty parameters
	if diskType == "" {
		diskType = GKEDefaultDiskType
	}
	if diskSizeGB <= 0 {
		diskSizeGB = GKEDefaultDiskSizeGB
	}
	if labels == nil {
		labels = make(map[string]string)
	}

	// Create the request, fill in necessary defaults
	request := &container.CreateNodePoolRequest{
		ProjectId: p.metadata.GetProjectID(),
		ClusterId: p.metadata.GetClusterID(),
		Zone:      p.metadata.GetMasterZone(),
		NodePool: &container.NodePool{
			Name: name,
			Config: &container.NodeConfig{
				MachineType: machineType,
				DiskType:    diskType,
				DiskSizeGb:  diskSizeGB,
				Labels:      labels,
				OauthScopes: GetGKEDefaultOAuthScopes(),
				Metadata:    GetGKEDefaultMetadata(),
			},
			InitialNodeCount: nodeCount,
			Management:       GetGKEDefaultNodeManagement(),
		},
	}

	ctx, cancel := context.WithCancel(c)

	defer cancel()

	for {
		_, err := p.clusterManager.CreateNodePool(ctx, request)
		if err == nil {
			p.log.Log("Created NodePool Successfully: %s", request.NodePool.Name)
			return nil
		}

		// If the error represents a temporary state where we can retry, log and continue
		if isRetriableError(err) {
			p.log.Log("NodePool operation already in queue, retrying...")
		} else {
			return err
		}

		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return fmt.Errorf("NodePool Creation Cancelled")
		}
	}
}

func (p *GKEProvider) CreateAutoScalingNodePool(c context.Context, name, machineType string, minNodes, nodeCount, maxNodes int32, diskType string, diskSizeGB int32, labels map[string]string) error {
	// Fix any optional empty parameters
	if diskType == "" {
		diskType = GKEDefaultDiskType
	}
	if diskSizeGB <= 0 {
		diskSizeGB = GKEDefaultDiskSizeGB
	}
	if labels == nil {
		labels = make(map[string]string)
	}

	// Create the request, fill in necessary defaults
	request := &container.CreateNodePoolRequest{
		ProjectId: p.metadata.GetProjectID(),
		ClusterId: p.metadata.GetClusterID(),
		Zone:      p.metadata.GetMasterZone(),
		NodePool: &container.NodePool{
			Name: name,
			Config: &container.NodeConfig{
				MachineType: machineType,
				DiskType:    diskType,
				DiskSizeGb:  diskSizeGB,
				Labels:      labels,
				OauthScopes: GetGKEDefaultOAuthScopes(),
				Metadata:    GetGKEDefaultMetadata(),
			},
			InitialNodeCount: nodeCount,
			Management:       GetGKEDefaultNodeManagement(),
			Autoscaling: &container.NodePoolAutoscaling{
				Enabled:      true,
				MinNodeCount: minNodes,
				MaxNodeCount: maxNodes,
			},
		},
	}

	ctx, cancel := context.WithCancel(c)

	defer cancel()

	for {
		_, err := p.clusterManager.CreateNodePool(ctx, request)
		if err == nil {
			p.log.Log("Created NodePool Successfully: %s", request.NodePool.Name)
			return nil
		}

		// If the error represents a temporary state where we can retry, log and continue
		if isRetriableError(err) {
			p.log.Log("NodePool operation already in queue, retrying...")
		} else {
			return err
		}

		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return fmt.Errorf("NodePool Creation Cancelled")
		}
	}
}

func (p *GKEProvider) UpdateNodePoolSize(c context.Context, nodePool NodePool, size int32) error {
	if nodePool == nil {
		return fmt.Errorf("Provided nodePool was nil.")
	}

	request := &container.SetNodePoolSizeRequest{
		ProjectId:  nodePool.Project(),
		ClusterId:  nodePool.ClusterID(),
		Zone:       nodePool.Zone(),
		NodePoolId: nodePool.Name(),
		NodeCount:  size,
	}

	ctx, cancel := context.WithCancel(c)

	defer cancel()

	for {
		_, err := p.clusterManager.SetNodePoolSize(ctx, request)
		if err == nil {
			p.log.Log("Resized NodePool Successfully: %s", request.NodePoolId)
			return nil
		}

		// If the error represents a temporary state where we can retry, log and continue
		if isRetriableError(err) {
			p.log.Log("NodePool operation already in queue, retrying...")
		} else {
			return err
		}

		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return fmt.Errorf("NodePool Resize Cancelled")
		}
	}
}

func (p *GKEProvider) UpdateNodePoolSizes(c context.Context, nodePools []NodePool, size int32) error {
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

func (p *GKEProvider) DeleteNodePool(c context.Context, nodePool NodePool) error {
	if nodePool == nil {
		return fmt.Errorf("Provided nodePool was nil.")
	}

	request := &container.DeleteNodePoolRequest{
		ProjectId:  nodePool.Project(),
		ClusterId:  nodePool.ClusterID(),
		Zone:       nodePool.Zone(),
		NodePoolId: nodePool.Name(),
	}

	ctx, cancel := context.WithCancel(c)

	defer cancel()

	for {
		_, err := p.clusterManager.DeleteNodePool(ctx, request)
		if err == nil {
			p.log.Log("Deleted NodePool Successfully: %s", request.NodePoolId)
			return nil
		}

		// If the error represents a temporary state where we can retry, log and continue
		if isRetriableError(err) {
			p.log.Log("NodePool operation already in queue, retrying...")
		} else {
			return err
		}

		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return fmt.Errorf("NodePool Resize Cancelled")
		}
	}
}

func (p *GKEProvider) projectInfoFor(node *v1.Node) (project string, zone string, nodePool string) {
	nodeProviderID := node.Spec.ProviderID[6:]
	props := strings.Split(nodeProviderID, "/")

	nodePool = node.Labels[LabelGKENodePool]

	if len(props) < 2 {
		project = ""
		zone = ""
		return
	}

	project = props[0]
	zone = props[1]
	return
}

// Creates a new GKE based cluster manager API to execute GRPC commands
func newGKEClusterManager() (*gke.ClusterManagerClient, error) {
	if !file.FileExists(GKEAuthServiceAccount) {
		return nil, fmt.Errorf("Failed to located service account file: %s", GKEAuthServiceAccount)
	}

	ctx := context.Background()

	clusterManager, err := gke.NewClusterManagerClient(ctx)
	if err != nil {
		return nil, err
	}

	return clusterManager, nil
}

// isRetriableError returns true if the error is a Status error and it's error
// code matches a retriable error code
func isRetriableError(err error) bool {
	s, ok := status.FromError(err)
	return ok && isStatusCode(s, codes.FailedPrecondition, codes.Unavailable, codes.DeadlineExceeded)
}

// isStatusCode tests a Status against multiple codes and returns true
// if any match
func isStatusCode(s *status.Status, codes ...codes.Code) bool {
	c := s.Code()

	for _, code := range codes {
		if c == code {
			return true
		}
	}

	return false
}
