package turndown

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kubecost/kubecost-turndown/logging"

	"k8s.io/klog"
)

const (
	TurndownJobType   = "type"
	TurndownJobRepeat = "repeat"

	TurndownJobTypeScaleDown = "scaledown"
	TurndownJobTypeScaleUp   = "scaleup"
	TurndownJobTypeReset     = "reset"

	TurndownJobRepeatNone   = "none"
	TurndownJobRepeatDaily  = "daily"
	TurndownJobRepeatWeekly = "weekly"
)

var (
	repeatDurations = map[string]time.Duration{
		TurndownJobRepeatNone:   0,
		TurndownJobRepeatDaily:  24 * time.Hour,
		TurndownJobRepeatWeekly: 7 * 24 * time.Hour,
	}

	CancelledErr           = errors.New("Cancelled")
	EnvironmentPrepareErr  = errors.New("EnvironmentPrepare")
	NoSchedulesToCancelErr = errors.New("No Schedules to Cancel")
	CancelWhileRunningErr  = errors.New("Cannot Cancel Turndown while Running")
)

type TurndownScheduler struct {
	scheduler JobScheduler
	schedule  *Schedule
	lock      *sync.Mutex
	manager   TurndownManager
	store     ScheduleStore
	log       logging.NamedLogger

	// FIXME: Hack while supporting only a single scheduled pair
	lastTypeCompleted string
}

func NewTurndownScheduler(manager TurndownManager, store ScheduleStore) *TurndownScheduler {
	ts := &TurndownScheduler{
		scheduler: NewSimpleScheduler(),
		lock:      new(sync.Mutex),
		manager:   manager,
		store:     store,
		log:       logging.NamedLogger("TurndownScheduler"),
	}

	ts.scheduler.SetJobCompleteHandler(ts.onJobCompleted)

	schedule, err := store.GetSchedule()
	if err == nil {
		scheduleErr := ts.ScheduleTurndownBySchedule(schedule)
		if scheduleErr != nil {
			klog.V(1).Infof("Failed to schedule from saved state: %s", scheduleErr.Error())
		}
	}

	return ts
}

// Turndown via a Schedule instance. Assumes prior validation.
func (ts *TurndownScheduler) ScheduleTurndownBySchedule(schedule *Schedule) error {
	ts.lock.Lock()
	defer ts.lock.Unlock()

	if ts.schedule != nil {
		return fmt.Errorf("Currently, only a single turndown schedule is allowed.")
	}

	now := time.Now()
	downTime := schedule.ScaleDownTime
	downMeta := schedule.ScaleDownMetadata
	downRepeat := downMeta[TurndownJobRepeat]
	if downRepeat == "" {
		downRepeat = TurndownJobRepeatNone
	}

	upTime := schedule.ScaleUpTime
	upMeta := schedule.ScaleUpMetadata
	//upRepeat := upMeta[TurndownJobRepeat]

	current := schedule.Current
	if current == TurndownJobTypeScaleDown {
		// If we've missed the scale down time, offset by the missed time and apply upTime
		// both downTime and upTime times
		if downTime.Before(now) {
			delta := now.Sub(downTime) + (1 * time.Minute)
			downTime = downTime.Add(delta)
			upTime = upTime.Add(delta)
		}
	} else {
		// If we've missed the scale up time, offset by the missed time and apply upTime
		// both downTime and upTime times
		if upTime.Before(now) {
			delta := now.Sub(upTime) + (1 * time.Minute)
			downTime = downTime.Add(delta)
			upTime = upTime.Add(delta)
		}
	}

	var scaleDownID string
	var err error

	if current == TurndownJobTypeScaleUp && downRepeat == TurndownJobRepeatNone {
		klog.V(3).Infof("ScaleUp Job with NoRepeat ScaleDown. Omitting Scale Down Schedule.")
	} else {
		scaleDownID, err = ts.scheduler.ScheduleWithID(schedule.ScaleDownID, downTime, ts.scaleDown, downMeta)
		if err != nil {
			ts.store.Clear()
			return err
		}
	}

	_, err = ts.scheduler.ScheduleWithID(schedule.ScaleUpID, upTime, ts.scaleUp, upMeta)
	if err != nil {
		if scaleDownID != "" {
			ts.scheduler.Cancel(scaleDownID)
		}
		ts.store.Clear()
		return err
	}

	ts.schedule = schedule

	return nil
}

// Converts an invalid repeatType into a valid one
func fixupRepeatType(repeatType *string) string {
	// Convert nil to valid empty string ptr
	if repeatType == nil {
		*repeatType = ""
	}

	// Empty String -> None
	r := *repeatType
	if r == "" {
		r = TurndownJobRepeatNone
	}
	r = strings.ToLower(r)

	// Set pointer to valid repeat type, return the copy
	*repeatType = r
	return r
}

