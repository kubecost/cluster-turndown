package turndown

import (
	"encoding/json"
	"fmt"
	"strconv"

	v1b1 "k8s.io/api/batch/v1beta1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

const (
	KubecostTurnDownJobSuspend = "kubecost.kubernetes.io/job-suspend"
	KubecostTurnDownTaintKey   = "CriticalAddonsOnly"
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

	// Patch the Node with the kubecost turndown taint
	// Instead of using a custom taint here, we use 'CriticalAddonsOnly', which
	// will allow kube-dns to continue to schedule on the node (required)
	node := nodeList.Items[0]
	taints := node.Spec.Taints
	for _, taint := range taints {
		if taint.Key == KubecostTurnDownTaintKey {
			return nil
		}
	}

	oldData, err := json.Marshal(node)
	node.Spec.Taints = append(taints, v1.Taint{
		Key:    KubecostTurnDownTaintKey,
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
	// TODO: This should use the actual deployment namespace instead of "kubecost"
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
	// TODO: Use actual deployment namespace instead of "kubecost"
	_, err = ktdm.client.AppsV1().Deployments("kubecost").Patch(deployment.Name, types.MergePatchType, patch)
	if err != nil {
		return err
	}

	klog.V(3).Infoln("Kubecost-Turndown Deployment successfully updated with node selector")

	return nil
}

func (ktdm *KubernetesTurndownManager) SuspendJobs() error {
	jobsList, err := ktdm.client.BatchV1beta1().CronJobs("").List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, job := range jobsList.Items {
		err := ktdm.SuspendJob(job)
		if err != nil {
			klog.V(3).Infof("Failed to suspend CronJob: %s", err.Error())
		}
	}

	return nil
}

func (ktdm *KubernetesTurndownManager) ResumeJobs() error {
	jobsList, err := ktdm.client.BatchV1beta1().CronJobs("").List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, job := range jobsList.Items {
		err := ktdm.ResumeJob(job)
		if err != nil {
			klog.V(3).Infof("Failed to resume CronJob: %s", err.Error())
		}
	}

	return nil
}

func (ktdm *KubernetesTurndownManager) SuspendJob(job v1b1.CronJob) error {
	oldData, err := json.Marshal(job)

	var previousValue *bool
	if job.Spec.Suspend != nil {
		previousValue = job.Spec.Suspend
	}

	// Suspend the job
	value := true
	job.Spec.Suspend = &value

	// If there wasn't a previous value set, no need to set flag
	if previousValue != nil {
		if job.Annotations == nil {
			job.Annotations = map[string]string{
				KubecostTurnDownJobSuspend: fmt.Sprintf("%t", *previousValue),
			}
		} else {
			job.Annotations[KubecostTurnDownJobSuspend] = fmt.Sprintf("%t", *previousValue)
		}
	}

	newData, err := json.Marshal(job)
	patch, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, job)
	if err != nil {
		klog.Errorf("Couldn't set CronJob to suspended: %s", err.Error())
		return err
	}

	_, err = ktdm.client.BatchV1beta1().CronJobs(job.Namespace).Patch(job.Name, types.MergePatchType, patch)
	if err != nil {
		klog.Errorf("Couldn't patch CronJob: %s", err.Error())
		return err
	}

	return nil
}

// Sets the deployment pods to a safe-evict state, updates annotation flags
func (ktdm *KubernetesTurndownManager) ResumeJob(job v1b1.CronJob) error {
	oldData, err := json.Marshal(job)

	var suspend bool = false
	if job.Annotations != nil {
		// If there wasn't an entry, remove the pod safe evict flag
		suspendEntry, ok := job.Annotations[KubecostTurnDownJobSuspend]
		if ok {
			suspend, err = strconv.ParseBool(suspendEntry)
			if err != nil {
				return err
			}

			delete(job.Annotations, KubecostTurnDownJobSuspend)
		}
	}

	job.Spec.Suspend = &suspend

	newData, err := json.Marshal(job)
	patch, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, job)
	if err != nil {
		klog.Errorf("Couldn't set CronJob to resume: %s", err.Error())
		return err
	}

	_, err = ktdm.client.BatchV1beta1().CronJobs(job.Namespace).Patch(job.Name, types.MergePatchType, patch)
	if err != nil {
		klog.Errorf("Couldn't patch CronJob: %s", err.Error())
		return err
	}

	return nil
}

func (ktdm *KubernetesTurndownManager) ScaleDownCluster() error {
	// 1. Start by finding all the nodes that Kubernetes is using
	nodes, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	// 2a. Suspend All Cron Jobs
	err = ktdm.SuspendJobs()
	if err != nil {
		return err
	}

	// 2b. Use provided to generate expected node pools associated with this cluster
	poolCollections, err := ktdm.provider.GetZoneNodePools(nodes)
	if err != nil {
		return err
	}
	nodePools, err := ktdm.provider.GetNodePools(poolCollections)
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

		klog.V(3).Infof("Draining Node: %s", n.Name)
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
	pools, err := ktdm.provider.GetNodePoolList()
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

	// Resume any suspended jobs
	err = ktdm.ResumeJobs()
	if err != nil {
		return err
	}

	return nil
}
