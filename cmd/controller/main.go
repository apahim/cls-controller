package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/apahim/cls-controller/internal/config"
	"github.com/apahim/cls-controller/internal/controller"
	"github.com/apahim/cls-controller/internal/sdk"
	"go.uber.org/zap"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

func main() {
	var configFile string
	var kubeconfigPath string
	var controllerName string

	flag.StringVar(&configFile, "config", "", "Path to controller configuration file")
	flag.StringVar(&kubeconfigPath, "kubeconfig-path", "", "Path to kubeconfig file")
	flag.StringVar(&controllerName, "controller-name", "", "Name of the controller instance")
	flag.Parse()

	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Sync()

	logger.Info("Starting CLS Controller",
		zap.String("version", Version),
		zap.String("git_commit", GitCommit),
		zap.String("build_time", BuildTime),
	)

	// Load configuration
	cfg, err := config.Load(configFile)
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.Error(err))
	}

	// Override controller name if provided via flag
	if controllerName != "" {
		cfg.ControllerName = controllerName
	}

	if cfg.ControllerName == "" {
		logger.Fatal("Controller name must be specified via config file or --controller-name flag")
	}

	logger.Info("Configuration loaded",
		zap.String("controller_name", cfg.ControllerName),
		zap.String("project_id", cfg.ProjectID),
		zap.String("api_base_url", cfg.APIBaseURL),
	)

	// Initialize Kubernetes client
	k8sConfig, err := getKubernetesConfig(kubeconfigPath)
	if err != nil {
		logger.Fatal("Failed to get Kubernetes config", zap.Error(err))
	}

	k8sClient, err := client.New(k8sConfig, client.Options{
		Scheme: controller.GetScheme(),
	})
	if err != nil {
		logger.Fatal("Failed to create Kubernetes client", zap.Error(err))
	}

	// Create controller manager
	mgr, err := ctrl.NewManager(k8sConfig, ctrl.Options{
		Scheme: controller.GetScheme(),
	})
	if err != nil {
		logger.Fatal("Failed to create controller manager", zap.Error(err))
	}

	// Create the generalized controller
	clsController, err := controller.New(cfg, k8sClient, logger)
	if err != nil {
		logger.Fatal("Failed to create controller", zap.Error(err))
	}

	// Create SDK client for event handling and status reporting
	sdkClient, err := sdk.NewClient(cfg.SDKConfig, clsController)
	if err != nil {
		logger.Fatal("Failed to create SDK client", zap.Error(err))
	}

	// Initialize controller with SDK client
	clsController.SetSDKClient(sdkClient)

	// Setup controller with manager
	if err := clsController.SetupWithManager(mgr); err != nil {
		logger.Fatal("Failed to setup controller with manager", zap.Error(err))
	}

	logger.Info("CLS Controller starting",
		zap.String("controller_name", cfg.ControllerName),
	)

	// Start SDK client for event processing
	if err := sdkClient.Start(); err != nil {
		logger.Fatal("Failed to start SDK client", zap.Error(err))
	}

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("Shutdown signal received, stopping controller...")
		cancel()
	}()

	// Start the controller manager
	logger.Info("CLS Controller started successfully",
		zap.String("controller_name", cfg.ControllerName),
	)

	if err := mgr.Start(ctx); err != nil {
		logger.Error("Controller manager failed", zap.Error(err))
	}

	// Graceful shutdown
	logger.Info("Shutting down controller...")

	// Stop SDK client
	if err := sdkClient.Stop(); err != nil {
		logger.Error("Failed to stop SDK client", zap.Error(err))
	}

	logger.Info("Controller stopped successfully")
}

// getKubernetesConfig returns a Kubernetes REST config
func getKubernetesConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		// Use provided kubeconfig file
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	// Try in-cluster config first
	if config, err := rest.InClusterConfig(); err == nil {
		return config, nil
	}

	// Fall back to default kubeconfig
	return clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
}