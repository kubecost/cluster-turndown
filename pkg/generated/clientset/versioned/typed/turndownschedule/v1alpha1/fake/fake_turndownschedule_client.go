/* Generated Source: Do Not Modify */
// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1alpha1 "github.com/kubecost/cluster-turndown/v2/pkg/generated/clientset/versioned/typed/turndownschedule/v1alpha1"
	rest "k8s.io/client-go/rest"
	testing "k8s.io/client-go/testing"
)

type FakeKubecostV1alpha1 struct {
	*testing.Fake
}

func (c *FakeKubecostV1alpha1) TurndownSchedules() v1alpha1.TurndownScheduleInterface {
	return &FakeTurndownSchedules{c}
}

// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *FakeKubecostV1alpha1) RESTClient() rest.Interface {
	var ret *rest.RESTClient
	return ret
}
