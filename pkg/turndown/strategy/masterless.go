package strategy

import (
	"context"
	"fmt"

	"github.com/kubecost/cluster-turndown/pkg/cluster/patcher"
	cp "github.com/kubecost/cluster-turndown/pkg/cluster/provider"
	"github.com/kubecost/cluster-turndown/pkg/logging"
	"github.com/kubecost/cluster-turndown/pkg/turndown/provider"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
)

const (
	MasterlessTaintKey = "CriticalAddonsOnly"
)

// MasterlessTurndownStrategy is a TurndownStrategy implementation used in managed kubernetes where the master control plane is
// not available as a node to the cluster. When the turndown schedule occurs, a new node pool with a single "small" node is created.
// Taints are added to this node to only allow specific pods to be scheduled there. We update the turndown deployment such
// that the turndown pod is allowed to schedule on the singleton node. Once the pod is moved to the new node, it will start back up and
// resume scaledown. This is done by cordoning all nodes in the cluster (other than our new small node), and then reducing the node pool
// sizes to 0.
type MasterlessTurndownStrategy struct {
	client         kubernetes.Interface
	provider       provider.TurndownProvider
	nodePoolLabels map[string]string
	log            logging.NamedLogger
}

// Creates a new MasterlessTurndownStrategy instance
func NewMasterlessTurndownStrategy(client kubernetes.Interface, provider provider.TurndownProvider, npLabels map[string]string) TurndownStrategy {
	return &MasterlessTurndownStrategy{
		client:         client,
		provider:       provider,
		nodePoolLabels: npLabels,
		log:            logging.NamedLogger("MasterlessStrategy"),
	}
}

// Masterless strategy is currrently non-reversible. Need to determine whether there is value in
// reprovisioning every turndown and deleting on scale up.
func (mts *MasterlessTurndownStrategy) IsReversible() bool {
	return false
}

func (mts *MasterlessTurndownStrategy) TaintKey() string {
	return MasterlessTaintKey
}

// This method will locate or create a node, apply a specific taint and
// label, and return the updated kubernetes Node instance.
func (ktdm *MasterlessTurndownStrategy) CreateOrGetHostNode() (*v1.Node, error) {
	// Determine if there is autoscaling node pools
	nodePools, err := ktdm.provider.GetNodePools()
	if err != nil {
		return nil, err
	}

	var autoScalingNodePool cp.NodePool = nil
	for _, np := range nodePools {
		if np.AutoScaling() {
			autoScalingNodePool = np
			break
		}
	}

	// The Target Node for deployment selection
	var tnode *v1.Node

	// When AutoScaling is Not Enabled, Create a Turndown Node
	if autoScalingNodePool == nil {
		ktdm.log.Log("Finite node backed cluster. Creating singleton nodepool for turndown.")

		// There isn't a node pool for the turndown pod, so create one
		if !ktdm.provider.IsTurndownNodePool() {
			// Create a new singleton node pool with a small instance capable of hosting the turndown
			// pod -- this implementation will create and wait for the node to exist before returning
			err := ktdm.provider.CreateSingletonNodePool(ktdm.nodePoolLabels)
			if err != nil {
				return nil, err
			}
		}

		// Lookup the turndown node in the kubernetes API
		nodeList, err := ktdm.client.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{
			LabelSelector: provider.TurndownNodeLabelSelector,
		})
		if err != nil {
			return nil, err
		}
		tnode = &nodeList.Items[0]
	} else {
		// Otherwise, have the current pod move to autoscaling node pool
		nodeList, err := ktdm.client.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}

		var targetNode *v1.Node
		for _, node := range nodeList.Items {
			poolID := ktdm.provider.GetPoolID(&node)
			if poolID == autoScalingNodePool.Name() {
				targetNode = &node
				break
			}
		}

		if targetNode == nil {
			return nil, fmt.Errorf("Target node was not located for autoscaling cluster.")
		}

		// Patch and get the updated node
		tnode, err = patcher.UpdateNodeLabel(ktdm.client, *targetNode, provider.TurndownNodeLabel, "true")
		if err != nil {
			return nil, err
		}
	}

	// Patch Node with Taint
	return patcher.PatchNode(ktdm.client, *tnode, func(n *v1.Node) error {
		taints := n.Spec.Taints
		for _, taint := range taints {
			if taint.Key == MasterlessTaintKey {
				return patcher.NoUpdates
			}
		}

		n.Spec.Taints = append(taints, v1.Taint{
			Key:    MasterlessTaintKey,
			Value:  "true",
			Effect: v1.TaintEffectNoSchedule,
		})

		return nil
	})
}

func (mts *MasterlessTurndownStrategy) UpdateDNS() error {
	// No-op for this strategy, as we already use the critical addon taint for our target host
	// node. Kube-DNS already has a toleration for critical addon.
	return nil
}

func (mts *MasterlessTurndownStrategy) ReverseHostNode() error {
	return fmt.Errorf("MasterlessTurndownStrategy::ReverseHostNode() is not yet implemented!")
}
