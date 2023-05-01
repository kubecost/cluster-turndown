package turndown

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/kubecost/cluster-turndown/v2/pkg/file"

	"github.com/kubecost/cluster-turndown/v2/pkg/apis/turndownschedule/v1alpha1"
	clientset "github.com/kubecost/cluster-turndown/v2/pkg/generated/clientset/versioned"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rs/zerolog/log"
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
	Create(schedule *Schedule) error
	Update(schedule *Schedule) error
	Complete()
	Clear()
}

type KubernetesScheduleStore struct {
	client clientset.Interface
}

func NewKubernetesScheduleStore(client clientset.Interface) ScheduleStore {
	return &KubernetesScheduleStore{
		client: client,
	}
}

func WriteScheduleFromStatus(schedule *Schedule, status *v1alpha1.TurndownScheduleStatus) {
	if schedule == nil {
		return
	}

	schedule.Current = status.Current
	schedule.ScaleDownID = status.ScaleDownID
	schedule.ScaleUpID = status.ScaleUpID
	schedule.ScaleDownMetadata = status.ScaleDownMetadata
	schedule.ScaleUpMetadata = status.ScaleUpMetadata
	schedule.ScaleDownTime = status.ScaleDownTime.Time
	schedule.ScaleUpTime = status.ScaleUpTime.Time
}

func WriteStatusFromSchedule(status *v1alpha1.TurndownScheduleStatus, schedule *Schedule) {
	if status == nil {
		return
	}

	status.Current = schedule.Current
	status.ScaleDownID = schedule.ScaleDownID
	status.ScaleUpID = schedule.ScaleUpID
	status.ScaleDownMetadata = schedule.ScaleDownMetadata
	status.ScaleUpMetadata = schedule.ScaleUpMetadata
	status.ScaleDownTime = v1.NewTime(schedule.ScaleDownTime)
	status.ScaleUpTime = v1.NewTime(schedule.ScaleUpTime)
	status.LastUpdated = v1.NewTime(time.Now().UTC())
}

func (kss *KubernetesScheduleStore) GetSchedule() (*Schedule, error) {
	tds, err := kss.client.KubecostV1alpha1().TurndownSchedules().List(context.TODO(), v1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, td := range tds.Items {
		if td.Status.State == ScheduleStateSuccess {
			schedule := &Schedule{}
			WriteScheduleFromStatus(schedule, &td.Status)

			return schedule, nil
		}
	}

	return nil, fmt.Errorf("No schedule exists")
}

func (kss *KubernetesScheduleStore) Create(schedule *Schedule) error {
	// Kubernetes persistent storage is driven by the resource controller,
	// so creation is already handled
	return nil
}

func (kss *KubernetesScheduleStore) Update(schedule *Schedule) error {
	tds, err := kss.client.KubecostV1alpha1().TurndownSchedules().List(context.TODO(), v1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing TurndownSchedules: %w", err)
	}

	for _, td := range tds.Items {
		if td.Status.State == ScheduleStateSuccess {
			tdCopy := td.DeepCopy()
			WriteStatusFromSchedule(&tdCopy.Status, schedule)

			_, err := kss.client.KubecostV1alpha1().TurndownSchedules().UpdateStatus(context.TODO(), tdCopy, v1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("updating status of TurndownSchedule '%s': %w")
			}
			return nil
		}
	}

	return fmt.Errorf("No schedule exists")
}

func (kss *KubernetesScheduleStore) Complete() {
	tds, err := kss.client.KubecostV1alpha1().TurndownSchedules().List(context.TODO(), v1.ListOptions{})
	if err != nil {
		return
	}

	for _, td := range tds.Items {
		if td.Status.State == ScheduleStateSuccess {
			tdCopy := td.DeepCopy()
			tdCopy.Status.State = ScheduleStateCompleted
			tdCopy.Status.LastUpdated = v1.NewTime(time.Now().UTC())

			_, err := kss.client.KubecostV1alpha1().TurndownSchedules().UpdateStatus(context.TODO(), tdCopy, v1.UpdateOptions{})
			if err != nil {
				log.Error().Err(err).Msgf("Failed to UpdateStatus in kss.Complete()")
			}
			return
		}
	}
}

func (kss *KubernetesScheduleStore) Clear() {
	tds, err := kss.client.KubecostV1alpha1().TurndownSchedules().List(context.TODO(), v1.ListOptions{})
	if err != nil {
		return
	}

	for _, td := range tds.Items {
		if td.Status.State == ScheduleStateSuccess {
			tdCopy := td.DeepCopy()
			tdCopy.Status.State = ScheduleStateCompleted
			tdCopy.Status.LastUpdated = v1.NewTime(time.Now().UTC())

			_, err := kss.client.KubecostV1alpha1().TurndownSchedules().UpdateStatus(context.TODO(), tdCopy, v1.UpdateOptions{})
			if err != nil {
				log.Error().Err(err).Msgf("Failed to UpdateStatus in kss.Clear()")
			}
			return
		}
	}
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

func (dss *DiskScheduleStore) Create(schedule *Schedule) error {
	// With regards to storing on disk, this is identical to Update
	// we just write the schedule object straight to disk
	return dss.Update(schedule)
}

func (dss *DiskScheduleStore) Update(schedule *Schedule) error {
	data, err := json.Marshal(schedule)
	if err != nil {
		return fmt.Errorf("marshaling schedule: %w", err)
	}
	err = ioutil.WriteFile(dss.file, data, 0644)
	if err != nil {
		return fmt.Errorf("writing schedule file: %w", err)
	}

	return nil
}

func (dss *DiskScheduleStore) Complete() {
	dss.Clear()
}

func (dss *DiskScheduleStore) Clear() {
	if !file.FileExists(dss.file) {
		return
	}

	os.Remove(dss.file)
}
