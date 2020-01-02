package turndown

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	gax "github.com/googleapis/gax-go/v2"
	container "google.golang.org/genproto/googleapis/container/v1"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"cloud.google.com/go/compute/metadata"
	gke "cloud.google.com/go/container/apiv1"

	"k8s.io/klog"
)

const (
	LabelGKENodePool      = "cloud.google.com/gke-nodepool"
	GKECredsEnvVar        = "GOOGLE_APPLICATION_CREDENTIALS"
	GKEAuthServiceAccount = "/var/configs/key.json"
)

var (
	options []gax.CallOption = []gax.CallOption{
		//gax.WithGRPCOptions(grpc.WaitForReady(true)),
	}
)

type ComputeProvider interface {
	IsServiceAccountKey() bool
	IsTurndownNodePool() bool
	CreateSingletonNodePool() error
	SetServiceAccount(key string) error
	GetNodePoolList() ([]*NodePool, error)
	GetZoneNodePools(nodes *v1.NodeList) ([]*NodePoolCollection, error)
	GetNodePools([]*NodePoolCollection) ([]*NodePool, error)
	GetClusterNodePools(*v1.Node) ([]*NodePool, error)
	GetPoolID(node *v1.Node) string
	SetNodePoolSizes(nodePools []*NodePool, size int32) error
	ResetNodePoolSizes(nodePools []*NodePool) error
}

// Collection of NodePools for a specific project and zone
type NodePoolCollection struct {
	Project string
	Zone    string
	Pools   []string
}

// ComputeProvider for GKE
type GKEProvider struct {
	kubernetes     kubernetes.Interface
	clusterManager *gke.ClusterManagerClient
	clusterID      string
}

// NodePool contains a node pool identifier and the initial number of nodes
// in the pool
type NodePool struct {
	Project    string
	Zone       string
	ClusterID  string
	NodePoolID string
	NodeCount  int32
}

func (npc *NodePool) String() string {
	return fmt.Sprintf("[Name: %s, Count: %d]", npc.NodePoolID, npc.NodeCount)
}

func NewGKEProvider(kubernetes kubernetes.Interface) ComputeProvider {
	clusterManager, err := newClusterManager()
	if err != nil {
		klog.V(1).Infof("Failed to load service account.")
	}

	return &GKEProvider{
		kubernetes:     kubernetes,
		clusterManager: clusterManager,
		clusterID:      getClusterID(),
	}
}

func (p *GKEProvider) IsServiceAccountKey() bool {
	return fileExists(GKEAuthServiceAccount)
}

func (p *GKEProvider) SetServiceAccount(key string) error {
	err := ioutil.WriteFile(GKEAuthServiceAccount, []byte(key), 0644)
	if err != nil {
		return err
	}

	cm, err := newClusterManager()
	if err != nil {
		klog.V(1).Infof("Failed to create cluster manager: %s", err.Error())
		return err
	}

	klog.V(3).Infof("Successfully created new cluster manager from service account")

	p.clusterManager = cm
	return nil
}

func (p *GKEProvider) IsTurndownNodePool() bool {
	ctx := context.TODO()

	req := &container.GetNodePoolRequest{
		ProjectId:  getProjectID(),
		ClusterId:  p.clusterID,
		Zone:       getZone(),
		NodePoolId: "kubecost-turndown",
	}

	resp, err := p.clusterManager.GetNodePool(ctx, req)
	if err != nil {
		return false
	}

	return resp.GetInitialNodeCount() == 1
}

