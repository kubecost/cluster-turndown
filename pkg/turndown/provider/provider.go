package provider

import (
	"errors"
	"net/http"
	"strings"

	"cloud.google.com/go/compute/metadata"

	cp "github.com/kubecost/cluster-turndown/pkg/cluster/provider"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

const (
	TurndownNodeLabel = "cluster-turndown-node"
)

type UserAgentTransport struct {
	userAgent string
	base      http.RoundTripper
}

func (t UserAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", t.userAgent)
	return t.base.RoundTrip(req)
}

// TurndownProvider contains methods used to manage turndown
type TurndownProvider interface {
	IsTurndownNodePool() bool
	CreateSingletonNodePool() error
	GetNodePools() ([]cp.NodePool, error)
	GetPoolID(node *v1.Node) string
	SetNodePoolSizes(nodePools []cp.NodePool, size int32) error
	ResetNodePoolSizes(nodePools []cp.NodePool) error
}

func NewTurndownProvider(client kubernetes.Interface, clusterProvider cp.ClusterProvider) (TurndownProvider, error) {
	if metadata.OnGCE() {
		return NewGKEProvider(client, clusterProvider), nil
	}

	nodes, err := client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	node := nodes.Items[0]
	provider := strings.ToLower(node.Spec.ProviderID)
	if strings.HasPrefix(provider, "aws") {
		if _, ok := node.Labels["eks.amazonaws.com/nodegroup"]; ok {
			klog.V(2).Info("Found ProviderID starting with \"aws\" and eks nodegroup, using EKS Provider")
			return NewEKSProvider(client, clusterProvider), nil
		}
		klog.V(2).Info("Found ProviderID starting with \"aws\", using AWS Provider")
		return NewAWSProvider(client, clusterProvider), nil
	} else if strings.HasPrefix(provider, "azure") {
		klog.V(2).Info("Found ProviderID starting with \"azure\", using Azure Provider")
		return nil, errors.New("Azure Not Supported")
	} else {
		klog.V(2).Info("Unsupported provider, falling back to default")
		return nil, errors.New("Custom Not Supported")
	}
}
