package turndown

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	"github.com/kubecost/cluster-turndown/pkg/apis/turndownschedule/v1alpha1"
	clientset "github.com/kubecost/cluster-turndown/pkg/generated/clientset/versioned"
	schedulescheme "github.com/kubecost/cluster-turndown/pkg/generated/clientset/versioned/scheme"
	informers "github.com/kubecost/cluster-turndown/pkg/generated/informers/externalversions/turndownschedule/v1alpha1"
	listers "github.com/kubecost/cluster-turndown/pkg/generated/listers/turndownschedule/v1alpha1"
)

const controllerAgentName = "turndown-schedule-controller"

const (
	// ScheduleTurndownSuccess is used as part of the Event 'reason' when a TurndownSchedule
	// resource is successfully scheduled
	ScheduleTurndownSuccess        = "ScheduleTurndownSuccess"
	ScheduleTurndownSuccessMessage = "Successfully scheduled turndown"

	CancelTurndownSuccess        = "CancelTurndownSuccess"
	CancelTurndownSuccessMessage = "Successfully cancelled turndown"

	// ErrAlreadyScheduled is used as part of the Event 'reason' when a TurndownSchedule fails
	// due to an existing turndown schedule.
	ErrAlreadyScheduled = "ErrAlreadyScheduled"

	TurndownScheduleFinalizer = "finalizer.kubecost.k8s.io"

	ScheduleStateSuccess   = "ScheduleSuccess"
	ScheduleStateFailed    = "ScheduleFailed"
	ScheduleStateCompleted = "ScheduleCompleted"
)

// TurndownScheduleResourceController is the controller implementation for TurndownSchedule,
// the custom resource definition for turndown.
type TurndownScheduleResourceController struct {
	kubeclientset kubernetes.Interface
	clientset     clientset.Interface

	scheduler         *TurndownScheduler
	schedulesInformer informers.TurndownScheduleInformer
	scheduleLister    listers.TurndownScheduleLister
	scheduleSynced    cache.InformerSynced

	workqueue workqueue.RateLimitingInterface
	recorder  record.EventRecorder
}

// NewTurndownScheduleResourceController creates a new controller for TurndownSchedule,
// the custom resource definition for turndown.
func NewTurndownScheduleResourceController(
	kubeclientset kubernetes.Interface,
	clientset clientset.Interface,
	scheduler *TurndownScheduler,
	schedulesInformer informers.TurndownScheduleInformer) *TurndownScheduleResourceController {

	utilruntime.Must(schedulescheme.AddToScheme(scheme.Scheme))

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &TurndownScheduleResourceController{
		kubeclientset:     kubeclientset,
		clientset:         clientset,
		scheduler:         scheduler,
		schedulesInformer: schedulesInformer,
		scheduleLister:    schedulesInformer.Lister(),
		scheduleSynced:    schedulesInformer.Informer().HasSynced,
		workqueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "TurndownSchedules"),
		recorder:          recorder,
	}

	klog.V(4).Info("Setting up event handlers")

	// Set up an event handler for when TurndownSchedule resources change
	schedulesInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    controller.addSchedule,
		UpdateFunc: controller.updateSchedule,
		DeleteFunc: controller.removeSchedule,
	})

	return controller
}

