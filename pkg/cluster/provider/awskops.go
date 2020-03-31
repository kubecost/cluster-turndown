package provider

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/yaml"
)

//--------------------------------------------------------------------------
//  Kops Tags
//--------------------------------------------------------------------------

const (
	KopsClusterTag       = "KubernetesCluster"
	KopsClusterNameTag   = "Name"
	KopsAutoScalerIGTag  = "k8s.io/cluster-autoscaler/node-template/label/kops.k8s.io/instancegroup"
	KopsRoleNodeTag      = "k8s.io/role/node"
	KopsInstanceGroupTag = "kops.k8s.io/instancegroup"
	KopsEnabled          = "1"
)

//--------------------------------------------------------------------------
//  Kops InstanceGroup Stub
//--------------------------------------------------------------------------

// Kops InstanceGroup Spec
type KopsInstanceGroupSpec struct {
	Image       string            `yaml:"image"`
	MachineType string            `yaml:"machineType"`
	MaxSize     int64             `yaml:"maxSize"`
	MinSize     int64             `yaml:"minSize"`
	NodeLabels  map[string]string `yaml:"nodeLabels"`
	Role        string            `yaml:"role"`
	Subnets     []string          `yaml:"subnets"`
}

// Kops InstanceGroup MetaData
type KopsInstanceGroupMetaData struct {
	CreationTimestamp metav1.Time       `yaml:"creationTimestamp"`
	Labels            map[string]string `yaml:"labels"`
	Name              string            `yaml:"name"`
}

// Kops InstanceGroup
type KopsInstanceGroup struct {
	APIVersion string                     `yaml:"apiVersion"`
	Kind       string                     `yaml:"kind"`
	Metadata   *KopsInstanceGroupMetaData `yaml:"metadata"`
	Spec       *KopsInstanceGroupSpec     `yaml:"spec"`
}

// Creates a new kops instance group with the provided parameters
func newKopsInstanceGroup(clusterName, groupName, image, machineType string, minSize, maxSize int64, subnets []string) *KopsInstanceGroup {
	return &KopsInstanceGroup{
		APIVersion: "kops.k8s.io/v1alpha2",
		Kind:       "InstanceGroup",
		Metadata: &KopsInstanceGroupMetaData{
			CreationTimestamp: metav1.NewTime(time.Now()),
			Labels: map[string]string{
				"kops.k8s.io/cluster": clusterName,
			},
			Name: groupName,
		},
		Spec: &KopsInstanceGroupSpec{
			Image:       image,
			Role:        "Node",
			MachineType: machineType,
			MinSize:     minSize,
			MaxSize:     maxSize,
			NodeLabels: map[string]string{
				"kops.k8s.io/instancegroup": groupName,
			},
			Subnets: subnets,
		},
	}
}

//--------------------------------------------------------------------------
//  Helper Methods for Kops
//--------------------------------------------------------------------------

// GetKopsInstanceGroupYaml templates the provided parameter into an InstanceGroup, then encodes into yaml
func GetKopsInstanceGroupYaml(clusterName, groupName, image, machineType string, minSize, maxSize int64, subnets []string) (string, error) {
	ig := newKopsInstanceGroup(clusterName, groupName, image, machineType, minSize, maxSize, subnets)

	bytes, err := yaml.Marshal(ig)
	if err != nil {
		return "", err
	}

	return string(bytes), nil
}

// GetKopsInstanceGroupTags returns a map of the default kops tags for an autoscaling group
func GetKopsInstanceGroupTags(clusterName, groupName string) map[string]string {
	return map[string]string{
		KopsClusterTag:       clusterName,
		KopsClusterNameTag:   fmt.Sprintf("%s.%s", groupName, clusterName),
		KopsAutoScalerIGTag:  groupName,
		KopsRoleNodeTag:      KopsEnabled,
		KopsInstanceGroupTag: groupName,
	}
}
