package turndown

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"k8s.io/klog"
)

type userAgentTransport struct {
	userAgent string
	base      http.RoundTripper
}

func (t userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", t.userAgent)
	return t.base.RoundTrip(req)
}

// DataEnvelope is a generic wrapper struct for http response data
type DataEnvelope struct {
	Code   int         `json:"code"`
	Status string      `json:"status"`
	Data   interface{} `json:"data"`
}

type TurndownEndpoints struct {
	scheduler *TurndownScheduler
	turndown  TurndownManager
	provider  ComputeProvider
}

func NewTurndownEndpoints(scheduler *TurndownScheduler, turndown TurndownManager, provider ComputeProvider) *TurndownEndpoints {
	return &TurndownEndpoints{
		scheduler: scheduler,
		turndown:  turndown,
		provider:  provider,
	}
}

func (te *TurndownEndpoints) HandleStartSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	q := r.URL.Query()
	start := q.Get("start")
	end := q.Get("end")
	repeat := q.Get("repeat")

	var err error = nil
	if start == "" {
		err = fmt.Errorf("No 'start' date parameter provided.")
	} else if end == "" {
		err = fmt.Errorf("No 'end' date parameter provided.")
	} else if repeat == "" {
		err = fmt.Errorf("No 'repeat' type provided. Must set to 'none', 'daily', or 'weekly'.")
	}

	if err != nil {
		w.Write(wrapData(nil, err))
		return
	}

	startTime, err := time.Parse(RFC3339Milli, start)
	if err != nil {
		w.Write(wrapData(nil, fmt.Errorf("Failed to parse 'start' time.")))
		return
	}
	endTime, err := time.Parse(RFC3339Milli, end)
	if err != nil {
		w.Write(wrapData(nil, fmt.Errorf("Failed to parse 'end' time.")))
		return
	}

	err = te.scheduler.ScheduleTurndown(startTime, endTime, repeat)
	if err != nil {
		w.Write(wrapData(nil, err))
		return
	}

	schedule := te.scheduler.GetSchedule()

	w.Write(wrapData(&schedule, nil))
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
			Data:   []byte(err.Error()),
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
