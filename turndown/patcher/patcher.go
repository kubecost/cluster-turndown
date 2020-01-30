package patcher

import (
	"encoding/json"
	"errors"

	appsv1 "k8s.io/api/apps/v1"
	v1b1 "k8s.io/api/batch/v1beta1"
	v1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
)

const NoUpdatesReason = "NoUpdates"

type NodePatch = func(*v1.Node) error
type CronJobPatch = func(*v1b1.CronJob) error
type DaemonSetPatch = func(*appsv1.DaemonSet) error
type DeploymentPatch = func(*appsv1.Deployment) error

// Error to return to the patcher if there are no updates
var NoUpdates error = errors.New(NoUpdatesReason)

//
func IsNoUpdates(err error) bool {
	return err != nil && err.Error() == NoUpdatesReason
}

//
func PatchNode(c kubernetes.Interface, node v1.Node, patch NodePatch) (*v1.Node, error) {
	oldData, err := json.Marshal(node)
	if err != nil {
		return nil, err
	}

	err = patch(&node)
	if err != nil {
		if IsNoUpdates(err) {
			return &node, nil
		}

		return nil, err
	}

	newData, err := json.Marshal(node)
	p, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, node)
	if err != nil {
		return nil, err
	}

	return c.CoreV1().Nodes().Patch(node.Name, types.MergePatchType, p)
}

//
func PatchDeployment(c kubernetes.Interface, deployment appsv1.Deployment, patch DeploymentPatch) (*appsv1.Deployment, error) {
	ns, name := deployment.Namespace, deployment.Name

	oldData, err := json.Marshal(deployment)
	if err != nil {
		return nil, err
	}

	err = patch(&deployment)
	if err != nil {
		if IsNoUpdates(err) {
			return &deployment, nil
		}

		return nil, err
	}

	newData, err := json.Marshal(deployment)
	p, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, deployment)
	if err != nil {
		return nil, err
	}

	return c.AppsV1().Deployments(ns).Patch(name, types.MergePatchType, p)
}

//
func PatchDaemonSet(c kubernetes.Interface, daemonset appsv1.DaemonSet, patch DaemonSetPatch) (*appsv1.DaemonSet, error) {
	ns, name := daemonset.Namespace, daemonset.Name

	oldData, err := json.Marshal(daemonset)
	if err != nil {
		return nil, err
	}

	err = patch(&daemonset)
	if err != nil {
		if IsNoUpdates(err) {
			return &daemonset, nil
		}

		return nil, err
	}

	newData, err := json.Marshal(daemonset)
	p, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, daemonset)
	if err != nil {
		return nil, err
	}

	return c.AppsV1().DaemonSets(ns).Patch(name, types.MergePatchType, p)
}

//
func PatchCronJob(c kubernetes.Interface, cronJob v1b1.CronJob, patch CronJobPatch) (*v1b1.CronJob, error) {
	ns, name := cronJob.Namespace, cronJob.Name

	oldData, err := json.Marshal(cronJob)
	if err != nil {
		return nil, err
	}

	err = patch(&cronJob)
	if err != nil {
		if IsNoUpdates(err) {
			return &cronJob, nil
		}

		return nil, err
	}

	newData, err := json.Marshal(cronJob)
	p, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, cronJob)
	if err != nil {
		return nil, err
	}

	return c.BatchV1beta1().CronJobs(ns).Patch(name, types.MergePatchType, p)
}

//
func UpdateNodeLabel(c kubernetes.Interface, node v1.Node, labelKey string, labelValue string) (*v1.Node, error) {
	return PatchNode(c, node, func(n *v1.Node) error {
		if len(n.Labels) == 0 {
			n.Labels = map[string]string{
				labelKey: labelValue,
			}
		} else {
			n.Labels[labelKey] = labelValue
		}

		return nil
	})
}

//
func DeleteNodeLabel(c kubernetes.Interface, node v1.Node, labelKey string) (*v1.Node, error) {
	return PatchNode(c, node, func(n *v1.Node) error {
		if len(n.Labels) == 0 {
			return NoUpdates
		}

		delete(n.Labels, labelKey)

		return nil
	})
}
