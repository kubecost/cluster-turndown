package turndown

import (
	"context"
	"sync"
	"time"

	"github.com/kubecost/kubecost-turndown/async"

	"github.com/google/uuid"
	"k8s.io/klog"
)

const (
	RFC3339Milli = "2006-01-02T15:04:05.000Z"
)

// JobFunc definition that runs on a specific schedule
type JobFunc func() error

// JobCompleteHandler occurs after the job executes passing back the identifier, metadata, and any errors
type JobCompleteHandler func(id string, scheduled time.Time, metadata map[string]string, err error)

// Creates a new unique job identifier
func newJobID() string {
	return uuid.New().String()
}

// JobScheduler interface contains
type JobScheduler interface {
	Schedule(next time.Time, job JobFunc, metadata map[string]string) (id string, err error)
	ScheduleWithID(id string, next time.Time, jobFunc JobFunc, metadata map[string]string) (string, error)
	Cancel(id string) bool
	NextScheduledTimeFor(id string) (next time.Time, ok bool)
	SetJobCompleteHandler(handler JobCompleteHandler)
	IsRunning(jobID string) bool
}

type SimpleJob struct {
	id       string
	next     time.Time
	job      JobFunc
	cancel   context.CancelFunc
	metadata map[string]string
}

type SimpleJobScheduler struct {
	jobs        map[string]*SimpleJob
	lock        *sync.Mutex
	runningJobs *async.ConcurrentStringSet
	jobComplete JobCompleteHandler
}

func NewSimpleScheduler() JobScheduler {
	return &SimpleJobScheduler{
		jobs:        make(map[string]*SimpleJob),
		lock:        new(sync.Mutex),
		runningJobs: async.NewConcurrentStringSet(),
	}
}

func (sjs *SimpleJobScheduler) Schedule(next time.Time, jobFunc JobFunc, metadata map[string]string) (string, error) {
	ctx, cancelFunc := context.WithCancel(context.TODO())

	job := sjs.addJob(next, jobFunc, cancelFunc, metadata)
	sjs.scheduleJob(ctx, job)

	return job.id, nil
}

func (sjs *SimpleJobScheduler) ScheduleWithID(id string, next time.Time, jobFunc JobFunc, metadata map[string]string) (string, error) {
	ctx, cancelFunc := context.WithCancel(context.TODO())

	job := sjs.addJobWithID(id, next, jobFunc, cancelFunc, metadata)
	sjs.scheduleJob(ctx, job)

	return job.id, nil
}

func (sjs *SimpleJobScheduler) Cancel(id string) bool {
	job := sjs.removeJob(id)
	if job == nil {
		return false
	}

	job.cancel()
	return true
}

// Looks up and returns the next scheduled time a job will execute.
func (sjs *SimpleJobScheduler) NextScheduledTimeFor(id string) (time.Time, bool) {
	var next time.Time

	job, ok := sjs.jobFor(id)
	if !ok {
		return next, false
	}

	return job.next, true
}

// Sets the job complete handler. If one is already
func (sjs *SimpleJobScheduler) SetJobCompleteHandler(handler JobCompleteHandler) {
	sjs.jobComplete = handler
}

func (sjs *SimpleJobScheduler) IsRunning(jobID string) bool {
	return sjs.runningJobs.Contains(jobID)
}

// Looks up a job by identifier.
func (sjs *SimpleJobScheduler) jobFor(id string) (job *SimpleJob, ok bool) {
	sjs.lock.Lock()
	defer sjs.lock.Unlock()

	job, ok = sjs.jobs[id]
	return
}

// Creates a new job and returns it but does not schedule it
func (sjs *SimpleJobScheduler) addJob(next time.Time, jobFunc JobFunc, cancelFunc context.CancelFunc, metadata map[string]string) *SimpleJob {
	return sjs.addJobWithID(newJobID(), next, jobFunc, cancelFunc, metadata)
}

// Creates a new job and returns it but does not schedule it
func (sjs *SimpleJobScheduler) addJobWithID(id string, next time.Time, jobFunc JobFunc, cancelFunc context.CancelFunc, metadata map[string]string) *SimpleJob {
	job := &SimpleJob{
		id:       id,
		next:     next,
		job:      jobFunc,
		cancel:   cancelFunc,
		metadata: metadata,
	}

	sjs.lock.Lock()
	defer sjs.lock.Unlock()

	sjs.jobs[job.id] = job
	return job
}

// Removes a job by id from the map and returns that job instance, but does not cancel
// the job.
func (sjs *SimpleJobScheduler) removeJob(id string) *SimpleJob {
	sjs.lock.Lock()
	defer sjs.lock.Unlock()

	job, ok := sjs.jobs[id]
	if !ok {
		return nil
	}

	delete(sjs.jobs, id)
	return job
}

// Schedules the execution of a job for a specific time
func (sjs *SimpleJobScheduler) scheduleJob(ctx context.Context, job *SimpleJob) {
	remaining := job.next.UTC().Sub(time.Now().UTC())
	go func() {
		var isCancelled bool = false
		var err error = nil

		// Defer the job removal and jobComplete execution to ensure that they do
		// not overlap. We want the jobCompletion call to be able to create a new
		// scheduled job
		defer func() {
			defer sjs.runningJobs.Remove(job.id)

			sjs.removeJob(job.id)
			if isCancelled {
				return
			}

			if sjs.jobComplete != nil {
				sjs.jobComplete(job.id, job.next, job.metadata, err)
			} else if err != nil {
				klog.V(1).Infof("Error: Job failed with error: %s", err.Error())
			}
		}()

		select {
		case <-time.After(remaining):
			sjs.runningJobs.Add(job.id)
			err = job.job()
		case <-ctx.Done():
			isCancelled = true
			klog.V(1).Infof("Job was cancelled: %s", job.id)
		}
	}()
}
