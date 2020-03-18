package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	clientset "github.com/kubecost/cluster-turndown/pkg/generated/clientset/versioned"
	informers "github.com/kubecost/cluster-turndown/pkg/generated/informers/externalversions"

	"github.com/kubecost/cluster-turndown/pkg/signals"
	"github.com/kubecost/cluster-turndown/pkg/turndown"
	"github.com/kubecost/cluster-turndown/pkg/turndown/provider"
	"github.com/kubecost/cluster-turndown/pkg/turndown/strategy"

	"k8s.io/klog"
)

// Run web server with turndown endpoints
func runWebServer(kubeClient kubernetes.Interface, client clientset.Interface, scheduler *turndown.TurndownScheduler, manager turndown.TurndownManager, provider provider.ComputeProvider) {
	mux := http.NewServeMux()

	endpoints := turndown.NewTurndownEndpoints(kubeClient, client, scheduler, manager, provider)

	mux.HandleFunc("/schedule", endpoints.HandleStartSchedule)
	mux.HandleFunc("/cancel", endpoints.HandleCancelSchedule)

	klog.Fatal(http.ListenAndServe(":9731", mux))
}

// Initialize Kubernetes Client as well as the CRD Client
func initKubernetes(isLocal bool) (kubernetes.Interface, clientset.Interface, error) {
	var kc *rest.Config
	var err error

	// For local testing, use kubeconfig in home directory
	if isLocal {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, err
		}

		configFile := filepath.Join(homeDir, ".kube", "config")
		klog.V(3).Infof("KubeConfig Path: %s", configFile)

		kc, err = clientcmd.BuildConfigFromFlags("", configFile)
		if err != nil {
			return nil, nil, err
		}
	} else {
		kc, err = rest.InClusterConfig()
		if err != nil {
			return nil, nil, err
		}
	}

	kubeClient, err := kubernetes.NewForConfig(kc)
	if err != nil {
		return nil, nil, err
	}

	client, err := clientset.NewForConfig(kc)
	if err != nil {
		return nil, nil, err
	}

	return kubeClient, client, nil
}

// Runs a controller loop to ensure that our custom resource definition: TurndownSchedule is handled properly
// by the API.
func runTurndownResourceController(kubeClient kubernetes.Interface, tdClient clientset.Interface, scheduler *turndown.TurndownScheduler, stopCh <-chan struct{}) {
	tdInformer := informers.NewSharedInformerFactory(tdClient, time.Second*30)
	controller := turndown.NewTurndownScheduleResourceController(kubeClient, tdClient, scheduler, tdInformer.Kubecost().V1alpha1().TurndownSchedules())
	tdInformer.Start(stopCh)

	go func(c *turndown.TurndownScheduleResourceController, s <-chan struct{}) {
		if err := c.Run(1, s); err != nil {
			klog.Fatalf("Error running controller: %s", err.Error())
		}
	}(controller, stopCh)
}

// For now, we'll choose our strategy based on the provider, but functionally, there is
// no dependency.
func strategyForProvider(c kubernetes.Interface, p provider.ComputeProvider) (strategy.TurndownStrategy, error) {
	switch v := p.(type) {
	case *provider.GKEProvider:
		return strategy.NewMasterlessTurndownStrategy(c, p), nil
	case *provider.AWSProvider:
		return strategy.NewStandardTurndownStrategy(c, p), nil
	default:
		return nil, fmt.Errorf("No strategy available for: %+v", v)
	}
}

func main() {
	klog.InitFlags(nil)
	flag.Set("v", "3")
	flag.Parse()

	stopCh := signals.SetupSignalHandler()

	node := os.Getenv("NODE_NAME")
	klog.V(1).Infof("Running Kubecost Turndown on: %s", node)

	// Setup Components
	kubeClient, tdClient, err := initKubernetes(false)
	if err != nil {
		klog.Fatalf("Failed to initialize kubernetes client: %s", err.Error())
	}

	// Schedule Persistence via Kubernetes Custom Resource Definition
	scheduleStore := turndown.NewKubernetesScheduleStore(tdClient)
	//scheduleStore := turndown.NewDiskScheduleStore("/var/configs/schedule.json")

	// Platform Provider for Turndown API
	computeProvider, err := provider.NewProvider(kubeClient)
	if err != nil {
		klog.V(1).Infof("[Error]: Failed to determine provider: %s", err.Error())
		return
	}

	// Validate ComputeProvider
	err = provider.Validate(computeProvider, 5)
	if err != nil {
		klog.V(1).Infof("[Error]: Failed to validate provider: %s", err.Error())
		return
	}

	// Determine the best turndown strategy to use based on provider
	strategy, err := strategyForProvider(kubeClient, computeProvider)
	if err != nil {
		klog.V(1).Infof("Failed to create strategy: %s", err.Error())
		return
	}

	// Turndown Management and Scheduler
	manager := turndown.NewKubernetesTurndownManager(kubeClient, computeProvider, strategy, node)
	scheduler := turndown.NewTurndownScheduler(manager, scheduleStore)

	// Run TurndownSchedule Kubernetes Resource Controller
	runTurndownResourceController(kubeClient, tdClient, scheduler, stopCh)

	// Run Turndown Endpoints
	runWebServer(kubeClient, tdClient, scheduler, manager, computeProvider)
}
