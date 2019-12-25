package main

import (
	"flag"
	"net/http"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

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

func initLocalKubernetes() kubernetes.Interface {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir = os.Getenv("USERPROFILE")
	}

	// Kubernetes API setup
	configFile := filepath.Join(homeDir, ".kube", "config")
	klog.Infof("KubeConfig Path: %s", configFile)

	kc, err := clientcmd.BuildConfigFromFlags("", configFile)
	if err != nil {
		klog.Fatalf("Fatal: %s", err.Error())
		return nil
	}

	k8s, err := kubernetes.NewForConfig(kc)
	if err != nil {
		klog.Fatalf("Fatal: %s", err.Error())
		return nil
	}

	return k8s
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
