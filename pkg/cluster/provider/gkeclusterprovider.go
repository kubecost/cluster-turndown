package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kubecost/cluster-turndown/pkg/async"
	"github.com/kubecost/cluster-turndown/pkg/cluster/helper"
	"github.com/kubecost/cluster-turndown/pkg/file"
	"github.com/kubecost/cluster-turndown/pkg/logging"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	container "google.golang.org/genproto/googleapis/container/v1"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	gke "cloud.google.com/go/container/apiv1"
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
type GKEClusterProvider struct {
	kubernetes     kubernetes.Interface
	clusterManager *gke.ClusterManagerClient
	metadata       *GKEMetaData
	log            logging.NamedLogger
}

// NewGKEClusterProvider creates a new GKEClusterProvider instance as the ClusterProvider
func NewGKEClusterProvider(kubernetes kubernetes.Interface) (ClusterProvider, error) {
	clusterManager, err := newGKEClusterManager()
	if err != nil {
		return nil, err
	}

	return &GKEClusterProvider{
		kubernetes:     kubernetes,
		clusterManager: clusterManager,
		metadata:       NewGKEMetaData(),
		log:            logging.NamedLogger("GKEClusterProvider"),
	}, nil
}

// IsNodePool determines if there is a node pool with the name or not.
func (p *GKEClusterProvider) IsNodePool(name string) bool {
	req := &container.GetNodePoolRequest{Name: p.toNodePoolResourceByName(name)}
	resp, err := p.clusterManager.GetNodePool(context.TODO(), req)
	if err != nil {
		return false
	}

	return resp != nil
}

// GetNodePoolName returns the name of a NodePool for a specific kubernetes node.
func (p *GKEClusterProvider) GetNodePoolName(node *v1.Node) string {
	_, _, pool := p.projectInfoFor(node)
	return pool
}

// GetNodesFor returns a slice of kubernetes Node instances for the NodePool instance
// provided.
func (p *GKEClusterProvider) GetNodesFor(np NodePool) ([]*v1.Node, error) {
	name := np.Name()

	allNodes, err := p.kubernetes.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	nodes := []*v1.Node{}
	for _, n := range allNodes.Items {
		node := helper.NodePtr(n)

		_, _, nodePool := p.projectInfoFor(node)
		if strings.EqualFold(nodePool, name) {
			nodes = append(nodes, node)
		}
	}

	return nodes, nil
}