// Determine whether or not a request scheduled is valid.
func validateSchedule(from time.Time, to time.Time, repeatType *string) error {
	// Check From -> To Range
	delta := to.Sub(from)
	if delta < 0 {
		return fmt.Errorf("The end time (%s) was set to a time before the start parameter (%s).", to, from)
	}

	// Check To relative to Now
	now := time.Now()
	if now.After(from) {
		return fmt.Errorf("The start time (%s) was set to a time in the past (now=%s).", from, now)
	}

	// Check Repetition Type
	repeatDuration, ok := repeatDurations[fixupRepeatType(repeatType)]
	if !ok {
		return fmt.Errorf("The Repeat Type: %s is not a valid repeat type.", *repeatType)
	}

	// Check Total Range vs Repeat Duration
	if repeatDuration > 0 && delta > repeatDuration {
		return fmt.Errorf("The total time between from and to is larger than the repeat duration. Overlap schedule conflict.")
	}

	return nil
}

// Schedules Turndown for the current kubernetes cluster
func (ts *TurndownScheduler) ScheduleTurndown(from time.Time, to time.Time, repeatType string) error {
	ts.lock.Lock()
	defer ts.lock.Unlock()

	// Already a turndown schedule
	if ts.schedule != nil {
		ts.log.Err("Failed to scheduled turndown. Schedule already exists.")
		return fmt.Errorf("Currently, only a single turndown schedule is allowed.")
	}

	err := validateSchedule(from, to, &repeatType)
	if err != nil {
		ts.log.Err("Failed to validate schedule: %s", err.Error())
		return err
	}

	// Schedule the turndown
	scaleDownMeta := map[string]string{
		TurndownJobType:   TurndownJobTypeScaleDown,
		TurndownJobRepeat: repeatType,
	}
	scaleDownID, err := ts.scheduler.Schedule(from, ts.scaleDown, scaleDownMeta)
	if err != nil {
		return err
	}

	// Schedule turnup
	scaleUpMeta := map[string]string{
		TurndownJobType:   TurndownJobTypeScaleUp,
		TurndownJobRepeat: repeatType,
	}
	scaleUpID, err := ts.scheduler.Schedule(to, ts.scaleUp, scaleUpMeta)

	// Persist the current schedule state in store
	ts.schedule = &Schedule{
		Current:           TurndownJobTypeScaleDown,
		ScaleDownID:       scaleDownID,
		ScaleDownTime:     from,
		ScaleDownMetadata: scaleDownMeta,
		ScaleUpID:         scaleUpID,
		ScaleUpTime:       to,
		ScaleUpMetadata:   scaleUpMeta,
	}

	ts.store.SetSchedule(ts.schedule)

	ts.log.Log("Schedule Created: %+v", ts.schedule)

	return nil
}

// Cancels the turndown from occurring. The force bool should be used only if the job is
// cancelled by a running child job.
func (ts *TurndownScheduler) Cancel(force bool) error {
	ts.lock.Lock()
	defer ts.lock.Unlock()

	if ts.schedule == nil {
		ts.log.Err("No Schedules to Cancel")

		return NoSchedulesToCancelErr
	}

	downID := ts.schedule.ScaleDownID
	upID := ts.schedule.ScaleUpID

	// Unless force is flagged, do not allow jobs to be cancelled if one is currently running
	if !force && (ts.scheduler.IsRunning(downID) || ts.scheduler.IsRunning(upID)) {
		ts.log.Err("Attempted to cancel turndown while a job is running.")
		return CancelWhileRunningErr
	}

	ts.scheduler.Cancel(downID)
	ts.scheduler.Cancel(upID)

	ts.schedule = nil
	ts.store.Clear()

	ts.log.Log("Turndown Schedule Successfully Cancelled")

	// If we cancel the turndown after it's already scaled down, scale back up
	if ts.lastTypeCompleted == TurndownJobTypeScaleDown {
		ts.log.Log("Last Turndown Job that ran was ScaleDown. Cancellation will now run ScaleUp...")

		err := ts.manager.ScaleUpCluster()
		if err != nil {
			ts.log.Err("Failed to ScaleUp after Cancel: %s", err.Error())
		}

		// Schedule Reset after ScaleUp
		_, err = ts.scheduler.Schedule(time.Now().Add(5*time.Minute), ts.reset, map[string]string{
			TurndownJobType:   TurndownJobTypeReset,
			TurndownJobRepeat: TurndownJobRepeatNone,
		})
	}

	return nil
}

func (ts *TurndownScheduler) GetSchedule() *Schedule {
	ts.lock.Lock()
	defer ts.lock.Unlock()

	if ts.schedule == nil {
		return nil
	}

	// Return a copy of the schedule
	clone := *ts.schedule
	return &clone
}

