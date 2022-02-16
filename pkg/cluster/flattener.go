package cluster

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kubecost/cluster-turndown/pkg/cluster/patcher"
	"github.com/kubecost/cluster-turndown/pkg/logging"

	appsv1 "k8s.io/api/apps/v1"
	v1b1 "k8s.io/api/batch/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"k8s.io/klog"
)

const (
	ClusterAutoScalerSafeEvict    = "cluster-autoscaler.kubernetes.io/safe-to-evict"
	KubecostTurnDownReplicas      = "kubecost.kubernetes.io/turn-down-replicas"
	KubecostTurnDownRollout       = "kubecost.kubernetes.io/turn-down-rollout"
	KubecostTurnDownSafeEvictFlag = "kubecost.kubernetes.io/safe-evict"
	KubecostTurnDownJobSuspend    = "kubecost.kubernetes.io/job-suspend"
)

// Flattener is the type used to set specific kubernetes annotations and configurations\
// to entice the autoscaler to downscale the cluster.
type Flattener struct {
	client          kubernetes.Interface
	omitDeployments []string
	log             logging.NamedLogger
}

// Creates a new Draininator instance for a specific node.
func NewFlattener(client kubernetes.Interface, omitDeployments []string) *Flattener {
	return &Flattener{
		client:          client,
		omitDeployments: omitDeployments,
		log:             logging.NamedLogger("Flattener"),
	}
}

// Flatten reduces deployments to single replicas, updates rollout strategies and pod
// disruption budgets to one, and sets all pods to "safe for eviction". This mode
// is used to reduce node resources such that the autoscaler will reduce node counts
// on a cluster as low as possible.
func (d *Flattener) Flatten() error {
	d.log.SLog("Starting to Flatten All Deployments...")
	err := d.FlattenDeployments()
	if err != nil {
		return err
	}

	d.log.SLog("Starting to Flatten All DaemonSets...")
	err = d.FlattenDaemonSets()
	if err != nil {
		return err
	}

	d.log.SLog("Starting to Suspend All Jobs...")
	err = d.SuspendJobs()
	if err != nil {
		return err
	}

	return nil
}

func (d *Flattener) Expand() error {
	d.log.SLog("Starting to Expand All Deployments...")
	err := d.ExpandDeployments()
	if err != nil {
		return err
	}

	d.log.SLog("Starting to Expand All DaemonSets...")
	err = d.ExpandDaemonSets()
	if err != nil {
		return err
	}

	d.log.SLog("Starting to Resume All Jobs...")
	err = d.ResumeJobs()
	if err != nil {
		return err
	}

	return nil
}

// Test to determine if any of the cluster components have been flattened.
func (d *Flattener) IsClusterFlattened() bool {
	deployments, err := d.client.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		d.log.Warn("Failed to fetch deployments: %s", err.Error())
	} else {
		for _, deployment := range deployments.Items {
			if d.isOmitted(&deployment) {
				continue
			}
			if deployment.Annotations == nil {
				continue
			}

			if _, ok := deployment.Annotations[KubecostTurnDownSafeEvictFlag]; ok {
				return true
			}
			if _, ok := deployment.Annotations[KubecostTurnDownReplicas]; ok {
				return true
			}
			if _, ok := deployment.Annotations[KubecostTurnDownRollout]; ok {
				return true
			}
		}
	}

	daemonSets, err := d.client.AppsV1().DaemonSets("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		d.log.Warn("Failed to fetch daemonsets: %s", err.Error())
	} else {
		for _, daemonSet := range daemonSets.Items {
			if daemonSet.Annotations == nil {
				continue
			}

			if _, ok := daemonSet.Annotations[KubecostTurnDownSafeEvictFlag]; ok {
				return true
			}
		}
	}

	jobsList, err := d.client.BatchV1beta1().CronJobs("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		d.log.Warn("Failed to fetch jobs: %s", err.Error())
	} else {
		for _, job := range jobsList.Items {
			if job.Annotations == nil {
				continue
			}

			if _, ok := job.Annotations[KubecostTurnDownJobSuspend]; ok {
				return true
			}
		}
	}

	// Did not hit any valid testable annotation for flattening
	return false
}