func (p *GKEProvider) CreateSingletonNodePool() error {
	ctx := context.TODO()

	// cloud.google.com/gke-nodepool
	nodePool := &container.NodePool{
		Name: "kubecost-turndown",
		Config: &container.NodeConfig{
			MachineType: "g1-small",
			DiskSizeGb:  10,
			Labels: map[string]string{
				"kubecost-turndown-node": "true",
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
		ProjectId: getProjectID(),
		ClusterId: p.clusterID,
		Zone:      getZone(),
		NodePool:  nodePool,
	})

	if err != nil {
		return err
	}
	klog.Infof("Create Singleton Node: %s", resp.GetStatus())

	err = WaitUntilNodeCreated(p.kubernetes, "kubecost-turndown-node", "true", "kubecost-turndown", 5*time.Second, 5*time.Minute)
	if err != nil {
		return err
	}

	return nil
}

func (p *GKEProvider) GetPoolID(node *v1.Node) string {
	_, _, pool := p.projectInfoFor(node)
	return pool
}

func (p *GKEProvider) GetZoneNodePools(nodes *v1.NodeList) ([]*NodePoolCollection, error) {
	var collections []*NodePoolCollection
	for _, n := range nodes.Items {
		project, zone, pool := p.projectInfoFor(&n)

		var poolCollection *NodePoolCollection
		for _, np := range collections {
			if np.Project == project && np.Zone == zone {
				poolCollection = np
				break
			}
		}

		if poolCollection == nil {
			poolCollection = &NodePoolCollection{
				Project: project,
				Zone:    zone,
				Pools:   []string{},
			}

			collections = append(collections, poolCollection)
		}

		var found bool = false
		for _, existing := range poolCollection.Pools {
			if existing == pool {
				found = true
				break
			}
		}
		if !found {
			poolCollection.Pools = append(poolCollection.Pools, pool)
		}
	}

	return collections, nil
}

func (p *GKEProvider) GetNodePools(nodePoolCollections []*NodePoolCollection) ([]*NodePool, error) {
	requests := []*container.GetNodePoolRequest{}

	for _, poolCollection := range nodePoolCollections {
		for _, pool := range poolCollection.Pools {
			requests = append(requests, &container.GetNodePoolRequest{
				ProjectId:  poolCollection.Project,
				ClusterId:  p.clusterID,
				Zone:       poolCollection.Zone,
				NodePoolId: pool,
			})
		}
	}

	poolCounts := []*NodePool{}
	ctx := context.TODO()

	for _, req := range requests {
		resp, err := p.clusterManager.GetNodePool(ctx, req, options...)
		if err != nil {
			klog.Infof("Error: %s", err.Error())
			continue
		}

		poolCounts = append(poolCounts, &NodePool{
			Project:    req.ProjectId,
			ClusterID:  req.ClusterId,
			Zone:       req.Zone,
			NodePoolID: resp.GetName(),
			NodeCount:  resp.GetInitialNodeCount(),
		})
	}

	return poolCounts, nil
}

func (p *GKEProvider) GetNodePoolList() ([]*NodePool, error) {
	ctx := context.TODO()

	projectID := getProjectID()
	zone := getZone()
	cluster := getClusterID()

	req := &container.ListNodePoolsRequest{
		ProjectId: projectID,
		Zone:      zone,
		ClusterId: cluster,
	}
	klog.Infof("Loading node pools for: [ProjectID: %s, Zone: %s, ClusterID: %s]", projectID, zone, cluster)

	resp, err := p.clusterManager.ListNodePools(ctx, req, options...)
	if err != nil {
		return nil, err
	}

	pools := []*NodePool{}

	for _, np := range resp.GetNodePools() {
		pools = append(pools, &NodePool{
			Project:    projectID,
			ClusterID:  cluster,
			Zone:       zone,
			NodePoolID: np.GetName(),
			NodeCount:  np.GetInitialNodeCount(),
		})
	}

	return pools, nil
}

func (p *GKEProvider) SetNodePoolSizes(nodePools []*NodePool, size int32) error {
	requests := []*container.SetNodePoolSizeRequest{}
	for _, nodePool := range nodePools {
		requests = append(requests, &container.SetNodePoolSizeRequest{
			ProjectId:  nodePool.Project,
			ClusterId:  nodePool.ClusterID,
			Zone:       nodePool.Zone,
			NodePoolId: nodePool.NodePoolID,
			NodeCount:  size,
		})

		klog.V(3).Infof("Created Resize to 0 Request: Proj: %s, ClusterId: %s, Zone: %s, PoolId: %s", nodePool.Project, nodePool.ClusterID, nodePool.Zone, nodePool.NodePoolID)
	}

	ctx := context.TODO()

	for _, req := range requests {

		resp, err := p.clusterManager.SetNodePoolSize(ctx, req, options...)
		if err != nil {
			klog.V(1).Infof("Failed to executed request: %s", err.Error())
		}

		klog.V(3).Infof("Response Status: %s", resp.GetStatus())
	}

	return nil
}

func (p *GKEProvider) GetClusterNodePools(currentNode *v1.Node) ([]*NodePool, error) {
	var pools []*NodePool

	project, zone, poolName := p.projectInfoFor(currentNode)

	ctx := context.TODO()

	poolResp, err := p.clusterManager.ListNodePools(ctx, &container.ListNodePoolsRequest{
		ProjectId: project,
		Zone:      zone,
		ClusterId: p.clusterID,
	})
	if err != nil {
		return nil, err
	}
	for _, np := range poolResp.GetNodePools() {
		if np.Name != poolName {
			pools = append(pools, &NodePool{
				Project:    project,
				Zone:       zone,
				ClusterID:  p.clusterID,
				NodePoolID: np.Name,
				NodeCount:  np.InitialNodeCount,
			})
		}
	}

	return pools, nil
}

func (p *GKEProvider) ResetNodePoolSizes(nodePools []*NodePool) error {
	requests := []*container.SetNodePoolSizeRequest{}
	for _, nodePool := range nodePools {
		requests = append(requests, &container.SetNodePoolSizeRequest{
			ProjectId:  nodePool.Project,
			ClusterId:  nodePool.ClusterID,
			Zone:       nodePool.Zone,
			NodePoolId: nodePool.NodePoolID,
			NodeCount:  nodePool.NodeCount,
		})

		klog.V(3).Infof("Created Resize to %d Request: Proj: %s, ClusterId: %s, Zone: %s, PoolId: %s",
			nodePool.NodeCount,
			nodePool.Project,
			nodePool.ClusterID,
			nodePool.Zone,
			nodePool.NodePoolID)
	}

	ctx := context.TODO()

	for _, req := range requests {
		resp, err := p.clusterManager.SetNodePoolSize(ctx, req, options...)
		if err != nil {
			klog.V(1).Infof("Failed to execute request to reset node pool size: %s", err.Error())
		}

		klog.V(3).Infof("Response Status: %s", resp.GetStatus())
	}

	return nil
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

func newClusterManager() (*gke.ClusterManagerClient, error) {
	if !fileExists(GKEAuthServiceAccount) {
		return nil, fmt.Errorf("Failed to located service account file: %s", GKEAuthServiceAccount)
	}

	ctx := context.Background()

	clusterManager, err := gke.NewClusterManagerClient(ctx)
	if err != nil {
		return nil, err
	}

	return clusterManager, nil
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func getProjectID() string {
	metadataClient := metadata.NewClient(&http.Client{
		Transport: userAgentTransport{
			userAgent: "kubecost-turndown",
			base:      http.DefaultTransport,
		},
	})
	projectID, err := metadataClient.ProjectID()
	if err != nil {
		klog.Infof("Error: %s", err.Error())
		return ""
	}

	return projectID
}

func getClusterID() string {
	metadataClient := metadata.NewClient(&http.Client{
		Transport: userAgentTransport{
			userAgent: "kubecost-turndown",
			base:      http.DefaultTransport,
		},
	})

	attribute, err := metadataClient.InstanceAttributeValue("cluster-name")
	if err != nil {
		klog.Infof("Error: %s", err.Error())
		return ""
	}

	return attribute
}

func getZone() string {
	metadataClient := metadata.NewClient(&http.Client{
		Transport: userAgentTransport{
			userAgent: "kubecost-turndown",
			base:      http.DefaultTransport,
		},
	})

	zone, err := metadataClient.Zone()
	if err != nil {
		klog.Infof("Error: %s", err.Error())
		return ""
	}

	return zone
}
