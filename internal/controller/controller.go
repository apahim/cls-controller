package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/apahim/cls-controller/internal/config"
	"github.com/apahim/cls-controller/internal/crd"
	"github.com/apahim/cls-controller/internal/template"
	"github.com/apahim/cls-controller/internal/client"
	"github.com/apahim/cls-controller/internal/sdk"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Controller represents the simplified CLS controller
type Controller struct {
	config       *config.Config
	k8sClient    ctrlclient.Client
	logger       *zap.Logger

	// SDK client for event handling and status reporting
	sdkClient    *sdk.Client

	// Template engine for rendering resources
	templateEngine *template.Engine

	// Client manager for different target types
	clientManager  *client.Manager

	// Current controller configuration
	controllerConfig *crd.ControllerConfig
}

// New creates a new simplified controller
func New(cfg *config.Config, k8sClient ctrlclient.Client, logger *zap.Logger) (*Controller, error) {
	templateEngine := template.NewEngine(cfg.TemplateTimeout, logger)

	clientManager, err := client.NewManager(k8sClient, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create client manager: %w", err)
	}

	return &Controller{
		config:         cfg,
		k8sClient:      k8sClient,
		logger:         logger.Named("controller"),
		templateEngine: templateEngine,
		clientManager:  clientManager,
	}, nil
}

// SetSDKClient sets the SDK client after controller creation
func (c *Controller) SetSDKClient(sdkClient *sdk.Client) {
	c.sdkClient = sdkClient
}

// SetupWithManager sets up the controller with the manager
func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&crd.ControllerConfig{}).
		Complete(c)
}

