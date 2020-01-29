package turndown

import (
	"fmt"
	"os"

	"github.com/kubecost/kubecost-turndown/logging"
	"github.com/kubecost/kubecost-turndown/turndown/patcher"
	"github.com/kubecost/kubecost-turndown/turndown/provider"
	"github.com/kubecost/kubecost-turndown/turndown/strategy"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
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
	provider    provider.ComputeProvider
	strategy    strategy.TurndownStrategy
	currentNode string
	autoScaling *bool
	nodePools   []provider.NodePool
	log         logging.NamedLogger
}

func NewKubernetesTurndownManager(client kubernetes.Interface, provider provider.ComputeProvider, strategy strategy.TurndownStrategy, currentNode string) TurndownManager {
	return &KubernetesTurndownManager{
		client:      client,
		provider:    provider,
		strategy:    strategy,
		currentNode: currentNode,
		autoScaling: nil,
		log:         logging.NamedLogger("Turndown"),
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
	ktdm.log.Log("Creating or Getting the Target Host Node...")
	_, err := ktdm.strategy.CreateOrGetHostNode()
	if err != nil {
		return err
	}

	ktdm.log.Log("Fixing DNS if applicable...")

	// NOTE: Need to investigate this a bit more. Sometimes, when we turn down, DNS
	// NOTE: for the turndown pod seems to start failing. We should make sure we
	// NOTE: continue to allow a dns service to run for the turndown pod.
	err = ktdm.strategy.AllowKubeDNS()
	if err != nil {
		ktdm.log.Err("Failed to allow kube-dns on master node: %s", err.Error())
		return err
	}

	// Locate turndown namespace -- default to kubecost
	ns := os.Getenv("TURNDOWN_NAMESPACE")
	if ns == "" {
		ns = "kubecost"
	}

	ktdm.log.Log("Applying Tolerations and Node Selector to turndown deployment...")

	// Modify the Deployment for the Current Turndown Pod to include a node selector
	deployment, err := ktdm.client.AppsV1().Deployments(ns).Get("kubecost-turndown", metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Patch the deployment of the turndown pod with a node selector for the target node as well as
	// tolerations for the applied taint
	_, err = patcher.PatchDeployment(ktdm.client, *deployment, func(d *appsv1.Deployment) error {
		d.Spec.Template.Spec.Tolerations = append(d.Spec.Template.Spec.Tolerations, v1.Toleration{
			Key:      ktdm.strategy.TaintKey(),
			Effect:   v1.TaintEffectNoSchedule,
			Operator: v1.TolerationOpExists,
		})
		d.Spec.Template.Spec.NodeSelector = map[string]string{
			"kubecost-turndown-node": "true",
		}
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (ktdm *KubernetesTurndownManager) ScaleDownCluster() error {
	ktdm.log.Log("Scaling Down Cluster Now")

	// 1. Start by finding all the nodes that Kubernetes is using
	nodes, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	// 2. Use provider to get all node pools used for this cluster, determine
	// whether or not there exists autoscaling node pools
	var isAutoScalingCluster bool = false
	pools := make(map[string]provider.NodePool)
	nodePools, err := ktdm.provider.GetNodePools()
	if err != nil {
		return err
	}
	for _, np := range nodePools {
		if np.AutoScaling() {
			isAutoScalingCluster = true
		}
		pools[np.Name()] = np
	}

	// If this cluster has autoscaling nodes, we consider the entire cluster
	// autoscaling. Run Flatten on the cluster to reduce deployments and daemonsets
	// to 0 replicas. Otherwise, just suspend cron jobs
	flattener := NewFlattener(ktdm.client, KubecostFlattenerOmit)
	if isAutoScalingCluster {
		ktdm.log.Log("Found Cluster-AutoScaler. Flattening Cluster...")

		err := flattener.Flatten()
		if err != nil {
			klog.V(1).Infof("Failed to flatten cluster: %s", err.Error())
			return err
		}
	} else {
		ktdm.log.Log("Suspending all jobs...")

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
			ktdm.log.Err("Failed to locate pool id: %s in pools map.", poolID)
			continue
		}

		if pool.AutoScaling() {
			continue
		}

		draininator := NewDraininator(ktdm.client, n.Name)
		err = draininator.Drain()
		if err != nil {
			ktdm.log.Err("Failed: %s - Error: %s", n.Name, err.Error())
		}
	}

	// 4. Filter out the current node pool holding the current node and/or autoscaling
	targetPools := []provider.NodePool{}
	for _, np := range nodePools {
		if np.Name() == currentNodePoolID || np.AutoScaling() {
			continue
		}

		targetPools = append(targetPools, np)
	}

	// Set NodePools on instance for resetting/upscaling
	ktdm.nodePools = targetPools
	ktdm.autoScaling = &isAutoScalingCluster

	ktdm.log.Log("Resizing all non-autoscaling node groups to 0...")

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

	var nodePools []provider.NodePool
	for _, pool := range pools {
		autoscaling := pool.AutoScaling()

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
		ktdm.log.Log("NodeGroups Require Loading. Loading now...")

		if err := ktdm.loadNodePools(); err != nil {
			ktdm.log.Err("Failed to load NodeGroups")
			return err
		}

		// Check Again
		if len(ktdm.nodePools) == 0 {
			ktdm.log.Err("Failed to load NodeGroups")
			return fmt.Errorf("Failed to locate any node pools to scale up.")
		}
	}

	ktdm.log.Log("Resetting all NodeGroup sizes to pre-turndown capacity...")

	// 2. Set NodePool sizes back to what they were previously
	err := ktdm.provider.ResetNodePoolSizes(ktdm.nodePools)
	if err != nil {
		return err
	}

	// 3. Expand Autoscaling Nodes or Resume Jobs
	flattener := NewFlattener(ktdm.client, KubecostFlattenerOmit)
	if ktdm.autoScaling != nil && *ktdm.autoScaling {
		ktdm.log.Log("Expanding Cluster...")

		err := flattener.Expand()
		if err != nil {
			return err
		}
	} else {
		ktdm.log.Log("Resuming Jobs...")

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
