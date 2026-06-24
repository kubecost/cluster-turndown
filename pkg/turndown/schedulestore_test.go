package turndown

import (
	"testing"
	"time"
	"context"

	v1alpha1 "github.com/kubecost/cluster-turndown/v2/pkg/apis/turndownschedule/v1alpha1"
	"github.com/kubecost/cluster-turndown/v2/pkg/generated/clientset/versioned/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestKubernetesScheduleStore tests basic operations of the schedule store
func TestKubernetesScheduleStore(t *testing.T) {
	// Create a fake clientset
	fakeClient := fake.NewSimpleClientset()
	
	// Create a KubernetesScheduleStore
	store := NewKubernetesScheduleStore(fakeClient)
	
	// Test getting a schedule when none exists
	_, err := store.GetSchedule()
	if err == nil {
		t.Errorf("Expected error when getting non-existent schedule, got nil")
	}
	
	// Create a test TurndownSchedule CR
	now := time.Now()
	testSchedule := &v1alpha1.TurndownSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-schedule",
		},
		Spec: v1alpha1.TurndownScheduleSpec{
			Start:  metav1.NewTime(now.Add(1 * time.Hour)),
			End:    metav1.NewTime(now.Add(2 * time.Hour)),
			Repeat: TurndownJobRepeatNone,
		},
		Status: v1alpha1.TurndownScheduleStatus{
			State:       ScheduleStateSuccess,
			Current:     TurndownJobTypeScaleDown,
			ScaleDownID: "sd-12345",
			ScaleUpID:   "su-12345",
			ScaleDownTime: metav1.NewTime(now.Add(1 * time.Hour)),
			ScaleUpTime:   metav1.NewTime(now.Add(2 * time.Hour)),
			LastUpdated: metav1.NewTime(now),
		},
	}
	
	// Create the schedule CR
	_, err = fakeClient.KubecostV1alpha1().TurndownSchedules().Create(
		context.TODO(),
		testSchedule,
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to create test schedule: %v", err)
	}
	
	// Verify store.Complete changes the status to completed
	store.Complete()
	
	completedSchedule, err := fakeClient.KubecostV1alpha1().TurndownSchedules().Get(
		context.TODO(),
		testSchedule.Name,
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to get completed schedule: %v", err)
	}
	if completedSchedule.Status.State != ScheduleStateCompleted {
		t.Errorf("Complete failed. Expected state %s, got %s", ScheduleStateCompleted, completedSchedule.Status.State)
	}
}