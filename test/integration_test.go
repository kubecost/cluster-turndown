// +build integration

package test

import (
	"context"
	"testing"
	"time"
	
	"github.com/kubecost/cluster-turndown/v2/pkg/apis/turndownschedule/v1alpha1"
	"github.com/kubecost/cluster-turndown/v2/pkg/turndown"
	"github.com/kubecost/cluster-turndown/v2/pkg/generated/clientset/versioned/fake"
	
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestTurndownScheduleResource tests creating and deleting a TurndownSchedule resource
func TestTurndownScheduleResource(t *testing.T) {
	// Skip this test unless explicitly running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test")
	}
	
	// Use a fake client for this test
	client := fake.NewSimpleClientset()
	
	// Create a TurndownSchedule
	now := time.Now()
	schedule := &v1alpha1.TurndownSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-schedule",
			Finalizers: []string{
				turndown.TurndownScheduleFinalizer,
			},
		},
		Spec: v1alpha1.TurndownScheduleSpec{
			Start:  metav1.NewTime(now.Add(1 * time.Hour)),
			End:    metav1.NewTime(now.Add(2 * time.Hour)),
			Repeat: turndown.TurndownJobRepeatNone,
		},
	}
	
	// Create the schedule
	createdSchedule, err := client.KubecostV1alpha1().TurndownSchedules().Create(
		context.TODO(),
		schedule,
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to create TurndownSchedule: %v", err)
	}
	
	// Verify the created schedule
	if createdSchedule.Name != schedule.Name {
		t.Errorf("Expected schedule name %s, got %s", schedule.Name, createdSchedule.Name)
	}
	
	// Delete the schedule
	err = client.KubecostV1alpha1().TurndownSchedules().Delete(
		context.TODO(),
		schedule.Name,
		metav1.DeleteOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to delete TurndownSchedule: %v", err)
	}
	
	// Verify the schedule is deleted
	_, err = client.KubecostV1alpha1().TurndownSchedules().Get(
		context.TODO(),
		schedule.Name,
		metav1.GetOptions{},
	)
	if err == nil {
		t.Error("Expected error when getting deleted schedule, got nil")
	}
}