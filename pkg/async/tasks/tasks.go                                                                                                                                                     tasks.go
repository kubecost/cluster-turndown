package tasks

import (
	"fmt"
	"sync"

	"github.com/kubecost/cluster-turndown/pkg/async"

	"k8s.io/klog"
)

//--------------------------------------------------------------------------
//  Errors
//--------------------------------------------------------------------------

// taskError is an error implementation that contains a specific task
type taskError struct {
	message string
	task    Task
}

// Error returns the error message, meets the error interface contract.
func (te *taskError) Error() string {
	return te.message
}

// Creates a new task error from another error
func taskErrorFrom(task Task, err error) error {
	// Determine if the error already has a task assigned. If so,
	// propagate that specific task, not the parent
	t := TaskForError(err)

	// If the error is not a task error, set it to the parameter
	if t == nil {
		t = task
	}

	return &taskError{
		message: err.Error(),
		task:    t,
	}
}

// Creates a new task error with a formatted message
func taskErrorf(task Task, format string, a ...interface{}) error {
	return &taskError{
		message: fmt.Sprintf(format, a...),
		task:    task,
	}
}

// TaskFor returns a specific Task context for an error iff the error was
// a task error. Otherwise, nil is returned.
func TaskForError(e error) Task {
	if te, ok := e.(*taskError); ok {
		return te.task
	}

	return nil
}

//--------------------------------------------------------------------------
//  Task
//--------------------------------------------------------------------------

// Task is an implementation prototype that represents an executable task with
// status message
type Task interface {
	// Executes a task and returns an error of one occurs
	Execute() error

	// Description of the task.
	Description() string
}

//--------------------------------------------------------------------------
//  funcTask
//--------------------------------------------------------------------------

// funcTask is a wrapper for a func() error which implements the Task interface
type funcTask struct {
	fn          func() error
	description string
}

// Executes the function and returns the error
func (t *funcTask) Execute() error {
	return t.fn()
}

// Description returns a description of the task.
func (t *funcTask) Description() string {
	return t.description
}

// TaskFromFunc returns a Task implementation for a func() error.
func TaskFromFunc(fn func() error, description string) Task {
	return &funcTask{
		fn:          fn,
		description: description,
	}
}

//--------------------------------------------------------------------------
//  TaskQueue
//--------------------------------------------------------------------------

// TaskQueue is a FIFO implementation for tasks.
type TaskQueue []Task

// NewTaskQueue creates a new task queue and enqueues all tasks from the provided
// slice.
func NewTaskQueue(tasks []Task) *TaskQueue {
	// Instead of casting here, we want to ensure we strip out any nil entries by
	// calling Enqueue. This is important to maintain the Dequeue implementation.
	q := new(TaskQueue)
	for _, task := range tasks {
		q.Enqueue(task)
	}
	return q
}

// Enqueue adds a Task to the queue.
func (q *TaskQueue) Enqueue(task Task) bool {
	if task == nil {
		return false
	}

	*q = append(*q, task)
	return true
}

// Deqeue removes the first item from the queue. If there are no items in
// the queue, nil is returned.
func (q *TaskQueue) Dequeue() Task {
	qq := *q

	// Since we disallow queueing nil, use it for a failed dequeue
	if len(qq) == 0 {
		return nil
	}

	// Store value for return
	value := qq[0]

	// Nil out backing array value, slice from first index
	qq[0] = nil
	*q = qq[1:]

	return value
}

// Peek returns the first Task in the queue or nil if the queue is empty
func (q *TaskQueue) Peek() Task {
	qq := *q

	if len(qq) == 0 {
		return nil
	}

	return qq[0]
}

// Len returns the length of the queue.
func (q *TaskQueue) Len() int {
	return len(*q)
}

// IsEmpty returns true if the queue is empty.
func (q *TaskQueue) IsEmpty() bool {
	return q.Len() == 0
}

// DrainTo drains the queue to a buffered channel of tasks. Note that
// this method could block if the buffered channel length is hit
func (q *TaskQueue) DrainTo(buffer chan<- Task) {
	for q.Len() != 0 {
		buffer <- q.Dequeue()
	}
}

// Enqueue tasks from an input channel buffer of tasks. Note that this
// method could block if the buffered channel isn't closed.
func (q *TaskQueue) ReceiveFrom(buffer <-chan Task) {
	for task := range buffer {
		q.Enqueue(task)
	}
}

