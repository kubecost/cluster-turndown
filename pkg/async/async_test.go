package async

import (
	"testing"
	"time"
)

func TestAtomicBool(t *testing.T) {
	// Test initialization with default value
	trueVal := NewAtomicBool(true)
	falseVal := NewAtomicBool(false)
	
	if !trueVal.Get() {
		t.Errorf("NewAtomicBool(true) should return true on Get()")
	}
	
	if falseVal.Get() {
		t.Errorf("NewAtomicBool(false) should return false on Get()")
	}
	
	// Test Set method
	boolVal := NewAtomicBool(false)
	boolVal.Set(true)
	if !boolVal.Get() {
		t.Errorf("After Set(true), Get() should return true")
	}
	
	boolVal.Set(false)
	if boolVal.Get() {
		t.Errorf("After Set(false), Get() should return false")
	}
	
	// Test CompareAndSet
	atomBool := NewAtomicBool(false)
	
	// When current matches, should change and return true
	if !atomBool.CompareAndSet(false, true) {
		t.Errorf("CompareAndSet with matching current value should return true")
	}
	
	if !atomBool.Get() {
		t.Errorf("After successful CompareAndSet(false, true), value should be true")
	}
	
	// When current doesn't match, should not change and return false
	if atomBool.CompareAndSet(false, false) {
		t.Errorf("CompareAndSet with non-matching current value should return false")
	}
	
	if !atomBool.Get() {
		t.Errorf("After failed CompareAndSet, value should remain unchanged")
	}
}

func TestWaitChannelBasic(t *testing.T) {
	wc := NewWaitChannel()
	
	// Add 1 to the counter
	wc.Add(1)
	
	// Create a channel to track completion
	done := make(chan bool)
	
	// Start a goroutine to wait on the WaitChannel
	go func() {
		ch := wc.Wait()
		<-ch // Wait for completion
		done <- true
	}()
	
	// Give the goroutine time to start
	time.Sleep(10 * time.Millisecond)
	
	// Call Done to decrement the counter
	wc.Done()
	
	// Check if the goroutine completes
	select {
	case <-done:
		// Success - the goroutine completed
	case <-time.After(100 * time.Millisecond):
		t.Errorf("WaitChannel didn't complete within the expected time")
	}
}

func TestWaitChannelMultipleWaiters(t *testing.T) {
	wc := NewWaitChannel()
	wc.Add(1)
	
	// Get the wait channel twice - should be the same channel
	ch1 := wc.Wait()
	ch2 := wc.Wait()
	
	if ch1 != ch2 {
		t.Errorf("Multiple calls to Wait() should return the same channel")
	}
	
	// Mark as done
	wc.Done()
	
	// Both channels should close
	select {
	case <-ch1:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Errorf("First wait channel wasn't closed after Done()")
	}
	
	select {
	case <-ch2:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Second wait channel wasn't closed after Done()")
	}
}

func TestWaitChannelReuse(t *testing.T) {
	wc := NewWaitChannel()
	
	// First usage
	wc.Add(1)
	ch1 := wc.Wait()
	wc.Done()
	
	// Wait for channel to close
	<-ch1
	
	// Give some time for cleanup to finish
	time.Sleep(20 * time.Millisecond)
	
	// Second usage
	wc.Add(1)
	ch2 := wc.Wait()
	
	// Verify it's a different channel
	if ch1 == ch2 {
		t.Errorf("Wait() should return a new channel after previous completion")
	}
	
	wc.Done()
}