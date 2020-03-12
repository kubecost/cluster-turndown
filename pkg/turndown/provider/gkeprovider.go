package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kubecost/kubecost-turndown/pkg/async"
	"github.com/kubecost/kubecost-turndown/pkg/file"
	"github.com/kubecost/kubecost-turndown/pkg/logging"

	gax "github.com/googleapis/gax-go/v2"
	container "google.golang.org/genproto/googleapis/container/v1"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	gke "cloud.google.com/go/container/apiv1"

	"k8s.io/klog"
)

const (
	LabelGKENodePool      = "cloud.google.com/gke-nodepool"
	GKECredsEnvVar        = "GOOGLE_APPLICATION_CREDENTIALS"
	GKEAuthServiceAccount = "/var/keys/service-key.json"
	GKETurndownPoolName   = "kubecost-turndown"
)

var (
	options []gax.CallOption = []gax.CallOption{
		//gax.WithGRPCOptions(grpc.WaitForReady(true)),
	}
)

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
func (np *GKENodePool) Tags() map[string]string { return np.tags }

// ComputeProvider for GKE
type GKEProvider struct {
	kubernetes     kubernetes.Interface
	clusterManager *gke.ClusterManagerClient
	metadata       *GKEMetaData
	log            logging.NamedLogger
}

func NewGKEProvider(kubernetes kubernetes.Interface) ComputeProvider {
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

func (p *GKEProvider) IsServiceAccountKey() bool {
	return file.FileExists(GKEAuthServiceAccount)
}

func (p *GKEProvider) IsTurndownNodePool() bool {
	ctx := context.TODO()

	req := &container.GetNodePoolRequest{
		ProjectId:  p.metadata.GetProjectID(),
		ClusterId:  p.metadata.GetClusterID(),
		Zone:       p.metadata.GetZone(),
		NodePoolId: GKETurndownPoolName,
	}

	resp, err := p.clusterManager.GetNodePool(ctx, req)
	if err != nil {
		return false
	}

	return resp.GetInitialNodeCount() == 1
}

func (p *GKEProvider) CreateSingletonNodePool() error {
	ctx := context.TODO()

	nodePool := &container.NodePool{
		Name: GKETurndownPoolName,
		Config: &container.NodeConfig{
			MachineType: "g1-small",
			DiskSizeGb:  10,
			Labels: map[string]string{
				TurndownNodeLabel: "true",
			},
			OauthScopes: []string{
				"https://www.googleapis.com/auth/cloud-platform",
				"https://www.googleapis.com/auth/devstorage.read_only",
				"https://www.googleapis.com/auth/logging.write",
				"https://www.googleapis.com/auth/monitoring",
				"https://www.googleapis.com/auth/servicecontrol",
				"https://www.googleapis.com/auth/service.management.readonly",
				"https://www.googleapis.com/auth/trace.append",
			},
			Metadata: map[string]string{
				"disable-legacy-endpoints": "true",
			},
			DiskType: "pd-standard",
		},
		InitialNodeCount: 1,
		Management: &container.NodeManagement{
			AutoUpgrade: true,
			AutoRepair:  true,
		},
	}

	resp, err := p.clusterManager.CreateNodePool(ctx, &container.CreateNodePoolRequest{
		ProjectId: p.metadata.GetProjectID(),
		ClusterId: p.metadata.GetClusterID(),
		Zone:      p.metadata.GetZone(),
		NodePool:  nodePool,
	})

	if err != nil {
		return err
	}
	p.log.Log("Create Singleton Node: %s", resp.GetStatus())

	err = WaitUntilNodeCreated(p.kubernetes, TurndownNodeLabel, "true", GKETurndownPoolName, 5*time.Second, 5*time.Minute)
	if err != nil {
		return err
	}

	return nil
}

func (p *GKEProvider) GetPoolID(node *v1.Node) string {
	_, _, pool := p.projectInfoFor(node)
	return pool
}

func (p *GKEProvider) GetNodePools() ([]NodePool, error) {
	ctx := context.TODO()

	projectID := p.metadata.GetProjectID()
	zone := p.metadata.GetZone()
	cluster := p.metadata.GetClusterID()

	req := &container.ListNodePoolsRequest{
		ProjectId: projectID,
		Zone:      zone,
		ClusterId: cluster,
	}
	p.log.Log("Loading node pools for: [ProjectID: %s, Zone: %s, ClusterID: %s]", projectID, zone, cluster)

	resp, err := p.clusterManager.ListNodePools(ctx, req, options...)
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
			tags:        tags,
		})
	}

	return pools, nil
}