// Reconcile handles ControllerConfig reconciliation
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := c.logger.With(
		zap.String("namespace", req.Namespace),
		zap.String("name", req.Name),
	)

	// Only reconcile our specific controller configuration
	if req.Name != c.config.ConfigName || req.Namespace != c.config.ConfigNamespace {
		log.Debug("Ignoring ControllerConfig not for this controller instance")
		return reconcile.Result{}, nil
	}

	log.Info("Reconciling ControllerConfig")

	// Fetch the ControllerConfig
	var controllerConfig crd.ControllerConfig
	if err := c.k8sClient.Get(ctx, req.NamespacedName, &controllerConfig); err != nil {
		if ctrlclient.IgnoreNotFound(err) != nil {
			log.Error("Failed to fetch ControllerConfig", zap.Error(err))
			return reconcile.Result{}, err
		}

		// ControllerConfig was deleted
		log.Info("ControllerConfig deleted, clearing configuration")
		c.controllerConfig = nil
		return reconcile.Result{}, nil
	}

	// Validate and store the configuration
	if err := c.validateControllerConfig(&controllerConfig); err != nil {
		log.Error("Invalid ControllerConfig", zap.Error(err))

		// Update status to Invalid
		if updateErr := c.updateConfigStatus(ctx, &controllerConfig, crd.PhaseInvalid, err.Error()); updateErr != nil {
			log.Error("Failed to update ControllerConfig status", zap.Error(updateErr))
		}

		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Store the valid configuration
	c.controllerConfig = &controllerConfig

	// Update client manager with the namespace where secrets should be read from
	c.clientManager.SetSecretNamespace(controllerConfig.GetNamespace())

	// Compile templates
	if err := c.templateEngine.CompileTemplates(&controllerConfig); err != nil {
		log.Error("Failed to compile templates", zap.Error(err))

		// Update status to Invalid
		if updateErr := c.updateConfigStatus(ctx, &controllerConfig, crd.PhaseInvalid, fmt.Sprintf("Template compilation failed: %v", err)); updateErr != nil {
			log.Error("Failed to update ControllerConfig status", zap.Error(updateErr))
		}

		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Update status to Valid
	if err := c.updateConfigStatus(ctx, &controllerConfig, crd.PhaseValid, "Controller configuration is valid and ready"); err != nil {
		log.Error("Failed to update ControllerConfig status", zap.Error(err))
		return reconcile.Result{}, err
	}

	log.Info("ControllerConfig reconciled successfully",
		zap.String("controller_name", controllerConfig.Spec.Name),
		zap.Int("resources", len(controllerConfig.Spec.Resources)),
		zap.Int("preconditions", len(controllerConfig.Spec.Preconditions)),
	)

	return reconcile.Result{}, nil
}

// HandleClusterEvent handles cluster lifecycle events (implements sdk.EventHandler)
func (c *Controller) HandleClusterEvent(event *sdk.ClusterEvent) error {
	log := c.logger.With(
		zap.String("event_type", event.Type),
		zap.String("cluster_id", event.ClusterID),
		zap.Int64("generation", event.Generation),
	)

	log.Info("Handling cluster event")

	// Check if we have a valid controller configuration
	if c.controllerConfig == nil {
		log.Warn("No controller configuration loaded, ignoring event")
		return nil
	}

	// Handle deletion events early - deleted clusters can't be fetched from API
	if event.Type == sdk.EventTypeClusterDeleted {
		log.Info("Processing cluster deletion event")
		return c.handleClusterDeleted(event, nil)
	}

	// Fetch cluster spec from simplified API for non-deletion events
	ctx := context.Background()
	apiClient := c.sdkClient.GetAPIClient()
	if apiClient == nil {
		log.Error("API client not available")
		return c.reportError(event, "APIClientUnavailable", fmt.Errorf("API client not available"))
	}

	cluster, err := apiClient.GetCluster(ctx, event.ClusterID)
	if err != nil {
		log.Error("Failed to fetch cluster from API", zap.Error(err))
		return c.reportError(event, "ClusterFetchFailed", err)
	}

	log.Debug("Fetched cluster spec",
		zap.String("cluster_name", cluster.Name),
		zap.Int64("cluster_generation", cluster.Generation),
	)

	// Check preconditions
	if !c.evaluatePreconditions(cluster) {
		log.Info("Preconditions not met, skipping resource creation")
		return c.reportPreconditionFailure(event, cluster)
	}

	// Process the event based on type
	switch event.Type {
	case sdk.EventTypeClusterCreated:
		return c.handleClusterCreated(event, cluster)
	case sdk.EventTypeClusterUpdated:
		return c.handleClusterUpdated(event, cluster)
	case sdk.EventTypeClusterReconcile:
		return c.handleClusterReconcile(event, cluster)
	default:
		log.Warn("Unknown event type", zap.String("event_type", event.Type))
		return nil
	}
}

// HandleNodePoolEvent handles nodepool events (not implemented yet)
func (c *Controller) HandleNodePoolEvent(event *sdk.NodePoolEvent) error {
	c.logger.Debug("NodePool events not implemented", zap.String("event_type", event.Type))
	return nil
}

// HandleControllerEvent handles controller events (not implemented yet)
func (c *Controller) HandleControllerEvent(event *sdk.ControllerEvent) error {
	c.logger.Debug("Controller events not implemented", zap.String("event_type", event.Type))
	return nil
}

// handleClusterCreated processes cluster creation events
func (c *Controller) handleClusterCreated(event *sdk.ClusterEvent, cluster *sdk.Cluster) error {
	c.logger.Info("Processing cluster creation", zap.String("cluster_id", event.ClusterID))

	// Create or update all resources
	resources, err := c.getOrCreateAllResources(cluster)
	if err != nil {
		c.logger.Error("Failed to create resources", zap.Error(err))
		return c.reportError(event, "ResourceCreationFailed", err)
	}

	// Report status
	return c.evaluateAndReportStatus(event, cluster, resources)
}

// handleClusterUpdated processes cluster update events
func (c *Controller) handleClusterUpdated(event *sdk.ClusterEvent, cluster *sdk.Cluster) error {
	c.logger.Info("Processing cluster update", zap.String("cluster_id", event.ClusterID))

	// Create or update all resources
	resources, err := c.getOrCreateAllResources(cluster)
	if err != nil {
		c.logger.Error("Failed to update resources", zap.Error(err))
		return c.reportError(event, "ResourceUpdateFailed", err)
	}

	// Report status
	return c.evaluateAndReportStatus(event, cluster, resources)
}

// handleClusterDeleted processes cluster deletion events
// Note: cluster parameter can be nil for deletion events since deleted clusters can't be fetched from API
func (c *Controller) handleClusterDeleted(event *sdk.ClusterEvent, cluster *sdk.Cluster) error {
	c.logger.Info("Processing cluster deletion", zap.String("cluster_id", event.ClusterID))

	// Clean up resources created by this controller for the deleted cluster
	err := c.deleteAllResources(event.ClusterID)
	if err != nil {
		c.logger.Error("Failed to clean up resources during cluster deletion",
			zap.String("cluster_id", event.ClusterID),
			zap.Error(err),
		)
		// Log error but don't fail - cluster is already deleted from cls-backend
		// We don't want to get stuck in retry loop over cleanup failures
	}

	c.logger.Info("Cluster deletion processed successfully", zap.String("cluster_id", event.ClusterID))

	// Don't report status back to cls-backend - the cluster is already deleted
	// Just acknowledge the message by returning nil
	return nil
}

// handleClusterReconcile processes cluster reconciliation events
func (c *Controller) handleClusterReconcile(event *sdk.ClusterEvent, cluster *sdk.Cluster) error {
	c.logger.Info("Processing cluster reconciliation", zap.String("cluster_id", event.ClusterID))

	// Same logic as update - check current state and ensure resources are correct
	resources, err := c.getOrCreateAllResources(cluster)
	if err != nil {
		c.logger.Error("Failed to reconcile resources", zap.Error(err))
		return c.reportError(event, "ResourceReconciliationFailed", err)
	}

	// Report status
	return c.evaluateAndReportStatus(event, cluster, resources)
}

// validateControllerConfig validates a controller configuration
func (c *Controller) validateControllerConfig(config *crd.ControllerConfig) error {
	if config.Spec.Name == "" {
		return fmt.Errorf("controller name is required")
	}

	if len(config.Spec.Resources) == 0 {
		return fmt.Errorf("at least one resource must be defined")
	}

	for i, resource := range config.Spec.Resources {
		if resource.Name == "" {
			return fmt.Errorf("resource[%d]: name is required", i)
		}
		if resource.Template == "" {
			return fmt.Errorf("resource[%d]: template is required", i)
		}
	}

	return nil
}

// updateConfigStatus updates the ControllerConfig status
func (c *Controller) updateConfigStatus(ctx context.Context, config *crd.ControllerConfig, phase string, message string) error {
	config.Status.Phase = phase
	config.Status.Message = message
	config.Status.ObservedGeneration = config.Generation

	return c.k8sClient.Status().Update(ctx, config)
}

// reportError reports an error status
func (c *Controller) reportError(event *sdk.ClusterEvent, reason string, err error) error {
	update := sdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)

	update.AddCondition(sdk.NewCondition(
		"Applied",
		"False",
		reason,
		err.Error(),
	))

	errorInfo := sdk.NewErrorInfo(
		sdk.ErrorTypeSystem,
		"",
		err.Error(),
		false,
	)
	update.SetError(errorInfo)

	return c.sdkClient.ReportStatus(update)
}


// GetScheme returns the runtime scheme with our CRDs registered
func GetScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	// Add the default Kubernetes types first
	_ = clientgoscheme.AddToScheme(scheme)
	// Add our CRD types
	_ = crd.AddToScheme(scheme)
	crd.EnsureTypesRegistered(scheme) // Explicitly register types
	return scheme
}

