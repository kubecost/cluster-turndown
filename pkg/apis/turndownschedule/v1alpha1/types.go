package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// TurndownSchedule is a specification for a TurndownSchedule resource
type TurndownSchedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TurndownScheduleSpec   `json:"spec"`
	Status TurndownScheduleStatus `json:"status"`
}

// TurndownScheduleSpec is the spec for a TurndownSchedule resource
type TurndownScheduleSpec struct {
	Start  metav1.Time `json:"start"`
	End    metav1.Time `json:"end"`
	Repeat string      `json:"repeat"`
}

// TurndownScheduleStatus is the status for a TurndownSchedule resource
type TurndownScheduleStatus struct {
	State             string            `json:"state"`
	LastUpdated       metav1.Time       `json:"lastUpdated"`
	Current           string            `json:"current,omitempty"`
	ScaleDownID       string            `json:"scaleDownId,omitempty"`
	ScaleDownTime     metav1.Time       `json:"nextScaleDownTime,omitempty"`
	ScaleDownMetadata map[string]string `json:"scaleDownMetadata,omitempty"`
	ScaleUpID         string            `json:"scaleUpID,omitempty"`
	ScaleUpTime       metav1.Time       `json:"nextScaleUpTime,omitempty"`
	ScaleUpMetadata   map[string]string `json:"scaleUpMetadata,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// TurndownScheduleList is a list of TurndownSchedule resources
type TurndownScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []TurndownSchedule `json:"items"`
}
