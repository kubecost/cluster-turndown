package strategy

import (
	"fmt"

	"github.com/kubecost/kubecost-turndown/turndown/patcher"
	"github.com/kubecost/kubecost-turndown/turndown/provider"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
)

const (
	MasterNodeLabelKey = "node-role.kubernetes.io/master"
	NodeRoleLabelKey   = "kubernetes.io/role"

	SchedulerTolerationAnnotation = "scheduler.alpha.kubernetes.io/tolerations"
)

type StandardTurndownStrategy struct {
	client   kubernetes.Interface
	provider provider.ComputeProvider
}

func NewStandardTurndownStrategy(client kubernetes.Interface, provider provider.ComputeProvider) TurndownStrategy {
	return &StandardTurndownStrategy{
		client:   client,
		provider: provider,
	}
}

// On scale up, reverse master node labels and turndown deployment selector
func (mts *StandardTurndownStrategy) IsReversible() bool {
	return true
}

func (mts *StandardTurndownStrategy) TaintKey() string {
	return MasterNodeLabelKey
}

// This method will locate or create a node, apply a specific taint and
// label, and return the updated kubernetes Node instance.
func (ktdm *StandardTurndownStrategy) CreateOrGetHostNode() (*v1.Node, error) {
	if !ktdm.provider.IsServiceAccountKey() {
		return nil, fmt.Errorf("The current provider does not have a service account key set.")
	}

	// Locate the master node using role labels
	nodeList, err := ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: MasterNodeLabelKey,
	})
	if err != nil || len(nodeList.Items) == 0 {
		// Try an alternate selector in case the first fails
		nodeList, err = ktdm.client.CoreV1().Nodes().List(metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=master", NodeRoleLabelKey),
		})
		if err != nil {
			return nil, err
		}
		if len(nodeList.Items) == 0 {
			return nil, fmt.Errorf("Failed to locate master node in standard turndown strategy.")
		}
	}

	// Pick a master node
	masterNode := &nodeList.Items[0]

	// Patch and get the updated node
	return patcher.UpdateNodeLabel(ktdm.client, *masterNode, "kubecost-turndown-node", "true")
}

func (sts *StandardTurndownStrategy) UpdateDNS() error {
	// NOTE: This needs a bit more investigation. GKE appears to overwrite any modifications to the kube-dns
	// NOTE: deployment, so this might not actually work there. However, having to "allow" kube-dns to run on
	// NOTE: a master node is also quite strange.
	dns, err := sts.client.AppsV1().Deployments("kube-system").Get("kube-dns", metav1.GetOptions{})
	if err != nil {
		return err
	}

	_, err = patcher.PatchDeployment(sts.client, *dns, func(d *appsv1.Deployment) error {
		tolerationAnnotation := fmt.Sprintf(`[{"key":"CriticalAddonsOnly", "operator":"Exists"}, {"key":"%s", "operator":"Exists"}]`, MasterNodeLabelKey)

		if d.Annotations != nil {
			d.Annotations[SchedulerTolerationAnnotation] = tolerationAnnotation
		} else {
			d.Annotations = map[string]string{
				SchedulerTolerationAnnotation: tolerationAnnotation,
			}
		}

		if d.Spec.Template.Annotations != nil {
			d.Spec.Template.Annotations[SchedulerTolerationAnnotation] = tolerationAnnotation
		} else {
			d.Spec.Template.Annotations = map[string]string{
				SchedulerTolerationAnnotation: tolerationAnnotation,
			}
		}

		d.Spec.Template.Spec.Tolerations = append(d.Spec.Template.Spec.Tolerations, v1.Toleration{
			Key:      MasterNodeLabelKey,
			Operator: v1.TolerationOpExists,
		})

		return nil
	})

	return err
}

func (sts *StandardTurndownStrategy) ReverseHostNode() error {
	// Locate the master node using role labels
	nodeList, err := sts.client.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: MasterNodeLabelKey,
	})
	if err != nil || len(nodeList.Items) == 0 {
		// Try an alternate selector in case the first fails
		nodeList, err = sts.client.CoreV1().Nodes().List(metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=master", NodeRoleLabelKey),
		})
		if err != nil {
			return err
		}
		if len(nodeList.Items) == 0 {
			return fmt.Errorf("Failed to locate master node in standard turndown strategy.")
		}
	}

	// Pick a master node
	masterNode := &nodeList.Items[0]

	// Patch and get the updated node
	_, err = patcher.DeleteNodeLabel(sts.client, *masterNode, "kubecost-turndown-node")

	dns, err := sts.client.AppsV1().Deployments("kube-system").Get("kube-dns", metav1.GetOptions{})
	if err != nil {
		return err
	}

	_, err = patcher.PatchDeployment(sts.client, *dns, func(d *appsv1.Deployment) error {
		tolerationAnnotation := `[{"key":"CriticalAddonsOnly", "operator":"Exists"}]`

		if d.Annotations != nil {
			d.Annotations[SchedulerTolerationAnnotation] = tolerationAnnotation
		} else {
			d.Annotations = map[string]string{
				SchedulerTolerationAnnotation: tolerationAnnotation,
			}
		}

		if d.Spec.Template.Annotations != nil {
			d.Spec.Template.Annotations[SchedulerTolerationAnnotation] = tolerationAnnotation
		} else {
			d.Spec.Template.Annotations = map[string]string{
				SchedulerTolerationAnnotation: tolerationAnnotation,
			}
		}

		d.Spec.Template.Spec.Tolerations = []v1.Toleration{}

		return nil
	})

	sts.client.CoreV1().Pods("kube-system").DeleteCollection(&metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: "k8s-app=kube-dns",
	})

	return err
}
