package strategy

import v1 "k8s.io/api/core/v1"

// Turndown Strategy to use for environment preparation
type TurndownStrategy interface {
	// TaintKey returns the key used to taint the target host node.
	TaintKey() string

	// CreateOrGetHostNode either create a new node or gets an existing node containing a turndown
	// label specific to our turndown deployment.
	CreateOrGetHostNode() (*v1.Node, error)

	// UpdateDNS will make adjustments to dns (if necessary) to allow the turndown deployment to continue
	// to use dns names for communication.
	UpdateDNS() error

	// IsReversible returns a bool set to true if the target host node updates can be reversed on scale up.
	IsReversible() bool

	// ReverseHostNode will back out of any annotation or labeling applied by the strategy.
	ReverseHostNode() error
}
