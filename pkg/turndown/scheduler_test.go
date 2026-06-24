package turndown

import (
	"testing"
	"time"

	// "github.com/kubecost/cluster-turndown/v2/pkg/turndown/provider"
	// "github.com/kubecost/cluster-turndown/v2/pkg/turndown/strategy"
	// cp "github.com/kubecost/cluster-turndown/v2/pkg/cluster/provider"
	// "github.com/kubecost/cluster-turndown/v2/pkg/generated/clientset/versioned/fake"
)

// TestScheduleTurndownValidation tests the validation logic in ScheduleTurndown
func TestScheduleTurndownValidation(t *testing.T) {
	// Current time for testing
	now := time.Now()

	// Test cases
	testCases := []struct {
		name        string
		from        time.Time
		to          time.Time
		repeatType  string
		shouldError bool
	}{
		{
			name:        "Valid schedule - future dates with sufficient gap",
			from:        now.Add(1 * time.Hour),
			to:          now.Add(2 * time.Hour),
			repeatType:  TurndownJobRepeatNone,
			shouldError: false,
		},
		{
			name:        "Invalid schedule - end before start",
			from:        now.Add(2 * time.Hour),
			to:          now.Add(1 * time.Hour),
			repeatType:  TurndownJobRepeatNone,
			shouldError: true,
		},
		{
			name:        "Invalid schedule - less than 20 min gap",
			from:        now.Add(1 * time.Hour),
			to:          now.Add(1*time.Hour + 15*time.Minute),
			repeatType:  TurndownJobRepeatNone,
			shouldError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call the function under test
			var repeatType = tc.repeatType
			err := validateSchedule(tc.from, tc.to, &repeatType)
			
			// Check the result
			if tc.shouldError && err == nil {
				t.Errorf("Expected an error but got none")
			}
			if !tc.shouldError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}

// Helper function to create a string pointer
func stringPtr(s string) *string {
	return &s
}