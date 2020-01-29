package turndown

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/kubecost/kubecost-turndown/turndown/provider"
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
	scheduler *TurndownScheduler
	turndown  TurndownManager
	provider  provider.ComputeProvider
}

func NewTurndownEndpoints(scheduler *TurndownScheduler, turndown TurndownManager, provider provider.ComputeProvider) *TurndownEndpoints {
	return &TurndownEndpoints{
		scheduler: scheduler,
		turndown:  turndown,
		provider:  provider,
	}
}

func (te *TurndownEndpoints) HandleStartSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method == http.MethodGet {
		schedule := te.scheduler.GetSchedule()
		if schedule == nil {
			w.Write(wrapData(nil, fmt.Errorf("No schedule available.")))
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

		err = te.scheduler.ScheduleTurndown(request.Start, request.End, request.Repeat)
		if err != nil {
			w.Write(wrapData(nil, err))
			return
		}

		schedule := te.scheduler.GetSchedule()

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
	w.Header().Set("Access-Control-Allow-Origin", "*")

	err := te.scheduler.Cancel()
	if err != nil {
		w.Write(wrapData(nil, err))
		return
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

func (te *TurndownEndpoints) HandleSetServiceKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		resp, _ := json.Marshal(&DataEnvelope{
			Code:   http.StatusNotFound,
			Status: "error",
			Data:   fmt.Sprintf("Not Found for method type: %s", r.Method),
		})
		w.Write(resp)
		return
	}

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.Write(wrapData(nil, err))
		return
	}

	serviceAccountKey := string(data)
	err = te.provider.SetServiceAccount(serviceAccountKey)
	if err != nil {
		w.Write(wrapData(nil, err))
		return
	}

	w.Write(wrapData("Success", nil))
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
