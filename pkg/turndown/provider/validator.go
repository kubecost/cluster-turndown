package provider

import (
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// The purpose of validation is currently to check whether or not the supplied authentication
// methods allow retrieval of the node pools.
// TODO: Look into supporting permissions checks for feature subsets.
func validateProvider(provider TurndownProvider, maxRetries int, done chan<- error) {
	interval := time.Second * 10

	for retries := 0; retries < maxRetries; retries++ {
		nodes, err := provider.GetNodePools()
		if err == nil {
			// Check the number of node groups loaded
			if len(nodes) > 0 {
				done <- nil
				return
			}

			log.Error().Msgf("Cannot locate any node groups from provider.")
		} else {
			log.Error().Msgf("Failed to load node groups: %s", err.Error())
		}

		if retries != (maxRetries - 1) {
			log.Info().Msgf("Retrying (%d remaining) in %d seconds...", maxRetries-retries-1, int64(interval.Seconds()))
			time.Sleep(interval)
		}
	}

	done <- fmt.Errorf("Failed to validate provider")
}

// Validate will return an error if the validation on a ComputeProvider fails
func Validate(provider TurndownProvider, maxRetries int) error {
	log.Info().Msg("Validating Provider...")

	done := make(chan error, 1)
	go validateProvider(provider, maxRetries, done)
	return <-done
}
