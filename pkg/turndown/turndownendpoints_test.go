package turndown

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cp "github.com/kubecost/cluster-turndown/v2/pkg/cluster/provider"
	clientset "github.com/kubecost/cluster-turndown/v2/pkg/generated/clientset/versioned"
	"github.com/kubecost/cluster-turndown/v2/pkg/generated/clientset/versioned/fake"
	"github.com/kubecost/cluster-turndown/v2/pkg/turndown/provider"
	"github.com/kubecost/cluster-turndown/v2/pkg/turndown/strategy"
)

func NewMockTurndownEndpoints(
	client clientset.Interface,
	scheduler *TurndownScheduler,
	turndown TurndownManager) *TurndownEndpoints {

	return &TurndownEndpoints{
		client:    client,
		scheduler: scheduler,
		turndown:  turndown,
	}
}

func Test_HandleCancelSchedule(t *testing.T) {
	const node = "test-node"

	mockClient := fake.NewSimpleClientset()
	mockScheduleStore := NewKubernetesScheduleStore(mockClient)

	mockProvider := cp.NewMockClusterProvider()

	mockTurndownProvider := provider.NewMockTurndownProvider(mockProvider)
	mockStrategy := strategy.NewStandardTurndownStrategy(nil, mockTurndownProvider)
	mockManager := NewKubernetesTurndownManager(nil, mockTurndownProvider, mockStrategy, node)

	mockScheduler := NewTurndownScheduler(mockManager, mockScheduleStore)

	te := NewMockTurndownEndpoints(mockClient, mockScheduler, mockManager)

	req, err := http.NewRequest("GET", "/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(te.HandleCancelSchedule)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v, want %v", status, http.StatusOK)
	}

}

func Test_HandleStartSchedule(t *testing.T) {
	const node = "test-node"

	mockClient := fake.NewSimpleClientset()
	mockScheduleStore := NewKubernetesScheduleStore(mockClient)

	mockProvider := cp.NewMockClusterProvider()

	mockTurndownProvider := provider.NewMockTurndownProvider(mockProvider)
	mockStrategy := strategy.NewStandardTurndownStrategy(nil, mockTurndownProvider)
	mockManager := NewKubernetesTurndownManager(nil, mockTurndownProvider, mockStrategy, node)

	mockScheduler := NewTurndownScheduler(mockManager, mockScheduleStore)

	te := NewMockTurndownEndpoints(mockClient, mockScheduler, mockManager)

	//invalid body
	body := strings.NewReader("Hi")

	req, err := http.NewRequest("POST", "/start", body)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(te.HandleStartSchedule)
	handler.ServeHTTP(rr, req)

	t.Log(rr.Body)

	if status := rr.Code; status != http.StatusInternalServerError {
		t.Errorf("handler returned wrong status code: got %v, want %v", status, http.StatusInternalServerError)
	}

}
