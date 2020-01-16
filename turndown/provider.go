package turndown

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kubecost/kubecost-turndown/async"

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
	GKEAuthServiceAccount = "/var/keys/service-key.json"
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
	GetNodePools() ([]*NodePool, error)
	GetPoolID(node *v1.Node) string
	SetNodePoolSizes(nodePools []*NodePool, size int32) error
	ResetNodePoolSizes(nodePools []*NodePool) error
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
	Project     string
	Zone        string
	ClusterID   string
	NodePoolID  string
	NodeCount   int32
	AutoScaling bool
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

func (p *GKEProvider) GetNodePools() ([]*NodePool, error) {
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
			Project:     projectID,
			ClusterID:   cluster,
			Zone:        zone,
			NodePoolID:  np.GetName(),
			NodeCount:   np.GetInitialNodeCount(),
			AutoScaling: np.Autoscaling.GetEnabled(),
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

	ctx, cancel := context.WithCancel(context.TODO())

	waitChannel := async.NewWaitChannel()
	waitChannel.Add(len(requests))

	for _, req := range requests {
		go func(request *container.SetNodePoolSizeRequest) {
			defer waitChannel.Done()

			for {
				resp, err := p.clusterManager.SetNodePoolSize(ctx, request, options...)
				if err == nil {
					klog.V(3).Infof("Response Status: %s", resp.GetStatus())
					return
				}

				klog.V(3).Infof("Failed to execute request: %s", err.Error())

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

	ctx, cancel := context.WithCancel(context.TODO())

	waitChannel := async.NewWaitChannel()
	waitChannel.Add(len(requests))

	for _, req := range requests {
		go func(request *container.SetNodePoolSizeRequest) {
			defer waitChannel.Done()

			for {
				resp, err := p.clusterManager.SetNodePoolSize(ctx, request, options...)
				if err == nil {
					klog.V(3).Infof("Response Status: %s", resp.GetStatus())
					return
				}

				klog.V(3).Infof("Failed to execute request to reset node pool size: %s", err.Error())
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
