package helper

import (
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

//--------------------------------------------------------------------------
//  AggregateError
//--------------------------------------------------------------------------

// Aggregate Error interface which defines a contract for an error
// containing multiple errors
type AggregateError interface {
	GetErrors() []error
}

// Holds multiple errors
type aggError struct {
	errors []error
}

// Implement error interface
func (ae *aggError) Error() string {
	errMessages := []string{}
	for i, err := range ae.errors {
		errMessages = append(errMessages, fmt.Sprintf("%d) %s\n", i+1, err.Error()))
	}
	return strings.Join(errMessages, "\n")
}

// Return a copy of error slice
func (ae *aggError) GetErrors() []error {
	errCopy := make([]error, len(ae.errors))
	copy(errCopy, ae.errors)
	return errCopy
}

// Whether or not the error is an aggregate error
func IsAggregateError(err error) bool {
	_, ok := err.(AggregateError)
	return ok
}

// Retrieve the errors from an aggregate error and return
func GetAggregateErrors(err error) []error {
	if ae, ok := err.(AggregateError); ok {
		return ae.GetErrors()
	}

	return []error{err}
}

//--------------------------------------------------------------------------
//  Kubernetes Waits
//--------------------------------------------------------------------------

// WaitUntilNodeCreated is a cluster helper method that runs a poll against the kubernetes api to
// determine if a node has been created or not.
func WaitUntilNodeCreated(client kubernetes.Interface, nodeLabelKey, nodeLabelValue, nodePoolName string, interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		nodeList, err := client.CoreV1().Nodes().List(metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", nodeLabelKey, nodeLabelValue),
		})
		for _, node := range nodeList.Items {
			if strings.Contains(node.Name, nodePoolName) {
				return true, nil
			}
		}
		return false, err
	})
}

// WaitUntilNodesCreated is a cluster helper method that runs a poll against the kubernetes api to
// determine if a set of node has been created or not.
func WaitUntilNodesCreated(client kubernetes.Interface, nodeLabelKey, nodeLabelValue string, nodeCount int, interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		nodeList, err := client.CoreV1().Nodes().List(metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", nodeLabelKey, nodeLabelValue),
		})
		if err != nil {
			return false, err
		}

		if len(nodeList.Items) >= nodeCount {
			return true, nil

		}
		return false, err
	})
}