//--------------------------------------------------------------------------
//  SerialExecutor
//--------------------------------------------------------------------------

// SerialExecutor is a task executor which executes each child task serially. It also
// implements the Task interface so it can be used to represent a single task as well.
type SerialExecutor struct {
	tasks       *TaskQueue
	description string
	running     *async.AtomicBool
	currentLock *sync.RWMutex
	current     Task
}

// NewSerialExecutor creates a new SerialExecutor, which can execute the slice of tasks
// serially.
func NewSerialExecutor(tasks []Task, description string) *SerialExecutor {
	return &SerialExecutor{
		tasks:       NewTaskQueue(tasks),
		description: description,
		running:     async.NewAtomicBool(false),
		currentLock: new(sync.RWMutex),
		current:     nil,
	}
}

// Executes each child task serially and reports any errors
func (se *SerialExecutor) IsRunning() bool {
	return se.running.Get()
}

// Executes each child task serially and reports any errors
func (se *SerialExecutor) Execute() error {
	if !se.running.CompareAndSet(false, true) {
		return taskErrorf(se, "The tasks are already executing")
	}

	// Task error to set if one occurs
	var taskError error

	completed := make(chan struct{}, 1)
	tasks := make(chan Task, 10)

	// Execute tasks serially in go routine
	go func() {
		defer close(completed)

		for task := range tasks {
			// Update the current task
			se.updateCurrent(task)

			// Execute task. If an error occurs, we'll divert
			// the remaining tasks to a new TaskQueue, set
			// the error flag, and exit the processing go routine.
			err := task.Execute()
			if err != nil {
				taskError = taskErrorFrom(task, err)

				tq := new(TaskQueue)
				tq.Enqueue(task)
				tq.ReceiveFrom(tasks)
				se.tasks = tq
				return
			}
		}
	}()

	// Drain queue to channel, close channel
	go func(q *TaskQueue) {
		defer close(tasks)

		q.DrainTo(tasks)
	}(se.tasks)

	// Select the completion state
	select {
	case <-completed:
		klog.V(3).Infof("Serial Execution Complete. Tasks Remaining: %d", se.tasks.Len())
	}

	se.updateCurrent(nil)
	se.running.Set(false)

	return taskError
}

// Description returns a description of the execution steps.
func (se *SerialExecutor) Description() string {
	se.currentLock.RLock()
	defer se.currentLock.RUnlock()

	if se.current == nil {
		return se.description
	}

	return fmt.Sprintf("[%s] %s", se.description, se.current.Description())
}

// updateCurrent sets the currently executing task
func (se *SerialExecutor) updateCurrent(task Task) {
	se.currentLock.Lock()
	defer se.currentLock.Unlock()

	se.current = task
}

// This is a stand-in right now. Plans would be to keep a thread-safe map of
// executors globally so status can be queried.
//
// Imagine something like this:
// Right-Size Endpoint (Thread 1) ->
//   func ExecuteSerially(id string, tasks []Task, description string) Task {
//       t := NewSerialExecutor(tasks, description)
//
//       lock.Lock()
//       executors[id] = t
//       lock.Unlock()
//
//       return t.Execute()
//   }
//
// Right-Size Status Endpoint (Thread 2) ->
//   func StatusOf(id string) string {
//       lock.RLock()
//       defer lock.RUnlock()
//
//       return executors[id].Description()
//   }
//
// ExecuteSerially will run synchronously but the SerialExecutor (also a Task implementation) will
// be stored in a global map (or even just as a singleton if we only allow single executions at a time).
// StatusOf will return the description of the executor, which returns something like:
//    "[<Executor Description>] <Currently Executing Task Description>"
//
// This API felt really flexible for a few main reasons:
//   * The Description automatically updates on the executor based on it's currently executing task,
//     and is thread safe.
//   * The Executor is a Task itself, so you can build out grouped executions
//   * Errors that occur mid-execution push the remaining tasks on a new TaskQueue. Remaining tasks (including the
//     failed task) can easily be retried. Retry logic not written yet.
//   * Errors returned by Task.Execute() have a pointer to the Task that caused the error that can be retrieved
//     using tasks.TaskFromError(err error) Task
//
func ExecuteSerially(tasks []Task, description string) error {
	return NewSerialExecutor(tasks, description).Execute()
}
