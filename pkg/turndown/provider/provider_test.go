package provider

import (
	"testing"
)

// TestToTurndownNodePoolLabels tests that the turndown labels are correctly added
func TestToTurndownNodePoolLabels(t *testing.T) {
	testCases := []struct {
		name          string
		inputLabels   map[string]string
		expectedCount int
	}{
		{
			name:          "Empty input labels",
			inputLabels:   map[string]string{},
			expectedCount: 1, // Just the turndown label
		},
		{
			name: "With existing labels",
			inputLabels: map[string]string{
				"foo": "bar",
				"baz": "qux",
			},
			expectedCount: 3, // 2 existing + 1 turndown label
		},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := toTurndownNodePoolLabels(tc.inputLabels)
			
			// Check count
			if len(result) != tc.expectedCount {
				t.Errorf("Expected %d labels, got %d", tc.expectedCount, len(result))
			}
			
			// Check turndown label exists and is set to "true"
			value, exists := result[TurndownNodeLabel]
			if !exists {
				t.Errorf("Turndown label not found in result")
			}
			if value != "true" {
				t.Errorf("Expected turndown label value 'true', got '%s'", value)
			}
		})
	}
}