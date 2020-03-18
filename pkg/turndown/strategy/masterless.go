package strategy

import (
	"fmt"

	"github.com/kubecost/cluster-turndown/pkg/logging"
	"github.com/kubecost/cluster-turndown/pkg/turndown/patcher"
	"github.com/kubecost/cluster-turndown/pkg/turndown/provider"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
)

const (
	MasterlessTaintKey = "CriticalAddonsOnly"
)

type MasterlessTurndownStrategy struct {
	client   kubernetes.Interface
	provider provider.ComputeProvider
	log      logging.NamedLogger
}

func NewMasterlessTurndownStrategy(client kubernetes.Interface, provider provider.ComputeProvider) TurndownStrategy {
	return &MasterlessTurndownStrategy{
		client:   client,
		provider: provider,
		log:      logging.NamedLogger("MasterlessStrategy"),
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
	if !ktdm.provider.IsServiceAccountKey() {
		return nil, fmt.Errorf("The current provider does not have a service account key set.")
	}

	// Determine if there is autoscaling node pools
	nodePools, err := ktdm.provider.GetNodePools()
	if err != nil {
		return nil, err
	}

	var autoScalingNodePool provider.NodePool = nil
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
			err := ktdm.provider.CreateSingletonNodePool()
			if err != nil {
				return nil, err
			}
		}

		// Lookup the turndown node in the kubernetes API
		nodeList, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{
			LabelSelector: "cluster-turndown-node=true",
		})
		if err != nil {
			return nil, err
		}
		tnode = &nodeList.Items[0]
	} else {
		// Otherwise, have the current pod move to autoscaling node pool
		nodeList, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{})
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
		tnode, err = patcher.UpdateNodeLabel(ktdm.client, *targetNode, "cluster-turndown-node", "true")
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
