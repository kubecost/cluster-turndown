package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kubecost/cluster-turndown/v2/pkg/async"
	"github.com/kubecost/cluster-turndown/v2/pkg/cluster/helper"
	"github.com/kubecost/cluster-turndown/v2/pkg/cluster/patcher"

	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1beta1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Draininator is the type used to drain a specific kubernetes node. Much like
// the "drain" functionality provided by kubectl, this implementation will cordon
// the node, then aggressively force pod evictions, ignoring daemonset pods, and
// also evicting pods with local storage attached.
type Draininator struct {
	client             kubernetes.Interface
	node               string
	gracePeriodSeconds int64
	timeout            time.Duration
	force              bool
	ignoreDaemonSets   bool
	deleteLocalData    bool
	ignorePods         []string
	log                zerolog.Logger
}

// PodFilter definition which is used to determine which pods to evict from a node.
type PodFilter func(v1.Pod) (bool, error)

// Creates a new Draininator instance for a specific node.
func NewDraininator(client kubernetes.Interface, node string, ignorePods []string) *Draininator {
	return &Draininator{
		client: client,
		node:   node,
		// Aggressive defaults for now
		gracePeriodSeconds: 30,
		timeout:            20 * time.Minute,
		force:              true,
		deleteLocalData:    true,
		ignoreDaemonSets:   true,
		ignorePods:         ignorePods,
		log:                log.With().Str("component", "Draininator").Logger(),
	}
}

// Cordons the node, then evicts pods from the node that qualify.
func (d *Draininator) Drain() error {
	d.log.Info().Msgf("Draining Node: %s", d.node)
	err := d.CordonNode()
	if err != nil {
		return fmt.Errorf("cordoning node '%s': %w", d.node, err)
	}

	err = d.DeletePodsOnNode()
	if err != nil {
		return fmt.Errorf("deleting Pods on node '%s': %w", d.node, err)
	}

	d.log.Info().Msgf("Node: %s was Drained Successfully", d.node)
	return nil
}

func (d *Draininator) CordonNode() error {
	d.log.Info().Msgf("  Cordoning Node: %s", d.node)

	node, err := d.client.CoreV1().Nodes().Get(context.TODO(), d.node, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if node.Spec.Unschedulable {
		return nil
	}

	_, err = patcher.PatchNode(d.client, *node, func(n *v1.Node) error {
		n.Spec.Unschedulable = true
		return nil
	})

	return err
}

// Deletes or evicts the pods on the node that qualify for eviction
func (d *Draininator) DeletePodsOnNode() error {
	pods, err := d.podsToDelete()
	if err != nil {
		return fmt.Errorf("retrieving pods to delete: %w", err)
	}

	if pods == nil || len(pods) == 0 {
		d.log.Info().Msg("  There are no pods to evict on the drained node.")
		return nil
	}

	d.log.Info().Msgf("  Found %d pods to be deleted...", len(pods))

	policyGroupVersion, err := IsEvictionAvailable(d.client)
	if err != nil {
		return fmt.Errorf("checking if eviction is available: %w", err)
	}

	// Evict if the API is available
	if len(policyGroupVersion) > 0 {
		return d.evictPods(pods, policyGroupVersion)
	}

	// Otherwise, delete the pods
	return d.deletePods(pods)
}

// Creates a field selector which uses the pod.spec.nodeName to match to the correct node
func podNodeSelector(node string) string {
	selectorSet := fields.Set{"spec.nodeName": node}

	return fields.SelectorFromSet(selectorSet).String()
}

// Finds pods running on the current node, then uses filters to recognize whether or not
// eviction is possible.
func (d *Draininator) podsToDelete() ([]v1.Pod, error) {
	listOptions := metav1.ListOptions{
		FieldSelector: podNodeSelector(d.node),
	}
	allPods, err := d.client.CoreV1().Pods(metav1.NamespaceAll).List(context.TODO(), listOptions)
	if err != nil {
		return nil, fmt.Errorf("listing all Pods: %w", err)
	}

	var podsToDelete []v1.Pod
	var podErrors []error

	podFilters := []PodFilter{
		d.daemonSetFilter,
		d.mirrorFilter,
		d.localStorageFilter,
		d.unreplicatedFilter,
		d.autoscalerFilter,
		d.ignoredPodFilter,
	}
	for _, pod := range allPods.Items {
		deletable := true
		for _, filter := range podFilters {
			ok, err := filter(pod)
			if err != nil {
				podErrors = append(podErrors, err)
			}
			deletable = deletable && ok
		}
		if deletable {
			podsToDelete = append(podsToDelete, pod)
		}
	}

	if len(podErrors) > 0 {
		var errMsg string
		for _, err := range podErrors {
			errMsg += fmt.Sprintf("%s\n", err.Error())
		}
		return nil, errors.New(errMsg)
	}
	return podsToDelete, nil
}

// PodFilter for daemon sets
func (d *Draininator) daemonSetFilter(pod v1.Pod) (bool, error) {
	// Determine if there is an underlying controller for the pod
	controllerRef := metav1.GetControllerOf(&pod)
	if controllerRef == nil || controllerRef.Kind != "DaemonSet" {
		return true, nil
	}

	// Check to see if that controller is a daemonset
	if _, err := d.client.AppsV1().DaemonSets(pod.Namespace).Get(context.TODO(), controllerRef.Name, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) && d.force {
			d.log.Info().Msgf("pod %s.%s is controlled by a DaemonSet but the DaemonSet is not found", pod.Namespace, pod.Name)
			return true, nil
		}
		return false, err
	}

	if !d.ignoreDaemonSets {
		return false, fmt.Errorf("pod %s.%s is controlled by a DaemonSet, node cannot be drained.", pod.Namespace, pod.Name)
	}

	d.log.Info().Msgf("  Pod %s.%s is controlled by a DaemonSet. Ignoring.", pod.Namespace, pod.Name)
	return false, nil
}

