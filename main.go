package main

import (
	"flag"
	"net/http"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kubecost/kubecost-turndown/turndown"

	"k8s.io/klog"
)

func runWebServer(scheduler *turndown.TurndownScheduler, manager turndown.TurndownManager, provider turndown.ComputeProvider) {
	mux := http.NewServeMux()

	endpoints := turndown.NewTurndownEndpoints(scheduler, manager, provider)

	mux.HandleFunc("/schedule", endpoints.HandleStartSchedule)
	mux.HandleFunc("/cancel", endpoints.HandleCancelSchedule)
	mux.HandleFunc("/setServiceKey", endpoints.HandleSetServiceKey)

	klog.Fatal(http.ListenAndServe(":9731", mux))
}

func initKubernetes() kubernetes.Interface {
	// Kubernetes API setup
	kc, err := rest.InClusterConfig()
	if err != nil {
		return nil
	}

	kubeClient, err := kubernetes.NewForConfig(kc)
	if err != nil {
		return nil
	}

	return kubeClient
}

func main() {
	klog.InitFlags(nil)
	flag.Set("v", "5")
	flag.Parse()

	node := os.Getenv("NODE_NAME")
	klog.V(1).Infof("Running Kubecost Turndown on: %s", node)

	// Setup Components
	kubeClient := initKubernetes()
	scheduleStore := turndown.NewDiskScheduleStore("/var/configs/startup.json")
	provider := turndown.NewGKEProvider(kubeClient)
	manager := turndown.NewKubernetesTurndownManager(kubeClient, provider, node)
	scheduler := turndown.NewTurndownScheduler(manager, scheduleStore)

	runWebServer(scheduler, manager, provider)
}
