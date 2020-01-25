package strategy

import v1 "k8s.io/api/core/v1"

// Turndown Strategy to use for environment preparation
type TurndownStrategy interface {
	TaintKey() string
	CreateOrGetHostNode() (*v1.Node, error)
	AllowKubeDNS() error
}