func (p *GKEProvider) SetNodePoolSizes(nodePools []NodePool, size int32) error {
	requests := []*container.SetNodePoolSizeRequest{}
	for _, nodePool := range nodePools {
		requests = append(requests, &container.SetNodePoolSizeRequest{
			ProjectId:  nodePool.Project(),
			ClusterId:  nodePool.ClusterID(),
			Zone:       nodePool.Zone(),
			NodePoolId: nodePool.Name(),
			NodeCount:  size,
		})

		p.log.Log("Resizing NodePool to 0 [Proj: %s, ClusterId: %s, Zone: %s, PoolID: %s]",
			nodePool.Project(),
			nodePool.ClusterID(),
			nodePool.Zone(),
			nodePool.Name())
	}

	ctx, cancel := context.WithCancel(context.TODO())

	waitChannel := async.NewWaitChannel()
	waitChannel.Add(len(requests))

	for _, req := range requests {
		go func(request *container.SetNodePoolSizeRequest) {
			defer waitChannel.Done()

			for {
				_, err := p.clusterManager.SetNodePoolSize(ctx, request, options...)
				if err == nil {
					p.log.Log("Resized NodePool Successfully: %s", request.NodePoolId)
					return
				}

				p.log.Log("NodePool operation already in queue, retrying...")

				select {
				case <-time.After(30 * time.Second):
				case <-ctx.Done():
					return
				}
			}
		}(req)
	}

	defer cancel()

	select {
	case <-waitChannel.Wait():
		return nil
	case <-time.After(30 * time.Minute):
		return fmt.Errorf("Resize Requests timed out after 30 minutes.")
	}
}

func (p *GKEProvider) ResetNodePoolSizes(nodePools []NodePool) error {
	requests := []*container.SetNodePoolSizeRequest{}
	for _, nodePool := range nodePools {
		requests = append(requests, &container.SetNodePoolSizeRequest{
			ProjectId:  nodePool.Project(),
			ClusterId:  nodePool.ClusterID(),
			Zone:       nodePool.Zone(),
			NodePoolId: nodePool.Name(),
			NodeCount:  nodePool.NodeCount(),
		})

		p.log.Log("Resizing NodePool to 0 %d [Proj: %s, ClusterId: %s, Zone: %s, PoolId: %s]",
			nodePool.NodeCount(),
			nodePool.Project(),
			nodePool.ClusterID(),
			nodePool.Zone(),
			nodePool.Name())
	}

	ctx, cancel := context.WithCancel(context.TODO())

	waitChannel := async.NewWaitChannel()
	waitChannel.Add(len(requests))

	for _, req := range requests {
		go func(request *container.SetNodePoolSizeRequest) {
			defer waitChannel.Done()

			for {
				_, err := p.clusterManager.SetNodePoolSize(ctx, request, options...)
				if err == nil {
					p.log.Log("Resized NodePool Successfully: %s", request.NodePoolId)
					return
				}

				p.log.Log("NodePool operation already in queue, retrying...")

				select {
				case <-time.After(30 * time.Second):
				case <-ctx.Done():
					return
				}
			}
		}(req)
	}

	defer cancel()

	select {
	case <-waitChannel.Wait():
		return nil
	case <-time.After(30 * time.Minute):
		return fmt.Errorf("Resizing node requests timed out after 30 minutes.")
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
