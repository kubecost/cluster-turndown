package turndown

import (
	"encoding/json"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

// TurndownManager is an implementation prototype for an object capable of managing
// turndown and turnup for a kubernetes cluster
type TurndownManager interface {
	// Whether or not the cluster is scaled down or not
	IsScaledDown() bool

	// Whether or not the current pod is running on the node designated for turndown
	// or not
	IsRunningOnTurndownNode() (bool, error)

	// Prepares the turndown environment by creating a small single node pool, tainting
	// the node, and then allow the current pod deployment such that it has tolerations
	// and a node selector to run on the newly created node
	PrepareTurndownEnvironment() error

	// Scales down the cluster leaving the single small node pool running the scheduled
	// scale up
	ScaleDownCluster() error

	// Scales back up the cluster
	ScaleUpCluster() error
}

type KubernetesTurndownManager struct {
	client      kubernetes.Interface
	provider    ComputeProvider
	currentNode string
	nodePools   []*NodePool
}

func NewKubernetesTurndownManager(client kubernetes.Interface, provider ComputeProvider, currentNode string) TurndownManager {
	return &KubernetesTurndownManager{
		client:      client,
		provider:    provider,
		currentNode: currentNode,
	}
}

func (ktdm *KubernetesTurndownManager) IsScaledDown() bool {
	return ktdm.nodePools == nil || len(ktdm.nodePools) != 0
}

func (ktdm *KubernetesTurndownManager) IsRunningOnTurndownNode() (bool, error) {
	nodeList, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: "kubecost-turndown-node=true",
	})
	if err != nil {
		return false, err
	}

	if len(nodeList.Items) == 0 {
		return false, nil
	}

	result := nodeList.Items[0].Name == ktdm.currentNode
	return result, nil
}

func (ktdm *KubernetesTurndownManager) PrepareTurndownEnvironment() error {
	if !ktdm.provider.IsServiceAccountKey() {
		return fmt.Errorf("The current provider does not have a service account key set.")
	}

	// There is already a valid turndown node pool
	if ktdm.provider.IsTurndownNodePool() {
		return nil
	}

	// Create a new singleton node pool with a small instance capable of hosting the turndown
	// pod -- this implementation will create and wait for the node to exist before returning
	err := ktdm.provider.CreateSingletonNodePool()
	if err != nil {
		return err
	}

	// Lookup the turndown node in the kubernetes API
	nodeList, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: "kubecost-turndown-node=true",
	})
	if err != nil {
		return err
	}

	// Patch the Node with the kubecost-turndown taint
	node := nodeList.Items[0]
	taints := node.Spec.Taints
	for _, taint := range taints {
		if taint.Key == "kubecost-turndown" {
			return nil
		}
	}

	oldData, err := json.Marshal(node)
	node.Spec.Taints = append(taints, v1.Taint{
		Key:    "kubecost-turndown",
		Value:  "true",
		Effect: v1.TaintEffectNoSchedule,
	})
	newData, err := json.Marshal(node)

	patch, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, node)
	if err != nil {
		return err
	}
	_, err = ktdm.client.CoreV1().Nodes().Patch(node.Name, types.MergePatchType, patch)
	if err != nil {
		return err
	}

	klog.V(3).Infoln("Node Taint was successfully added for kubecost-turndown.")

	// Modify the Deployment for the Current Turndown Pod to include a node selector
	deployment, err := ktdm.client.AppsV1().Deployments("kubecost").Get("kubecost-turndown", metav1.GetOptions{})
	if err != nil {
		return err
	}

	oldData, err = json.Marshal(deployment)
	deployment.Spec.Template.Spec.NodeSelector = map[string]string{
		"kubecost-turndown-node": "true",
	}
	newData, err = json.Marshal(deployment)

	patch, err = strategicpatch.CreateTwoWayMergePatch(oldData, newData, deployment)
	if err != nil {
		return err
	}
	_, err = ktdm.client.AppsV1().Deployments("kubecost").Patch(deployment.Name, types.MergePatchType, patch)
	if err != nil {
		return err
	}

	klog.V(3).Infoln("Kubecost-Turndown Deployment successfully updated with node selector")

	return nil
}

