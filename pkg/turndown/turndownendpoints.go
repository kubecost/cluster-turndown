package turndown

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kubecost/cluster-turndown/v2/pkg/apis/turndownschedule/v1alpha1"
	clientset "github.com/kubecost/cluster-turndown/v2/pkg/generated/clientset/versioned"

	"github.com/opencost/opencost/core/pkg/log"
	proto "github.com/opencost/opencost/core/pkg/protocol"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

var protocol = proto.HTTP()

// ScheduleTurndownRequest is the POST encoding used to
type ScheduleTurndownRequest struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Repeat string    `json:"repeat,omitempty"`
}

type TurndownEndpoints struct {
	client    clientset.Interface
	scheduler *TurndownScheduler
	turndown  TurndownManager
}

func NewTurndownEndpoints(
	client clientset.Interface,
	scheduler *TurndownScheduler,
	turndown TurndownManager) *TurndownEndpoints {

	return &TurndownEndpoints{
		client:    client,
		scheduler: scheduler,
		turndown:  turndown,
	}
}

func (te *TurndownEndpoints) HandleStartSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		schedule := te.scheduler.GetSchedule()
		if schedule == nil {
			protocol.WriteData(w, schedule)
			return
		}

		marshaled, err := json.Marshal(schedule)
		if err != nil {
			protocol.WriteError(w, protocol.InternalServerError(fmt.Sprintf("Failed to marshal result: %s", err)))
			return
		}

		protocol.WriteData(w, marshaled)
		return
	}

	if r.Method == http.MethodPost {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			protocol.WriteError(w, protocol.BadRequest(fmt.Sprintf("Failed to read request body: %s", err)))
			return
		}

		var request ScheduleTurndownRequest
		err = json.Unmarshal(data, &request)
		if err != nil {
			protocol.WriteError(w, protocol.InternalServerError(fmt.Sprintf("Failed to unmarshal request: %s", err)))
			return
		}

		if request.Repeat == "" {
			request.Repeat = TurndownJobRepeatNone
		}

		// test to see if there's already a schedule present
		s := te.scheduler.GetSchedule()
		if s != nil {
			protocol.WriteError(w, protocol.InternalServerError("Schedule already exists"))
			return
		}

		_, err = te.client.KubecostV1alpha1().TurndownSchedules().Create(
			context.TODO(),
			&v1alpha1.TurndownSchedule{
				ObjectMeta: v1.ObjectMeta{
					GenerateName: "scheduled-turndown-",
					Finalizers: []string{
						TurndownScheduleFinalizer,
					},
				},
				Spec: v1alpha1.TurndownScheduleSpec{
					Start:  v1.NewTime(request.Start),
					End:    v1.NewTime(request.End),
					Repeat: request.Repeat,
				},
			},
			v1.CreateOptions{})
		if err != nil {
			protocol.WriteError(w, protocol.InternalServerError(fmt.Sprintf("Failed to create schedule: %s", err)))
			return
		}

		// Poll scheduler until the resource controller has propagated the schedule
		var schedule *Schedule = nil
		err = wait.PollImmediate(time.Second*1, time.Second*30, func() (bool, error) {
			schedule = te.scheduler.GetSchedule()
			if schedule != nil {
				return true, nil
			}

			return false, nil
		})

		if err != nil {
			protocol.WriteError(w, protocol.InternalServerError(fmt.Sprintf("Failed to get schedule: %s", err)))
			return
		}

		marshaled, err := json.Marshal(schedule)
		if err != nil {
			protocol.WriteError(w, protocol.InternalServerError(fmt.Sprintf("Failed to marshal result: %s", err)))
			return
		}

		w.Write(marshaled)
		return
	}

	protocol.WriteError(w, protocol.NotFound())
}

func (te *TurndownEndpoints) HandleCancelSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	scheduleList, err := te.client.KubecostV1alpha1().TurndownSchedules().List(context.TODO(), v1.ListOptions{})
	if err != nil {
		protocol.WriteError(w, protocol.InternalServerError(fmt.Sprintf("Failed to list schedules: %s", err)))
		return
	}

	var toCancel *v1alpha1.TurndownSchedule
	for _, schedule := range scheduleList.Items {
		if schedule.Status.State == ScheduleStateSuccess {
			toCancel = &schedule
			break
		}
	}

	if toCancel != nil {
		err = te.client.KubecostV1alpha1().TurndownSchedules().Delete(context.TODO(), toCancel.Name, v1.DeleteOptions{})
		if err != nil {
			protocol.WriteError(w, protocol.InternalServerError(fmt.Sprintf("Failed to delete schedule: %s", err)))
			return
		}
	}
}

func (te *TurndownEndpoints) HandleInitEnvironment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	isOnNode, err := te.turndown.IsRunningOnTurndownNode()
	if nil != err {
		protocol.WriteError(w, protocol.InternalServerError(fmt.Sprintf("Failed to list nodes: %s", err)))
		return
	}

	if !isOnNode {
		err := te.turndown.PrepareTurndownEnvironment()
		if err != nil {
			protocol.WriteError(w, protocol.InternalServerError(fmt.Sprintf("Failed to prepare turndown environment: %s", err)))
			return
		}
	} else {
		log.Infof("Already running on correct turndown node. No need to setup environment")
	}
}