func (d *Flattener) isOmitted(deployment *appsv1.Deployment) bool {
	for _, d := range d.omitDeployments {
		if d == deployment.Name {
			return true
		}
	}

	return false
}

func (d *Flattener) FlattenDeployments() error {
	deployments, err := d.client.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, deployment := range deployments.Items {
		if d.isOmitted(&deployment) {
			continue
		}

		err := d.FlattenDeployment(deployment)
		if err != nil {
			d.log.SLog("Failed to flatten deployment: %s", deployment.Name)
		}
	}

	return nil
}

func (d *Flattener) FlattenDaemonSets() error {
	daemonSets, err := d.client.AppsV1().DaemonSets("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, daemonSet := range daemonSets.Items {
		err := d.FlattenDaemonSet(daemonSet)
		if err != nil {
			d.log.SLog("Failed to flatten DaemonSet: %s", daemonSet.Name)
		}
	}

	return nil
}

func (d *Flattener) SuspendJobs() error {
	jobsList, err := d.client.BatchV1beta1().CronJobs("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, job := range jobsList.Items {
		err := d.SuspendJob(job)
		if err != nil {
			d.log.SLog("Failed to suspend CronJob: %s", err.Error())
		}
	}

	return nil
}

// Flatten
func (d *Flattener) FlattenDeployment(dep appsv1.Deployment) error {
	_, err := patcher.PatchDeployment(d.client, dep, func(deployment *appsv1.Deployment) error {
		updateEvictFlag := false
		updateReplicas := false
		updateRollout := false

		if deployment.Namespace == "kube-system" {
			updateEvictFlag = d.setSafeEvict(deployment)
		} else {
			updateReplicas = d.zeroOutReplicas(deployment)
			updateRollout = d.zeroOutRollingUpdate(deployment)
		}

		// No updates -- Early Return
		if !updateEvictFlag && !updateReplicas && !updateRollout {
			return patcher.NoUpdates
		}

		return nil
	})

	return err
}

func (d *Flattener) ExpandDeployment(dep appsv1.Deployment) error {
	_, err := patcher.PatchDeployment(d.client, dep, func(deployment *appsv1.Deployment) error {
		updateEvictFlag := false
		updateReplicas := false
		updateRollout := false

		if deployment.Namespace == "kube-system" {
			updateEvictFlag = d.resetSafeEvict(deployment)
		} else {
			updateReplicas = d.resetReplicas(deployment)
			updateRollout = d.resetRollingUpdate(deployment)
		}

		// No updates
		if !updateEvictFlag && !updateReplicas && !updateRollout {
			return patcher.NoUpdates
		}

		return nil
	})

	return err
}

func (d *Flattener) FlattenDaemonSet(ds appsv1.DaemonSet) error {
	_, err := patcher.PatchDaemonSet(d.client, ds, func(daemonset *appsv1.DaemonSet) error {
		updateEvictFlag := d.setSafeEvictDaemonSet(daemonset)

		if !updateEvictFlag {
			return patcher.NoUpdates
		}

		return nil
	})

	return err
}

func (d *Flattener) ExpandDaemonSet(ds appsv1.DaemonSet) error {
	_, err := patcher.PatchDaemonSet(d.client, ds, func(daemonset *appsv1.DaemonSet) error {
		updateEvictFlag := d.resetSafeEvictDaemonSet(daemonset)

		if !updateEvictFlag {
			return patcher.NoUpdates
		}

		return nil
	})

	return err
}

func (d *Flattener) SuspendJob(cronJob v1b1.CronJob) error {
	_, err := patcher.PatchCronJob(d.client, cronJob, func(job *v1b1.CronJob) error {
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

		return nil
	})

	return err
}

// Sets the deployment pods to a safe-evict state, updates annotation flags
func (d *Flattener) ResumeJob(cronJob v1b1.CronJob) error {
	_, err := patcher.PatchCronJob(d.client, cronJob, func(job *v1b1.CronJob) error {
		var suspend bool = false
		var err error
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
		return nil
	})

	return err
}

func (d *Flattener) ExpandDeployments() error {
	deployments, err := d.client.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, deployment := range deployments.Items {
		if d.isOmitted(&deployment) {
			continue
		}

		err := d.ExpandDeployment(deployment)
		if err != nil {
			d.log.SLog("Failed to expand deployment: %s", deployment.Name)
		}
	}

	return nil
}

