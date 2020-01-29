package turndown

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/kubecost/kubecost-turndown/file"
)

type Schedule struct {
	Current           string            `json:"current"`
	ScaleDownID       string            `json:"scaleDownId"`
	ScaleDownTime     time.Time         `json:"scaleDownTime"`
	ScaleDownMetadata map[string]string `json:"scaleDownMetadata"`
	ScaleUpID         string            `json:"scaleUpID"`
	ScaleUpTime       time.Time         `json:"scaleUpTime"`
	ScaleUpMetadata   map[string]string `json:"scaleUpMetadata"`
}

// Persistent Schedule Storage interface for storing and retrieving a single stored schedule.
type ScheduleStore interface {
	GetSchedule() (*Schedule, error)
	SetSchedule(schedule *Schedule) error
	Clear()
}

// Disk based implementation of persistent schedule storage.
type DiskScheduleStore struct {
	file string
}

// Creates a new disk schedule storage instance
func NewDiskScheduleStore(file string) ScheduleStore {
	return &DiskScheduleStore{
		file: file,
	}
}

func (dss *DiskScheduleStore) GetSchedule() (*Schedule, error) {
	if !file.FileExists(dss.file) {
		return nil, fmt.Errorf("No schedule exists")
	}

	data, err := ioutil.ReadFile(dss.file)
	if err != nil {
		return nil, fmt.Errorf("No schedule exists")
	}

	var s Schedule
	err = json.Unmarshal(data, &s)
	if err != nil {
		return nil, err
	}

	return &s, nil
}

func (dss *DiskScheduleStore) SetSchedule(schedule *Schedule) error {
	data, err := json.Marshal(schedule)
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(dss.file, data, 0644)
	if err != nil {
		return err
	}

	return nil
}

func (dss *DiskScheduleStore) Clear() {
	if !file.FileExists(dss.file) {
		return
	}

	os.Remove(dss.file)
}
