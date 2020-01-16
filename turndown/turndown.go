package turndown

import (
	"fmt"
	"os"

	"github.com/kubecost/kubecost-turndown/turndown/patcher"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

const (
	KubecostTurnDownJobSuspend = "kubecost.kubernetes.io/job-suspend"
	KubecostTurnDownTaintKey   = "CriticalAddonsOnly"
)

var (
	KubecostFlattenerOmit = []string{"kubecost-turndown", "kube-dns", "kube-dns-autoscaler"}
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
	autoScaling *bool
	nodePools   []*NodePool
}

func NewKubernetesTurndownManager(client kubernetes.Interface, provider ComputeProvider, currentNode string) TurndownManager {
	return &KubernetesTurndownManager{
		client:      client,
		provider:    provider,
		currentNode: currentNode,
		autoScaling: nil,
	}
}

func (ktdm *KubernetesTurndownManager) IsScaledDown() bool {
	return ktdm.nodePools == nil || len(ktdm.nodePools) == 0
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

	// Determine if there is autoscaling node pools
	nodePools, err := ktdm.provider.GetNodePools()
	if err != nil {
		return err
	}

	var autoScalingNodePool *NodePool = nil
	for _, np := range nodePools {
		if np.AutoScaling {
			autoScalingNodePool = np
			break
		}
	}

	// The Target Node for deployment selection
	var tnode *v1.Node

	// When AutoScaling is Not Enabled, Create a Turndown Node
	if autoScalingNodePool == nil {
		klog.V(1).Infof("Finite node backed cluster. Creating singleton nodepool for turndown.")

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
		tnode = &nodeList.Items[0]
	} else {
		// Otherwise, have the current pod move to autoscaling node pool
		nodeList, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{})
		if err != nil {
			return err
		}

		var targetNode *v1.Node
		for _, node := range nodeList.Items {
			poolID := ktdm.provider.GetPoolID(&node)
			if poolID == autoScalingNodePool.NodePoolID {
				targetNode = &node
				break
			}
		}

		if targetNode == nil {
			return fmt.Errorf("Target node was not located for autoscaling cluster.")
		}

		// Patch and get the updated node
		tnode, err = patcher.UpdateNodeLabel(ktdm.client, *targetNode, "kubecost-turndown-node", "true")
		if err != nil {
			return err
		}
	}

	// Patch Node with Taint
	_, err = patcher.PatchNode(ktdm.client, *tnode, func(n *v1.Node) error {
		taints := n.Spec.Taints
		for _, taint := range taints {
			if taint.Key == KubecostTurnDownTaintKey {
				return patcher.NoUpdates
			}
		}

		n.Spec.Taints = append(taints, v1.Taint{
			Key:    KubecostTurnDownTaintKey,
			Value:  "true",
			Effect: v1.TaintEffectNoSchedule,
		})

		return nil
	})

	if err != nil {
		return err
	}

	klog.V(3).Infoln("Node Taint was successfully added for kubecost-turndown.")

	// Locate turndown namespace -- default to kubecost
	ns := os.Getenv("TURNDOWN_NAMESPACE")
	if ns == "" {
		ns = "kubecost"
	}

	// Modify the Deployment for the Current Turndown Pod to include a node selector
	deployment, err := ktdm.client.AppsV1().Deployments(ns).Get("kubecost-turndown", metav1.GetOptions{})
	if err != nil {
		return err
	}

	_, err = patcher.PatchDeployment(ktdm.client, *deployment, func(d *appsv1.Deployment) error {
		d.Spec.Template.Spec.NodeSelector = map[string]string{
			"kubecost-turndown-node": "true",
		}
		return nil
	})
	if err != nil {
		return err
	}

	klog.V(3).Infoln("Kubecost-Turndown Deployment successfully updated with node selector")

	return nil
}

func (ktdm *KubernetesTurndownManager) ScaleDownCluster() error {
	// 1. Start by finding all the nodes that Kubernetes is using
	nodes, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	// 2. Use provider to get all node pools used for this cluster, determine
	// whether or not there exists autoscaling node pools
	var isAutoScalingCluster bool = false
	pools := make(map[string]*NodePool)
	nodePools, err := ktdm.provider.GetNodePools()
	if err != nil {
		return err
	}
	for _, np := range nodePools {
		if np.AutoScaling {
			isAutoScalingCluster = true
		}
		pools[np.NodePoolID] = np
	}

	// If this cluster has autoscaling nodes, we consider the entire cluster
	// autoscaling. Run Flatten on the cluster to reduce deployments and daemonsets
	// to 0 replicas. Otherwise, just suspend cron jobs
	flattener := NewFlattener(ktdm.client, KubecostFlattenerOmit)
	if isAutoScalingCluster {
		err := flattener.Flatten()
		if err != nil {
			klog.V(1).Infof("Failed to flatten cluster: %s", err.Error())
			return err
		}
	} else {
		err := flattener.SuspendJobs()
		if err != nil {
			klog.V(1).Infof("Failed to suspend jobs: %s", err.Error())
			return err
		}
	}

	// 3. Drain a node if it is not the current node and is not part of an autoscaling pool.
	var currentNodePoolID string
	for _, n := range nodes.Items {
		poolID := ktdm.provider.GetPoolID(&n)

		if n.Name == ktdm.currentNode {
			currentNodePoolID = poolID
			continue
		}

		pool, ok := pools[poolID]
		if !ok {
			klog.V(1).Infof("Failed to locate pool id: %s in pools map.", poolID)
			continue
		}

		if pool.AutoScaling {
			continue
		}

		klog.V(3).Infof("Draining Node: %s", n.Name)
		draininator := NewDraininator(ktdm.client, n.Name)

		err = draininator.Drain()
		if err != nil {
			klog.V(1).Infof("Failed: %s - Error: %s", n.Name, err.Error())
		}
	}

	// 4. Filter out the current node pool holding the current node and/or autoscaling
	targetPools := []*NodePool{}
	for _, np := range nodePools {
		if np.NodePoolID == currentNodePoolID || np.AutoScaling {
			continue
		}

		targetPools = append(targetPools, np)
	}

	// Set NodePools on instance for resetting/upscaling
	ktdm.nodePools = targetPools
	ktdm.autoScaling = &isAutoScalingCluster

	// 5. Resize all the non-autoscaling node pools to 0
	err = ktdm.provider.SetNodePoolSizes(targetPools, 0)
	if err != nil {
		// TODO: Any steps that fail AFTER draining should revert the drain step?
		return err
	}

	return nil
}

func (ktdm *KubernetesTurndownManager) loadNodePools() error {
	pools, err := ktdm.provider.GetNodePools()
	if err != nil {
		return err
	}

	var nodePools []*NodePool
	for _, pool := range pools {
		autoscaling := pool.AutoScaling

		if autoscaling {
			ktdm.autoScaling = &autoscaling
			continue
		}

		nodePools = append(nodePools, pool)
	}

	ktdm.nodePools = nodePools
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

	// 3. Expand Autoscaling Nodes or Resume Jobs
	flattener := NewFlattener(ktdm.client, KubecostFlattenerOmit)
	if ktdm.autoScaling != nil && *ktdm.autoScaling {
		err := flattener.Expand()
		if err != nil {
			return err
		}
	} else {
		err := flattener.ResumeJobs()
		if err != nil {
			return err
		}
	}

	// No need to uncordone nodes here because they were complete removed and now added back
	// Reset node pools on instance
	ktdm.nodePools = nil
	ktdm.autoScaling = nil

	return nil
}
