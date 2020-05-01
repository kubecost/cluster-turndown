package test

import (
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubecost/cluster-turndown/pkg/async/tasks"
)

/*
func init() {
	klog.InitFlags(nil)
	flag.Set("v", "3")
	flag.Parse()
}
*/

// Sleep a random number of milliseconds between min and max
func randSleep(min, max int) {
	t := min + rand.Intn(max-min)
	if t <= 0 {
		return
	}

	time.Sleep(time.Duration(t) * time.Millisecond)
}

//
func newFunc(i int, counter *uint32) func() error {
	return func() error {
		randSleep(200, 600)
		atomic.AddUint32(counter, 1)
		return nil
	}
}

func newErrorFunc(i int) func() error {
	return func() error {
		randSleep(200, 600)
		return fmt.Errorf("Failed to execute!")
	}
}

func taskNameFor(index int) string {
	return fmt.Sprintf("Task-%d", index)
}

func dummyTasks(total int, counter *uint32, errorAt int) ([]tasks.Task, tasks.Task) {
	var errorTask tasks.Task = nil

	ts := []tasks.Task{}

	for i := 0; i < total; i++ {
		taskName := taskNameFor(i)

		var t tasks.Task
		if i == errorAt {
			t = tasks.TaskFromFunc(newErrorFunc(i), taskName)
			errorTask = t
		} else {
			t = tasks.TaskFromFunc(newFunc(i, counter), taskName)
		}

		ts = append(ts, t)
	}

	return ts, errorTask
}

func taskErrorLogMessage(err error) string {
	message := err.Error()

	t := tasks.TaskForError(err)
	if t != nil {
		return fmt.Sprintf("[Error][Task: %s]: %s", t.Description(), message)
	}

	return fmt.Sprintf("[Error]: %s", message)
}

func TestSerialExecution(t *testing.T) {
	const numTasks = 20
	const errorAtIndex = -1

	// Use to count task executions
	var counter uint32 = 0

	ts, _ := dummyTasks(numTasks, &counter, errorAtIndex)

	var err error
	runningTask := tasks.ExecuteSerially(ts, "Execute ScaleDown")

	select {
	case err = <-runningTask.OnComplete():
	}

	if err != nil {
		t.Errorf(taskErrorLogMessage(err))
		return
	}

	if counter != numTasks {
		t.Errorf("Tasks Executed [%d] not equal to Num Tasks[%d].", counter, numTasks)
		return
	}

	t.Logf("Successfully executed %d tasks", counter)
}

func TestSerialExecutionFailure(t *testing.T) {
	const numTasks = 20
	const errorAtIndex = 7
	const expectedExecutions = errorAtIndex
	// const expectedRemaining = numTasks - errorAtIndex + 1

	// Use to count task executions
	var counter uint32 = 0

	ts, errTask := dummyTasks(numTasks, &counter, errorAtIndex)

	var err error
	runningTask := tasks.ExecuteSerially(ts, "Execute ScaleDown")

	select {
	case err = <-runningTask.OnComplete():
	}

	if err == nil {
		t.Errorf("Expected Errorf After %d Tasks Executed. Actual: %d", errorAtIndex-1, counter)
		return
	}

	taskForError := tasks.TaskForError(err)
	if taskForError != errTask {
		t.Errorf("Task from the Error was not equal to the selected error Task. '%s' != '%s'", taskForError.Description(), errTask.Description())
		return
	}

	if counter != expectedExecutions {
		t.Errorf("Tasks Executed [%d] not equal to Expected Executions[%d].", counter, expectedExecutions)
		return
	}

	t.Logf("Serial Execution Failed as Expected")
}