func (d *Flattener) ExpandDaemonSets() error {
	daemonSets, err := d.client.AppsV1().DaemonSets("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, daemonSet := range daemonSets.Items {
		err := d.ExpandDaemonSet(daemonSet)
		if err != nil {
			d.log.SLog("Failed to flatten DaemonSet: %s", daemonSet.Name)
		}
	}

	return nil
}

func (d *Flattener) ResumeJobs() error {
	jobsList, err := d.client.BatchV1beta1().CronJobs("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, job := range jobsList.Items {
		err := d.ResumeJob(job)
		if err != nil {
			d.log.SLog("Failed to resume CronJob: %s", err.Error())
		}
	}

	return nil
}

// Sets the deployment pods to a safe-evict state, updates annotation flags
func (d *Flattener) setSafeEvict(deployment *appsv1.Deployment) bool {
	var previousValue string
	if deployment.Spec.Template.Annotations != nil {
		previousValue = deployment.Spec.Template.Annotations[ClusterAutoScalerSafeEvict]
	}

	// Set the Safe-Evict flag for the pods
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{
			ClusterAutoScalerSafeEvict: "true",
		}
	} else {
		deployment.Spec.Template.Annotations[ClusterAutoScalerSafeEvict] = "true"
	}

	// If there wasn't a previous value set, no need to set flag
	if previousValue == "" {
		return true
	}

	if deployment.Annotations == nil {
		deployment.Annotations = map[string]string{
			KubecostTurnDownSafeEvictFlag: previousValue,
		}
	} else {
		deployment.Annotations[KubecostTurnDownSafeEvictFlag] = previousValue
	}

	return true
}

// Sets the deployment replicas to 0 and stores the previous value in the deployment
// annotation
func (d *Flattener) zeroOutReplicas(deployment *appsv1.Deployment) bool {
	if *deployment.Spec.Replicas == 0 {
		return false
	}

	var zero int32 = 0
	oldReplicas := deployment.Spec.Replicas
	deployment.Spec.Replicas = &zero

	// Set annotation with previous value
	if deployment.Annotations == nil {
		deployment.Annotations = map[string]string{
			KubecostTurnDownReplicas: fmt.Sprintf("%d", *oldReplicas),
		}
	} else {
		deployment.Annotations[KubecostTurnDownReplicas] = fmt.Sprintf("%d", *oldReplicas)
	}

	return true
}

func (d *Flattener) zeroOutRollingUpdate(deployment *appsv1.Deployment) bool {
	rollingUpdate := deployment.Spec.Strategy.RollingUpdate
	if rollingUpdate == nil {
		return false
	}

	maxUnavailable := rollingUpdate.MaxUnavailable
	if maxUnavailable == nil {
		newValue := intstr.FromInt(1)
		rollingUpdate.MaxUnavailable = &newValue
	} else {
		rollingUpdate.MaxUnavailable.Type = intstr.Int
		rollingUpdate.MaxUnavailable.IntVal = 1
	}

	// Set annotation with previous value
	var toWrite string
	if maxUnavailable == nil {
		toWrite = ""
	} else {
		toWrite = maxUnavailable.String()
	}

	if deployment.Annotations == nil {
		deployment.Annotations = map[string]string{
			KubecostTurnDownRollout: fmt.Sprintf("%s", toWrite),
		}
	} else {
		deployment.Annotations[KubecostTurnDownRollout] = fmt.Sprintf("%s", toWrite)
	}

	return true
}

// Sets the deployment pods to a safe-evict state, updates annotation flags
func (d *Flattener) resetSafeEvict(deployment *appsv1.Deployment) bool {
	if deployment.Annotations == nil {
		return false
	}

	// If there wasn't an entry, remove the pod safe evict flag
	safeEvictEntry, ok := deployment.Annotations[KubecostTurnDownSafeEvictFlag]
	if !ok {
		if deployment.Spec.Template.Annotations != nil {
			delete(deployment.Spec.Template.Annotations, ClusterAutoScalerSafeEvict)
		}

		return true
	}

	// Otherwise, Delete Deployment Annotation
	delete(deployment.Annotations, KubecostTurnDownSafeEvictFlag)

	// Reset to the previous value
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{
			ClusterAutoScalerSafeEvict: safeEvictEntry,
		}
	} else {
		deployment.Spec.Template.Annotations[ClusterAutoScalerSafeEvict] = safeEvictEntry
	}

	return true
}

