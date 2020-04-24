package turndown

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/kubecost/cluster-turndown/pkg/apis/turndownschedule/v1alpha1"
	clientset "github.com/kubecost/cluster-turndown/pkg/generated/clientset/versioned"
	"github.com/kubecost/cluster-turndown/pkg/turndown/provider"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

// DataEnvelope is a generic wrapper struct for http response data
type DataEnvelope struct {
	Code   int         `json:"code"`
	Status string      `json:"status"`
	Data   interface{} `json:"data"`
}

// ScheduleTurndownRequest is the POST encoding used to
type ScheduleTurndownRequest struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Repeat string    `json:"repeat,omitempty"`
}

type TurndownEndpoints struct {
	kubeClient kubernetes.Interface
	client     clientset.Interface
	scheduler  *TurndownScheduler
	turndown   TurndownManager
	provider   provider.TurndownProvider
}

func NewTurndownEndpoints(
	kubeClient kubernetes.Interface,
	client clientset.Interface,
	scheduler *TurndownScheduler,
	turndown TurndownManager,
	provider provider.TurndownProvider) *TurndownEndpoints {

	return &TurndownEndpoints{
		kubeClient: kubeClient,
		client:     client,
		scheduler:  scheduler,
		turndown:   turndown,
		provider:   provider,
	}
}

func (te *TurndownEndpoints) HandleStartSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		schedule := te.scheduler.GetSchedule()
		if schedule == nil {
			w.Write(wrapData(struct{}{}, nil))
			return
		}

		w.Write(wrapData(schedule, nil))
		return
	}

	if r.Method == http.MethodPost {
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			w.Write(wrapData(nil, err))
			return
		}

		var request ScheduleTurndownRequest
		err = json.Unmarshal(data, &request)
		if err != nil {
			w.Write(wrapData(nil, err))
			return
		}

		if request.Repeat == "" {
			request.Repeat = TurndownJobRepeatNone
		}

		// test to see if there's already a schedule present
		s := te.scheduler.GetSchedule()
		if s != nil {
			w.Write(wrapData(nil, fmt.Errorf("Schedule already exists")))
			return
		}

		_, err = te.client.KubecostV1alpha1().TurndownSchedules().Create(&v1alpha1.TurndownSchedule{
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
		})
		if err != nil {
			w.Write(wrapData(nil, err))
			return
		}

		// Poll scheduler until the resource controller has propagated the schedule
		var schedule *Schedule = nil
		err = wait.PollImmediate(time.Second*1, time.Second*30, func() (bool, error) {
			schedule = te.scheduler.GetSchedule()
			if schedule != nil {
				return true, nil
			}

			return false, fmt.Errorf("Schedule not available")
		})

		if err != nil {
			w.Write(wrapData(nil, err))
			return
		}

		w.Write(wrapData(schedule, nil))
		return
	}

	resp, _ := json.Marshal(&DataEnvelope{
		Code:   http.StatusNotFound,
		Status: "error",
		Data:   fmt.Sprintf("Not Found for method type: %s", r.Method),
	})
	w.Write(resp)
}

func (te *TurndownEndpoints) HandleCancelSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	scheduleList, err := te.client.KubecostV1alpha1().TurndownSchedules().List(v1.ListOptions{})
	if err != nil {
		w.Write(wrapData(nil, err))
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
		err = te.client.KubecostV1alpha1().TurndownSchedules().Delete(toCancel.Name, &v1.DeleteOptions{})
		if err != nil {
			w.Write(wrapData(nil, err))
			return
		}
	}

	w.Write(wrapData("", nil))
}

func (te *TurndownEndpoints) HandleInitEnvironment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	isOnNode, err := te.turndown.IsRunningOnTurndownNode()
	if nil != err {
		w.Write(wrapData(nil, err))
		return
	}

	if !isOnNode {
		err := te.turndown.PrepareTurndownEnvironment()
		if err != nil {
			w.Write(wrapData(nil, err))
			return
		}
	} else {
		klog.Infof("Already running on correct turndown node. No need to setup environment")
	}

	w.Write(wrapData("", nil))
}

func wrapData(data interface{}, err error) []byte {
	var resp []byte

	if err != nil {
		klog.V(1).Infof("Error returned to client: %s", err.Error())
		resp, _ = json.Marshal(&DataEnvelope{
			Code:   http.StatusInternalServerError,
			Status: "error",
			Data:   err.Error(),
		})
	} else {
		resp, _ = json.Marshal(&DataEnvelope{
			Code:   http.StatusOK,
			Status: "success",
			Data:   data,
		})

	}

	return resp
}
