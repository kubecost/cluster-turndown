package provider

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/util/wait"
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

// ComputeProvider contains methods used to manage turndown
type ComputeProvider interface {
	IsServiceAccountKey() bool
	IsTurndownNodePool() bool
	CreateSingletonNodePool() error
	GetNodePools() ([]NodePool, error)
	GetPoolID(node *v1.Node) string
	SetNodePoolSizes(nodePools []NodePool, size int32) error
	ResetNodePoolSizes(nodePools []NodePool) error
}

// NodePool contains a node pool identifier and the initial number of nodes
// in the pool
type NodePool interface {
	Name() string
	Project() string
	Zone() string
	ClusterID() string
	MinNodes() int32
	MaxNodes() int32
	NodeCount() int32
	AutoScaling() bool
	Tags() map[string]string
}

var _ = klog.V(1)

func NewProvider(client kubernetes.Interface) (ComputeProvider, error) {
	if metadata.OnGCE() {
		return NewGKEProvider(client), nil
	}

	nodes, err := client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	provider := strings.ToLower(nodes.Items[0].Spec.ProviderID)
	if strings.HasPrefix(provider, "aws") {
		klog.V(2).Info("Found ProviderID starting with \"aws\", using AWS Provider")
		return NewAWSProvider(client), nil
	} else if strings.HasPrefix(provider, "azure") {
		klog.V(2).Info("Found ProviderID starting with \"azure\", using Azure Provider")
		return nil, errors.New("Azure Not Supported")
	} else {
		klog.V(2).Info("Unsupported provider, falling back to default")
		return nil, errors.New("Custom Not Supported")
	}
}

func WaitUntilNodeCreated(client kubernetes.Interface, nodeLabelKey, nodeLabelValue, nodePoolName string, interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		nodeList, err := client.CoreV1().Nodes().List(metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", nodeLabelKey, nodeLabelValue),
		})
		for _, node := range nodeList.Items {
			if strings.Contains(node.Name, nodePoolName) {
				return true, nil
			}
		}
		return false, err
	})
}