// PodFilter to determine which pods are kube system mirrors
func (d *Draininator) mirrorFilter(pod v1.Pod) (bool, error) {
	if _, found := pod.ObjectMeta.Annotations[v1.MirrorPodAnnotationKey]; found {
		d.log.Info().Msgf("%s.%s is a mirror pod, it won't be deleted", pod.Namespace, pod.Name)
		return false, nil
	}
	return true, nil
}

// PodFilter for cluster-autoscaler.kubernetes.io/safe-to-evict annotation
func (d *Draininator) autoscalerFilter(pod v1.Pod) (bool, error) {
	enabled, found := pod.ObjectMeta.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict"]
	if found && enabled == "false" {
		d.log.Info().Msgf("%s.%s is required for autoscaling, it won't be deleted", pod.Namespace, pod.Name)
		return false, nil
	}

	return true, nil
}

// ignores pods by name specified in the list provided during instantiation
func (d *Draininator) ignoredPodFilter(pod v1.Pod) (bool, error) {
	for _, ignoredPod := range d.ignorePods {
		if strings.EqualFold(ignoredPod, pod.Name) {
			d.log.Info().Msgf("%s is the current pod, it won't be deleted", pod.Name)
			return false, nil
		}
	}

	return true, nil
}

// PodFilter for localStorage
func (d *Draininator) localStorageFilter(pod v1.Pod) (bool, error) {
	localStorage := false
	for _, volume := range pod.Spec.Volumes {
		if volume.EmptyDir != nil {
			localStorage = true
			break
		}
	}
	if !localStorage {
		return true, nil
	}
	if !d.deleteLocalData {
		return false, fmt.Errorf("pod %s.%s has local storage, node cannot be drained.", pod.Namespace, pod.Name)
	}

	d.log.Info().Msgf("  Pod %s.%s has local storage. Force removing...", pod.Namespace, pod.Name)
	return true, nil
}

// PodFilter which locates pods which do not have a controller. These pods will be evicted prior to draining.
func (d *Draininator) unreplicatedFilter(pod v1.Pod) (bool, error) {
	if pod.Status.Phase == v1.PodSucceeded || pod.Status.Phase == v1.PodFailed {
		return true, nil
	}

	// Locate a controller: deployment, replicaset, etc...
	controllerRef := metav1.GetControllerOf(&pod)
	if controllerRef != nil {
		return true, nil
	}
	if !d.force {
		return false, fmt.Errorf("pod %s.%s is unreplicated, node cannot be drained (set force=true to drain)", pod.Namespace, pod.Name)
	}

	d.log.Info().Msgf("  Pod %s.%s does not have a controller. Force removing...", pod.Namespace, pod.Name)
	return true, nil
}

