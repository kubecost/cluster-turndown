package provider

import (
	"fmt"
	"time"

	"k8s.io/klog"
)

// Validation of provider by checking the GetNodePools() call
func validateProvider(provider ComputeProvider, maxRetries int, done chan<- error) {
	interval := time.Second * 10

	for retries := 0; retries < maxRetries; retries++ {
		nodes, err := provider.GetNodePools()
		if err == nil {
			// Check the number of node groups loaded
			if len(nodes) > 0 {
				done <- nil
				return
			}

			klog.Infof("[Error]: Cannot locate any node groups from provider.")
		} else {
			klog.Infof("[Error]: Failed to load node groups: %s", err.Error())
		}

		if retries != (maxRetries - 1) {
			klog.Infof("Retrying (%d remaining) in %d seconds...", maxRetries-retries-1, interval.Seconds)
			time.Sleep(interval)
		}
	}

	done <- fmt.Errorf("Failed to validate provider")
}

// Validate will return an error if the validation on a ComputeProvider fails
func Validate(provider ComputeProvider, maxRetries int) error {
	klog.Infof("Validating Provider")

	done := make(chan error, 1)
	go validateProvider(provider, maxRetries, done)
	return <-done
}
