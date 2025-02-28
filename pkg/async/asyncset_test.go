package async

import (
	"sync"
	"testing"
)

func TestConcurrentStringSetBasic(t *testing.T) {
	// Create a new set
	set := NewConcurrentStringSet()
	
	// Initially the set should be empty
	if set.Contains("test") {
		t.Errorf("New set should not contain any values")
	}
	
	// Add a value and check it exists
	set.Add("hello")
	if !set.Contains("hello") {
		t.Errorf("Set should contain 'hello' after adding it")
	}
	
	// Adding the same value again should be fine
	set.Add("hello")
	if !set.Contains("hello") {
		t.Errorf("Set should still contain 'hello' after adding it again")
	}
	
	// Add another value
	set.Add("world")
	if !set.Contains("world") {
		t.Errorf("Set should contain 'world' after adding it")
	}
	
	// Remove a value
	set.Remove("hello")
	if set.Contains("hello") {
		t.Errorf("Set should not contain 'hello' after removing it")
	}
	if !set.Contains("world") {
		t.Errorf("Set should still contain 'world'")
	}
	
	// Remove a non-existent value (should be a no-op)
	set.Remove("nonexistent")
	if set.Contains("nonexistent") {
		t.Errorf("Set should not contain 'nonexistent'")
	}
}

func TestConcurrentStringSetThreadSafety(t *testing.T) {
	set := NewConcurrentStringSet()
	const routines = 10
	const iterations = 100
	
	var wg sync.WaitGroup
	wg.Add(routines * 2) // Adding and checking routines
	
	// Start goroutines that add values
	for i := 0; i < routines; i++ {
		go func(id int) {
			defer wg.Done()
			
			// Add a unique value for this goroutine multiple times
			key := string(rune('A' + id))
			for j := 0; j < iterations; j++ {
				set.Add(key)
			}
		}(i)
	}
	
	// Start goroutines that check for values
	for i := 0; i < routines; i++ {
		go func(id int) {
			defer wg.Done()
			
			key := string(rune('A' + id))
			for j := 0; j < iterations; j++ {
				// Just check repeatedly - we don't know if the value
				// has been added yet, so we can't assert anything here
				set.Contains(key)
			}
		}(i)
	}
	
	// Wait for all operations to complete
	wg.Wait()
	
	// Verify all values were added
	for i := 0; i < routines; i++ {
		key := string(rune('A' + i))
		if !set.Contains(key) {
			t.Errorf("Set should contain '%s' after concurrent operations", key)
		}
	}
}

func TestConcurrentStringSetRemoval(t *testing.T) {
	set := NewConcurrentStringSet()
	
	// Add some values
	testValues := []string{"one", "two", "three", "four", "five"}
	for _, val := range testValues {
		set.Add(val)
	}
	
	// Check they were all added
	for _, val := range testValues {
		if !set.Contains(val) {
			t.Errorf("Set should contain '%s'", val)
		}
	}
	
	// Remove them concurrently
	var wg sync.WaitGroup
	wg.Add(len(testValues))
	
	for _, val := range testValues {
		go func(v string) {
			defer wg.Done()
			set.Remove(v)
		}(val)
	}
	
	wg.Wait()
	
	// Verify all were removed
	for _, val := range testValues {
		if set.Contains(val) {
			t.Errorf("Set should not contain '%s' after removal", val)
		}
	}
}