// Sets the deployment replicas to 0 and stores the previous value in the deployment
// annotation
func (d *Flattener) resetReplicas(deployment *appsv1.Deployment) bool {
	if deployment.Annotations == nil {
		return false
	}

	replicasEntry, ok := deployment.Annotations[KubecostTurnDownReplicas]
	if !ok {
		return false
	}

	replicas, err := strconv.ParseInt(replicasEntry, 10, 32)
	if err != nil {
		klog.V(1).Infof("Failed to parse replicas annotation: %s", err.Error())
		return false
	}

	var numReplicas int32 = int32(replicas)
	d.log.SLog("Setting Replicas for %s to %d", deployment.Name, numReplicas)

	delete(deployment.Annotations, KubecostTurnDownReplicas)
	deployment.Spec.Replicas = &numReplicas

	return true
}

func (d *Flattener) resetRollingUpdate(deployment *appsv1.Deployment) bool {
	if deployment.Annotations == nil {
		return false
	}

	maxUnavailableEntry, ok := deployment.Annotations[KubecostTurnDownRollout]
	if !ok {
		return false
	}

	maxUnavailable := intstr.Parse(maxUnavailableEntry)
	d.log.SLog("Setting Rollout Max Unavailable for %s to %s", deployment.Name, maxUnavailable.String())

	delete(deployment.Annotations, KubecostTurnDownRollout)

	rollingUpdate := deployment.Spec.Strategy.RollingUpdate
	if rollingUpdate == nil {
		rollingUpdate = &appsv1.RollingUpdateDeployment{
			MaxUnavailable: &maxUnavailable,
		}
	} else {
		rollingUpdate.MaxUnavailable = &maxUnavailable
	}

	deployment.Spec.Strategy.RollingUpdate = rollingUpdate

	return true
}

// Sets the daemonset pods to a safe-evict state, updates annotation flags
func (d *Flattener) setSafeEvictDaemonSet(daemonset *appsv1.DaemonSet) bool {
	var previousValue string
	if daemonset.Spec.Template.Annotations != nil {
		previousValue = daemonset.Spec.Template.Annotations[ClusterAutoScalerSafeEvict]
	}

	// Set the Safe-Evict flag for the pods
	if daemonset.Spec.Template.Annotations == nil {
		daemonset.Spec.Template.Annotations = map[string]string{
			ClusterAutoScalerSafeEvict: "true",
		}
	} else {
		daemonset.Spec.Template.Annotations[ClusterAutoScalerSafeEvict] = "true"
	}

	// If there wasn't a previous value set, no need to set flag
	if previousValue == "" {
		return true
	}

	if daemonset.Annotations == nil {
		daemonset.Annotations = map[string]string{
			KubecostTurnDownSafeEvictFlag: previousValue,
		}
	} else {
		daemonset.Annotations[KubecostTurnDownSafeEvictFlag] = previousValue
	}

	return true
}

// Sets the daemonset pods to a safe-evict state, updates annotation flags
func (d *Flattener) resetSafeEvictDaemonSet(daemonset *appsv1.DaemonSet) bool {
	if daemonset.Annotations == nil {
		return false
	}

	// If there wasn't an entry, remove the pod safe evict flag
	safeEvictEntry, ok := daemonset.Annotations[KubecostTurnDownSafeEvictFlag]
	if !ok {
		if daemonset.Spec.Template.Annotations != nil {
			delete(daemonset.Spec.Template.Annotations, ClusterAutoScalerSafeEvict)
		}

		return true
	}

	// Otherwise, Delete daemonset Annotation
	delete(daemonset.Annotations, KubecostTurnDownSafeEvictFlag)

	// Reset to the previous value
	if daemonset.Spec.Template.Annotations == nil {
		daemonset.Spec.Template.Annotations = map[string]string{
			ClusterAutoScalerSafeEvict: safeEvictEntry,
		}
	} else {
		daemonset.Spec.Template.Annotations[ClusterAutoScalerSafeEvict] = safeEvictEntry
	}

	return true
}