func (ktdm *KubernetesTurndownManager) AddKubeDNSTolerations() error {
	deployment, err := ktdm.client.AppsV1().Deployments("kube-system").Get("kube-dns", metav1.GetOptions{})
	if err != nil {
		return err
	}
	tolerations := deployment.Spec.Template.Spec.Tolerations
	for _, toleration := range tolerations {
		if toleration.Key == "kubecost-turndown" {
			return nil
		}
	}

	oldData, err := json.Marshal(deployment)
	deployment.Spec.Template.Spec.Tolerations = append(tolerations, v1.Toleration{
		Key:      "kubecost-turndown",
		Operator: v1.TolerationOpExists,
	})
	newData, err := json.Marshal(deployment)

	patch, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, deployment)
	if err != nil {
		return err
	}
	_, err = ktdm.client.AppsV1().Deployments("kube-system").Patch(deployment.Name, types.MergePatchType, patch)
	if err != nil {
		return err
	}

	klog.V(3).Infoln("Kube-DNS Successfully added tolerations for kubecost-turndown.")
	return nil
}

func (ktdm *KubernetesTurndownManager) ScaleDownCluster() error {
	// 1. Start by finding all the nodes that Kubernetes is using
	nodes, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	// 2a. Use provided to generate expected node pools associated with this cluster
	poolCollections, err := ktdm.provider.GetZoneNodePools(nodes)
	if err != nil {
		return err
	}
	nodePools, err := ktdm.provider.GetNodePools(poolCollections)
	if err != nil {
		return err
	}

	// 2b. Modify Kube-DNS Deployment with tolerations for Our Node
	// This step is necessary to ensure that once the main nodepool is
	// drained, kube-dns can be rescheduled back onto the turndown node
	err = ktdm.AddKubeDNSTolerations()
	if err != nil {
		return err
	}

	// 3. Drain all of the nodes except the one this pod runs on
	var currentNodePoolID string
	for _, n := range nodes.Items {
		if n.Name == ktdm.currentNode {
			currentNodePoolID = ktdm.provider.GetPoolID(&n)
			continue
		}

		klog.V(1).Infof("Draining Node: %s", n.Name)
		draininator := NewDraininator(ktdm.client, n.Name)

		err = draininator.Drain()
		if err != nil {
			klog.V(1).Infof("Failed: %s - Error: %s", n.Name, err.Error())
		}
	}

	// 4. Filter out the current node pool holding the current node
	targetPools := []*NodePool{}
	for _, np := range nodePools {
		if np.NodePoolID == currentNodePoolID {
			continue
		}

		targetPools = append(targetPools, np)
	}

	// Set NodePools on instance for resetting/upscaling
	ktdm.nodePools = targetPools

	// 5. Resize all node pools to 0
	err = ktdm.provider.SetNodePoolSizes(targetPools, 0)
	if err != nil {
		// TODO: Any steps that fail AFTER draining should revert the drain step?
		return err
	}

	return nil
}

func (ktdm *KubernetesTurndownManager) loadNodePools() error {
	node, err := ktdm.client.CoreV1().Nodes().Get(ktdm.currentNode, metav1.GetOptions{})
	if err != nil {
		return err
	}

	pools, err := ktdm.provider.GetClusterNodePools(node)
	if err != nil {
		return err
	}

	ktdm.nodePools = pools
	return nil
}

func (ktdm *KubernetesTurndownManager) ScaleUpCluster() error {
	// If for some reason, we're trying to scale up, but there weren't
	// any node pools set from downscale, try to load them
	if len(ktdm.nodePools) == 0 {
		if err := ktdm.loadNodePools(); err != nil {
			return err
		}

		// Check Again
		if len(ktdm.nodePools) == 0 {
			return fmt.Errorf("Failed to locate any node pools to scale up.")
		}
	}

	// 2. Set NodePool sizes back to what they were previously
	err := ktdm.provider.ResetNodePoolSizes(ktdm.nodePools)
	if err != nil {
		return err
	}

	// No need to uncordone nodes here because they were complete removed and now added back
	// Reset node pools on instance
	ktdm.nodePools = nil

	return nil
}