// Job Complete handler to reschedule a new job
func (ts *TurndownScheduler) onJobCompleted(id string, scheduled time.Time, metadata map[string]string, err error) {
	// Check to make sure this is a scheduler job made for turndown
	jobType, ok := metadata[TurndownJobType]
	if !ok {
		ts.log.Warn("Not a turndown job. Ignoring.")
		return
	}

	// Reset Job Type -- Nothing Further to Reschedule
	if jobType == TurndownJobTypeReset {
		return
	}

	// Handle Errors
	if err != nil {
		// Schedule is written, this is simply waiting on the pod to move nodes, so we just ignore any rescheduling
		if err.Error() == "EnvironmentPrepare" || err.Error() == "Cancelled" {
			return
		}

		ts.log.Err("Failed to run scaling job: %s - Error: %s", jobType, err.Error())
	}

	// Reset the Last Completed JobType
	ts.lastTypeCompleted = jobType

	// Scale-Up requires a follow-up job to reset the cluster environment
	// This is sort of a hack for now, as we want to ensure scale up completion before
	// scheduling this reset
	if jobType == TurndownJobTypeScaleUp {
		_, err := ts.scheduler.Schedule(time.Now().Add(5*time.Minute), ts.reset, map[string]string{
			TurndownJobType:   TurndownJobTypeReset,
			TurndownJobRepeat: TurndownJobRepeatNone,
		})

		if err != nil {
			ts.log.Err("Failed to create reset job: %s", err.Error())
		}
	}

	repeat, ok := metadata[TurndownJobRepeat]
	if !ok || repeat == TurndownJobRepeatNone {
		ts.log.Log("Did not find a repeat task. Not rescheduling")

		// For non-repeat tasks, make sure we update the current task unless it is a scale-up
		ts.lock.Lock()
		defer ts.lock.Unlock()

		if jobType == TurndownJobTypeScaleUp {
			ts.schedule = nil
			ts.store.Clear()
		} else if jobType == TurndownJobTypeScaleDown {
			ts.schedule.Current = TurndownJobTypeScaleUp
			ts.store.SetSchedule(ts.schedule)
		}

		return
	}

	repeatDuration := repeatDurations[repeat]
	newScheduled := scheduled.Add(repeatDuration)

	var jobFunc JobFunc
	if jobType == TurndownJobTypeScaleDown {
		jobFunc = ts.scaleDown
	} else if jobType == TurndownJobTypeScaleUp {
		jobFunc = ts.scaleUp
	}

	newJobID, err := ts.scheduler.Schedule(newScheduled, jobFunc, metadata)
	if err != nil {
		ts.log.Err("Failed to reschedule job: %s", err.Error())
	}

	ts.lock.Lock()
	defer ts.lock.Unlock()

	// Flip the Current Job (Next Job Type to Run), and update ids and times
	if jobType == TurndownJobTypeScaleDown {
		ts.schedule.Current = TurndownJobTypeScaleUp
		ts.schedule.ScaleDownID = newJobID
		ts.schedule.ScaleDownTime = newScheduled
		ts.schedule.ScaleDownMetadata = metadata
	} else if jobType == TurndownJobTypeScaleUp {
		ts.schedule.Current = TurndownJobTypeScaleDown
		ts.schedule.ScaleUpID = newJobID
		ts.schedule.ScaleUpTime = newScheduled
		ts.schedule.ScaleUpMetadata = metadata
	}

	ts.store.SetSchedule(ts.schedule)
}

func (ts *TurndownScheduler) scaleDown() error {
	klog.V(3).Info("-- Scale Down --")

	// Determine if we are running on a single small node
	isOnNode, err := ts.manager.IsRunningOnTurndownNode()
	if nil != err {
		ts.log.Err("Error attempting to check status of current node")
	}

	// If we're not running on the single turndown node, create a new turndown node,
	// and update the deployment for this pod to schedule only on the turndown node.
	// This will cause the pod to move from the current node to the new node once it
	// has been successfully provisioned. The current state of the scheduling must be
	// pulled from the persistent store in order to continue.
	if !isOnNode {
		ts.log.Log("Turndown Pod does not exist on expected host node. Preparing environment...")

		err := ts.manager.PrepareTurndownEnvironment()
		if err != nil {
			ts.log.Err("Failed to prepare current turndown environment. Cancelling. Err=%s", err.Error())

			ts.Cancel(true)
			return CancelledErr
		}

		ts.log.Log("Environment Preparation Completed. Pod will reschedule on target host node now.")

		// Since we'll be moving nodes and rescheduling, we'll return a "special" error here
		return EnvironmentPrepareErr
	} else {
		ts.log.Log("Already running on correct turndown host node. No need to setup environment.")
	}

	return ts.manager.ScaleDownCluster()
}

func (ts *TurndownScheduler) scaleUp() error {
	klog.V(3).Info("-- Scale Up --")
	err := ts.manager.ScaleUpCluster()

	return err
}

func (ts *TurndownScheduler) reset() error {
	klog.V(3).Info("-- Reset --")
	err := ts.manager.ResetTurndownEnvironment()

	return err
}