// Determine whether or not eviction is available. If so, return the policy group version
func IsEvictionAvailable(client kubernetes.Interface) (string, error) {
	discoveryClient := client.Discovery()
	groupList, err := discoveryClient.ServerGroups()
	if err != nil {
		return "", err
	}

	var foundPolicyGroup bool = false
	var policyGroupVersion string
	for _, group := range groupList.Groups {
		if group.Name == "policy" {
			foundPolicyGroup = true
			policyGroupVersion = group.PreferredVersion.GroupVersion
			break
		}
	}

	if !foundPolicyGroup {
		return "", nil
	}

	resourceList, err := discoveryClient.ServerResourcesForGroupVersion("v1")
	if err != nil {
		return "", err
	}

	for _, resource := range resourceList.APIResources {
		if resource.Name == "pods/eviction" && resource.Kind == "Eviction" {
			return policyGroupVersion, nil
		}
	}

	return "", nil
}

// Deletes the pods and waits until they're deleted
func (d *Draininator) deletePods(pods []v1.Pod) error {
	globalTimeout := d.timeout

	wc := async.NewWaitChannel()
	wc.Add(len(pods))

	for _, pod := range pods {
		d.log.Debug().Msgf("  Deleting Pod %s.%s", pod.Namespace, pod.Name)
		deleteContext, deleteCancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer deleteCancel()
		err := d.client.CoreV1().Pods(pod.Namespace).Delete(deleteContext, pod.Name, metav1.DeleteOptions{
			GracePeriodSeconds: &d.gracePeriodSeconds,
		})
		if err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("deleting Pod '%s.%s': %w", pod.Namespace, pod.Name, err)
		}

		go func(p v1.Pod) {
			defer wc.Done()

			err := helper.WaitUntilPodDeleted(d.client, p, 5*time.Second, globalTimeout)
			if err != nil {
				d.log.Error().Msgf("Failed to wait for pod deletion: %s", err.Error())
			} else {
				d.log.Info().Msgf("  Pod %s.%s is deleted.", p.Namespace, p.Name)
			}
		}(pod)
	}

	select {
	case <-wc.Wait():
		return nil
	case <-time.After(globalTimeout):
		return fmt.Errorf("Timed out while attempting to delete pods.")
	}
}

// Creates a new Eviction for the pod
func podEvictionFor(pod *v1.Pod, policyGroupVersion string, gracePeriodSeconds int64) *policyv1.Eviction {
	return &policyv1.Eviction{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Eviction",
			APIVersion: policyGroupVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: pod.Namespace,
			Name:      pod.Name,
		},
		DeleteOptions: &metav1.DeleteOptions{
			GracePeriodSeconds: &gracePeriodSeconds,
		},
	}
}

func (d *Draininator) evictPods(pods []v1.Pod, policyGroupVersion string) error {
	globalTimeout := d.timeout

	wc := async.NewWaitChannel()
	wc.Add(len(pods))

	for _, p := range pods {
		go func(pod v1.Pod) {
			var err error
			defer wc.Done()

			// Attempt to evict the pod, retrying if TooManyRequests received
			for {
				eviction := podEvictionFor(&pod, policyGroupVersion, d.gracePeriodSeconds)
				err = d.client.PolicyV1beta1().Evictions(eviction.Namespace).Evict(context.TODO(), eviction)
				if err == nil {
					break
				}

				// Pod already removed, complete
				if k8serrors.IsNotFound(err) {
					return
				}

				// Error other than TooManyRequests
				if !k8serrors.IsTooManyRequests(err) {
					d.log.Error().Msgf("Error Evicting Pod: %s - %s", pod.Name, err.Error())
					return
				}

				// Received a TooManyRequests from kubernetes, wait, then try again
				time.Sleep(5 * time.Second)
			}

			err = helper.WaitUntilPodDeleted(d.client, pod, 5*time.Second, globalTimeout)
			if err != nil {
				d.log.Error().Msgf("Failed to wait for pod deletion: %s", err.Error())
			} else {
				d.log.Info().Msgf("  Pod %s.%s is deleted.", pod.Namespace, pod.Name)
			}
		}(p)
	}

	select {
	case <-wc.Wait():
		return nil
	case <-time.After(globalTimeout):
		return fmt.Errorf("Timed out while attempting to delete pods.")
	}
}