// GetNodePools loads all of the provider NodePools in a cluster and returns them.
func (p *GKEClusterProvider) GetNodePools() ([]NodePool, error) {
	ctx := context.TODO()

	projectID := p.metadata.GetProjectID()
	zone := p.metadata.GetMasterZone()
	cluster := p.metadata.GetClusterID()

	req := &container.ListNodePoolsRequest{Parent: p.getClusterResourcePath()}
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

// CreateNodePool creates a new node pool with the provided specs.
func (p *GKEClusterProvider) CreateNodePool(c context.Context, name, machineType string, nodeCount int32, diskType string, diskSizeGB int32, labels map[string]string) error {
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
		Parent: p.getClusterResourcePath(),
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
			p.log.Log("Created NodePool Successfully: %s. Waiting for nodes to become available...", name)
			break
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

	return helper.WaitUntilNodesCreated(p.kubernetes, LabelGKENodePool, name, int(nodeCount), 5*time.Second, 20*time.Minute)
}

// CreateAutoScalingNodePool creates a new autoscaling node pool. The semantics behind autoscaling depend on the provider.
func (p *GKEClusterProvider) CreateAutoScalingNodePool(c context.Context, name, machineType string, minNodes, nodeCount, maxNodes int32, diskType string, diskSizeGB int32, labels map[string]string) error {
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
		Parent: p.getClusterResourcePath(),
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
			p.log.Log("Created NodePool Successfully: %s. Waiting for nodes to become available...", name)
			break
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

	// wait for at least a single node
	return helper.WaitUntilNodesCreated(p.kubernetes, LabelGKENodePool, name, 1, 5*time.Second, 20*time.Minute)
}

// UpdateNodePoolSize updates the number of nodes in a NodePool
func (p *GKEClusterProvider) UpdateNodePoolSize(c context.Context, nodePool NodePool, size int32) error {
	if nodePool == nil {
		return fmt.Errorf("Provided nodePool was nil.")
	}

	request := &container.SetNodePoolSizeRequest{
		Name:      p.toNodePoolResource(nodePool),
		NodeCount: size,
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

// UpdateNodePoolSizes updates the number of nodes in multiple NodePool instances.
func (p *GKEClusterProvider) UpdateNodePoolSizes(c context.Context, nodePools []NodePool, size int32) error {
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
func (p *GKEClusterProvider) DeleteNodePool(c context.Context, nodePool NodePool) error {
	if nodePool == nil {
		return fmt.Errorf("Provided nodePool was nil.")
	}

	request := &container.DeleteNodePoolRequest{
		Name: p.toNodePoolResource(nodePool),
	}

	ctx, cancel := context.WithCancel(c)

	defer cancel()

	for {
		_, err := p.clusterManager.DeleteNodePool(ctx, request)
		if err == nil {
			p.log.Log("Deleted NodePool Successfully: %s", nodePool.Name())
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
			return fmt.Errorf("NodePool Deletion Cancelled")
		}
	}
}

// CreateOrUpdateTags creates or updates the tags for NodePool instances.
func (p *GKEClusterProvider) CreateOrUpdateTags(c context.Context, nodePool NodePool, updateNodes bool, tags map[string]string) error {
	// NOTE: In GKE, it's not possible to update the node pool labels. This is because it propagates the labels
	// NOTE: via kubelet, which are set at creation. We could update all of the nodes, but that doesn't seem
	// NOTE: quite right, as any new nodes will not include the labels. Depending on how important this
	// NOTE: specific functionality is, we may have to recreate a NodePool, which currently seems very wasteful.
	return fmt.Errorf("GKE does not support modifying labels after a node pool has been created.")
}

// DeleteTags deletes the tags by key on a NodePool instance.
func (p *GKEClusterProvider) DeleteTags(c context.Context, nodePool NodePool, keys []string) error {
	// NOTE: In GKE, it's not possible to update the node pool labels. This is because it propagates the labels
	// NOTE: via kubelet, which are set at creation. We could update all of the nodes, but that doesn't seem
	// NOTE: quite right, as any new nodes will not include the labels. Depending on how important this
	// NOTE: specific functionality is, we may have to recreate a NodePool, which currently seems very wasteful.
	return fmt.Errorf("GKE does not support modifying labels after a node pool has been created.")
}

func (p *GKEClusterProvider) projectInfoFor(node *v1.Node) (project string, zone string, nodePool string) {
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

// gets the fully qualified resource path for the node pool
func (p *GKEClusterProvider) toNodePoolResourceByName(name string) string {
	return fmt.Sprintf("projects/%s/locations/%s/clusters/%s/nodePools/%s",
		p.metadata.GetProjectID(),
		p.metadata.GetMasterZone(),
		p.metadata.GetClusterID(),
		name)
}

// gets the fully qualified resource path for the node pool
func (p *GKEClusterProvider) toNodePoolResource(nodePool NodePool) string {
	return fmt.Sprintf("projects/%s/locations/%s/clusters/%s/nodePools/%s",
		nodePool.Project(), nodePool.Zone(), nodePool.ClusterID(), nodePool.Name())
}

// gets the fully qualified resource path for the cluster
func (p *GKEClusterProvider) getClusterResourcePath() string {
	return fmt.Sprintf("projects/%s/locations/%s/clusters/%s",
		p.metadata.GetProjectID(), p.metadata.GetMasterZone(), p.metadata.GetClusterID())
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