// Runs the resource controller workqueue processing
func (c *TurndownScheduleResourceController) Run(threadiness int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.V(3).Info("Starting TurndownSchedule controller")

	// Wait for the caches to be synced before starting workers
	if ok := cache.WaitForCacheSync(stopCh, c.scheduleSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	// Executes a prune go routine that clears out completed/failed schedule resources
	runPruneCompletedSchedules(time.Minute, time.Minute*30, c.clientset, stopCh)

	<-stopCh

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *TurndownScheduleResourceController) runWorker() {
	for c.processNextWorkItem() {
	}
}

// Processes the next workqueue item
func (c *TurndownScheduleResourceController) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		defer c.workqueue.Done(obj)
		var key string
		var ok bool

		if key, ok = obj.(string); !ok {
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}

		if err := c.handle(key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)

		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

// Handles incoming key from the workqueue
func (c *TurndownScheduleResourceController) handle(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Get the TurndownSchedule resource with this namespace/name
	turndownSchedule, err := c.scheduleLister.Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("TurndownSchedule '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	// Check schedule for flagged deletion
	if turndownSchedule.GetObjectMeta().GetDeletionTimestamp() != nil {
		// TurndownSchedule was successfully scheduled. Need Cancel.
		if turndownSchedule.Status.State == ScheduleStateSuccess {
			err = c.tryCancel(turndownSchedule)
			if err != nil {
				return err
			}

			c.recorder.Event(turndownSchedule, corev1.EventTypeNormal, CancelTurndownSuccess, CancelTurndownSuccessMessage)
		} else {
			// Otherwise, just finalize schedule
			clone := turndownSchedule.DeepCopy()
			err := c.clearFinalizer(clone)
			if err != nil {
				return err
			}
		}

		return nil
	}

	// Check to see if there is an existing status/state before scheduling
	if turndownSchedule.Status.State != "" {
		return nil
	}

	err = c.trySchedule(turndownSchedule)
	if err != nil {
		return err
	}

	c.recorder.Event(turndownSchedule, corev1.EventTypeNormal, ScheduleTurndownSuccess, ScheduleTurndownSuccessMessage)
	return nil
}

// Tries to schedule the turndown if a new TurndownSchedule resource was created.
func (c *TurndownScheduleResourceController) trySchedule(schedule *v1alpha1.TurndownSchedule) error {
	scheduleCopy := schedule.DeepCopy()

	s := scheduleCopy.Spec
	tds, err := c.scheduler.ScheduleTurndown(s.Start.Time, s.End.Time, s.Repeat)

	// Update the Schedule Status on Creation Here -- Other status changes are made by ScheduleStore
	scheduleCopy.Status.LastUpdated = v1.NewTime(time.Now().UTC())
	if err != nil {
		scheduleCopy.Status.State = ScheduleStateFailed
	} else {
		scheduleCopy.Status.State = ScheduleStateSuccess
		WriteScheduleStatus(&scheduleCopy.Status, tds)
	}

	_, err = c.clientset.KubecostV1alpha1().TurndownSchedules().UpdateStatus(context.TODO(), scheduleCopy, v1.UpdateOptions{})
	return err
}

// Tries to cancel a turndown schedule based on the soft deletion of a TurndownSchedule resource
// This method will also finalize the resource if cancelling succeeds.
func (c *TurndownScheduleResourceController) tryCancel(schedule *v1alpha1.TurndownSchedule) error {
	scheduleCopy := schedule.DeepCopy()

	current := c.scheduler.GetSchedule()
	status := &scheduleCopy.Status
	if current != nil {
		if strings.EqualFold(current.ScaleDownID, status.ScaleDownID) && strings.EqualFold(current.ScaleUpID, status.ScaleUpID) {
			err := c.scheduler.Cancel(false)
			if err != nil {
				klog.Infof("Failed to cancel: %s", err.Error())
				return err
			}
		}
	}

	return c.clearFinalizer(scheduleCopy)
}

// Clear Finalizers handles updating the resource to clear the specific turndown finalizer for our
// TurndownSchedule resource. This allows us to effectively finalize deletion for TurndownSchedules
func (c *TurndownScheduleResourceController) clearFinalizer(schedule *v1alpha1.TurndownSchedule) error {
	finalizers := schedule.ObjectMeta.GetFinalizers()
	newFinalizers := finalizers[:0]
	for _, f := range finalizers {
		if !strings.EqualFold(TurndownScheduleFinalizer, f) {
			newFinalizers = append(newFinalizers, f)
		}
	}

	schedule.ObjectMeta.SetFinalizers(newFinalizers)

	_, err := c.clientset.KubecostV1alpha1().TurndownSchedules().Update(context.TODO(), schedule, v1.UpdateOptions{})
	return err
}

// Add an AddSchedule to workqueue
func (c *TurndownScheduleResourceController) addSchedule(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.Add(key)
}

// Add an UpdateSchedule to workqueue
func (c *TurndownScheduleResourceController) updateSchedule(old, new interface{}) {
	c.addSchedule(new)
}

// Add a RemoveSchedule to workqueue
func (c *TurndownScheduleResourceController) removeSchedule(obj interface{}) {
	var key string
	var err error
	if key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}

	c.workqueue.Add(key)
}

// Runs a loop that will prune any schedules that failed or completed.
func runPruneCompletedSchedules(interval time.Duration, maxAge time.Duration, client clientset.Interface, stopCh <-chan struct{}) {
	ticker := time.NewTicker(interval)

	// Expired Entry Eviction
	go func(client clientset.Interface, stop <-chan struct{}) {
		for {
			select {
			case <-ticker.C:
				clearCompletedSchedules(client, maxAge)
			case <-stopCh:
				ticker.Stop()
				return
			}
		}
	}(client, stopCh)
}

// Clears failed or completed schedules over a specific age
func clearCompletedSchedules(client clientset.Interface, maxAge time.Duration) {
	schedules, err := client.KubecostV1alpha1().TurndownSchedules().List(context.TODO(), v1.ListOptions{})
	if err != nil {
		return
	}

	// LastUpdated must be after now-maxAge
	mustBeAfter := time.Now().UTC().Add(-maxAge)

	// Find schedules to prune
	for _, schedule := range schedules.Items {
		if schedule.Status.State != ScheduleStateSuccess {
			lastUpdated := schedule.Status.LastUpdated.Time
			if lastUpdated.Before(mustBeAfter) {
				client.KubecostV1alpha1().TurndownSchedules().Delete(context.TODO(), schedule.Name, v1.DeleteOptions{})
			}
		}
	}
}
