package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/apahim/cls-controller/internal/config"
	"github.com/apahim/cls-controller/internal/crd"
	"github.com/apahim/cls-controller/internal/template"
	"github.com/apahim/cls-controller/internal/client"
	controllersdk "github.com/apahim/controller-sdk"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Controller represents the generalized CLS controller
type Controller struct {
	config       *config.Config
	k8sClient    ctrlclient.Client
	logger       *zap.Logger

	// SDK client for event handling and status reporting
	sdkClient    *controllersdk.Client

	// Template engine for rendering resources
	templateEngine *template.Engine

	// Client manager for different target types
	clientManager  *client.Manager

	// Current controller configuration
	controllerConfig *crd.ControllerConfig
}

// New creates a new generalized controller
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
func (c *Controller) SetSDKClient(sdkClient *controllersdk.Client) {
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

// HandleClusterEvent handles cluster lifecycle events (implements controllersdk.EventHandler)
func (c *Controller) HandleClusterEvent(event *controllersdk.ClusterEvent) error {
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

	// Fetch cluster spec from API using organization from event
	ctx := context.Background()

	// Extract organization ID from event and convert to domain for API calls
	organizationID := event.OrganizationID
	if organizationID == "" {
		log.Error("No organization ID in cluster event")
		return c.reportError(event, "MissingOrganization", fmt.Errorf("cluster event missing organization_id field"))
	}

	// Convert organization ID (e.g., "redhat-com") to domain (e.g., "redhat.com") for API calls
	organizationDomain := controllersdk.OrganizationIDToDomain(organizationID)

	// Use multi-tenant API client to fetch cluster with correct organization
	mtClient := c.sdkClient.GetMultiTenantAPIClient()
	if mtClient == nil {
		log.Error("Multi-tenant API client not available")
		return c.reportError(event, "APIClientUnavailable", fmt.Errorf("multi-tenant API client not available"))
	}

	cluster, err := mtClient.GetClusterWithOrg(ctx, organizationDomain, event.ClusterID)
	if err != nil {
		log.Error("Failed to fetch cluster from API",
			zap.String("organization_domain", organizationDomain),
			zap.Error(err))
		return c.reportErrorWithOrg(event, organizationDomain, "ClusterFetchFailed", err)
	}

	log.Debug("Fetched cluster spec",
		zap.String("cluster_name", cluster.Name),
		zap.Int64("cluster_generation", cluster.Generation),
	)

	// Check preconditions
	if !c.evaluatePreconditions(cluster) {
		log.Info("Preconditions not met, skipping resource creation")
		return c.reportPreconditionFailureWithOrg(event, organizationDomain, cluster)
	}

	// Process the event based on type
	switch event.Type {
	case controllersdk.EventTypeClusterCreated:
		return c.handleClusterCreatedWithOrg(event, organizationDomain, cluster)
	case controllersdk.EventTypeClusterUpdated:
		return c.handleClusterUpdatedWithOrg(event, organizationDomain, cluster)
	case controllersdk.EventTypeClusterDeleted:
		return c.handleClusterDeletedWithOrg(event, organizationDomain, cluster)
	case controllersdk.EventTypeClusterReconcile:
		return c.handleClusterReconcileWithOrg(event, organizationDomain, cluster)
	default:
		log.Warn("Unknown event type", zap.String("event_type", event.Type))
		return nil
	}
}

// HandleNodePoolEvent handles nodepool events (not implemented yet)
func (c *Controller) HandleNodePoolEvent(event *controllersdk.NodePoolEvent) error {
	c.logger.Debug("NodePool events not implemented", zap.String("event_type", event.Type))
	return nil
}

// HandleControllerEvent handles controller events (not implemented yet)
func (c *Controller) HandleControllerEvent(event *controllersdk.ControllerEvent) error {
	c.logger.Debug("Controller events not implemented", zap.String("event_type", event.Type))
	return nil
}

// handleClusterCreated processes cluster creation events
func (c *Controller) handleClusterCreated(event *controllersdk.ClusterEvent, cluster *controllersdk.Cluster) error {
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
func (c *Controller) handleClusterUpdated(event *controllersdk.ClusterEvent, cluster *controllersdk.Cluster) error {
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
func (c *Controller) handleClusterDeleted(event *controllersdk.ClusterEvent, cluster *controllersdk.Cluster) error {
	c.logger.Info("Processing cluster deletion", zap.String("cluster_id", event.ClusterID))

	// For now, just report that deletion was processed
	// TODO: Implement resource cleanup if needed

	update := controllersdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)
	update.AddCondition(controllersdk.NewCondition(
		"Applied",
		"False",
		"ClusterDeleted",
		"Cluster deletion processed",
	))

	return c.sdkClient.ReportStatus(update)
}

// handleClusterReconcile processes cluster reconciliation events
func (c *Controller) handleClusterReconcile(event *controllersdk.ClusterEvent, cluster *controllersdk.Cluster) error {
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
func (c *Controller) reportError(event *controllersdk.ClusterEvent, reason string, err error) error {
	update := controllersdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)

	update.AddCondition(controllersdk.NewCondition(
		"Applied",
		"False",
		reason,
		err.Error(),
	))

	errorInfo := controllersdk.NewErrorInfo(
		controllersdk.ErrorTypeSystem,
		"",
		err.Error(),
		false,
	)
	update.SetError(errorInfo)

	return c.sdkClient.ReportStatus(update)
}

// reportErrorWithOrg reports an error status using organization-aware API
func (c *Controller) reportErrorWithOrg(event *controllersdk.ClusterEvent, organizationDomain string, reason string, err error) error {
	update := controllersdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)

	update.AddCondition(controllersdk.NewCondition(
		"Applied",
		"False",
		reason,
		err.Error(),
	))

	errorInfo := controllersdk.NewErrorInfo(
		controllersdk.ErrorTypeSystem,
		"",
		err.Error(),
		false,
	)
	update.SetError(errorInfo)

	// Use organization-aware status reporting
	mtClient := c.sdkClient.GetMultiTenantAPIClient()
	if mtClient != nil {
		ctx := context.Background()
		return mtClient.ReportClusterStatusWithOrg(ctx, organizationDomain, update)
	}

	// Fallback to legacy reporting
	return c.sdkClient.ReportStatus(update)
}

// Organization-aware event handlers

// handleClusterCreatedWithOrg processes cluster creation events with organization context
func (c *Controller) handleClusterCreatedWithOrg(event *controllersdk.ClusterEvent, organizationDomain string, cluster *controllersdk.Cluster) error {
	c.logger.Info("Processing cluster creation",
		zap.String("cluster_id", event.ClusterID),
		zap.String("organization_domain", organizationDomain))

	// Create or update all resources
	resources, err := c.getOrCreateAllResources(cluster)
	if err != nil {
		c.logger.Error("Failed to create resources", zap.Error(err))
		return c.reportErrorWithOrg(event, organizationDomain, "ResourceCreationFailed", err)
	}

	// Report status
	return c.evaluateAndReportStatusWithOrg(event, organizationDomain, cluster, resources)
}

// handleClusterUpdatedWithOrg processes cluster update events with organization context
func (c *Controller) handleClusterUpdatedWithOrg(event *controllersdk.ClusterEvent, organizationDomain string, cluster *controllersdk.Cluster) error {
	c.logger.Info("Processing cluster update",
		zap.String("cluster_id", event.ClusterID),
		zap.String("organization_domain", organizationDomain))

	// Create or update all resources
	resources, err := c.getOrCreateAllResources(cluster)
	if err != nil {
		c.logger.Error("Failed to update resources", zap.Error(err))
		return c.reportErrorWithOrg(event, organizationDomain, "ResourceUpdateFailed", err)
	}

	// Report status
	return c.evaluateAndReportStatusWithOrg(event, organizationDomain, cluster, resources)
}

// handleClusterDeletedWithOrg processes cluster deletion events with organization context
func (c *Controller) handleClusterDeletedWithOrg(event *controllersdk.ClusterEvent, organizationDomain string, cluster *controllersdk.Cluster) error {
	c.logger.Info("Processing cluster deletion",
		zap.String("cluster_id", event.ClusterID),
		zap.String("organization_domain", organizationDomain))

	// For now, just report that deletion was processed
	// TODO: Implement resource cleanup if needed

	update := controllersdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)
	update.AddCondition(controllersdk.NewCondition(
		"Applied",
		"False",
		"ClusterDeleted",
		"Cluster deletion processed",
	))

	// Use organization-aware status reporting
	mtClient := c.sdkClient.GetMultiTenantAPIClient()
	if mtClient != nil {
		ctx := context.Background()
		return mtClient.ReportClusterStatusWithOrg(ctx, organizationDomain, update)
	}

	// Fallback to legacy reporting
	return c.sdkClient.ReportStatus(update)
}

// handleClusterReconcileWithOrg processes cluster reconciliation events with organization context
func (c *Controller) handleClusterReconcileWithOrg(event *controllersdk.ClusterEvent, organizationDomain string, cluster *controllersdk.Cluster) error {
	c.logger.Info("Processing cluster reconciliation",
		zap.String("cluster_id", event.ClusterID),
		zap.String("organization_domain", organizationDomain))

	// Same logic as update - check current state and ensure resources are correct
	resources, err := c.getOrCreateAllResources(cluster)
	if err != nil {
		c.logger.Error("Failed to reconcile resources", zap.Error(err))
		return c.reportErrorWithOrg(event, organizationDomain, "ResourceReconciliationFailed", err)
	}

	// Report status
	return c.evaluateAndReportStatusWithOrg(event, organizationDomain, cluster, resources)
}

// reportPreconditionFailureWithOrg reports precondition failure with organization context
func (c *Controller) reportPreconditionFailureWithOrg(event *controllersdk.ClusterEvent, organizationDomain string, cluster *controllersdk.Cluster) error {
	update := controllersdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)
	update.AddCondition(controllersdk.NewCondition(
		"Applied",
		"False",
		"PreconditionsNotMet",
		"Controller preconditions not satisfied for this cluster",
	))

	// Use organization-aware status reporting
	mtClient := c.sdkClient.GetMultiTenantAPIClient()
	if mtClient != nil {
		ctx := context.Background()
		return mtClient.ReportClusterStatusWithOrg(ctx, organizationDomain, update)
	}

	// Fallback to legacy reporting
	return c.sdkClient.ReportStatus(update)
}

// evaluateAndReportStatusWithOrg evaluates status conditions and reports them with organization context
func (c *Controller) evaluateAndReportStatusWithOrg(event *controllersdk.ClusterEvent, organizationDomain string, cluster *controllersdk.Cluster, resources map[string]*unstructured.Unstructured) error {
	c.logger.Info("Starting status evaluation and reporting with organization context",
		zap.String("cluster_id", event.ClusterID),
		zap.String("organization_domain", organizationDomain),
		zap.Int("resource_count", len(resources)),
	)

	if c.controllerConfig == nil {
		return fmt.Errorf("no controller configuration loaded")
	}

	ctx := context.Background()

	// Get the appropriate client for refreshing resource status
	resourceClient, err := c.clientManager.GetClient(ctx, c.controllerConfig.Spec.Target, cluster)
	if err != nil {
		c.logger.Warn("Failed to get resource client for status refresh", zap.Error(err))
		// Continue with cached status if client fails
	}

	// Refresh resources with current status from Kubernetes
	refreshedResources := make(map[string]*unstructured.Unstructured)
	for name, resource := range resources {
		if resource != nil && resourceClient != nil {
			// Try to refresh the resource to get current status
			refreshed := &unstructured.Unstructured{}
			refreshed.SetGroupVersionKind(resource.GroupVersionKind())

			if err := resourceClient.Get(ctx, resource.GetName(), resource.GetNamespace(), refreshed); err == nil {
				refreshedResources[name] = refreshed
				c.logger.Info("Refreshed resource status",
					zap.String("resource_name", name),
					zap.String("resource_kind", resource.GetKind()),
					zap.String("resource_full_name", resource.GetName()),
				)

				// Log job status specifically for debugging
				if resource.GetKind() == "Job" {
					if status, exists, _ := unstructured.NestedMap(refreshed.Object, "status"); exists {
						c.logger.Info("Job status details",
							zap.String("job_name", resource.GetName()),
							zap.Any("status", status),
						)
					}
				}
			} else {
				// Use original resource if refresh fails
				refreshedResources[name] = resource
				c.logger.Warn("Failed to refresh resource status, using cached",
					zap.String("resource_name", name),
					zap.String("resource_full_name", resource.GetName()),
					zap.Error(err),
				)
			}
		} else {
			refreshedResources[name] = resource
		}
	}

	update := controllersdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)

	// Add default Applied condition
	update.AddCondition(controllersdk.NewCondition(
		"Applied",
		"True",
		"ResourcesCreated",
		fmt.Sprintf("Created %d resources successfully", len(resources)),
	))

	// Evaluate custom status conditions from ControllerConfig using refreshed resources
	for _, conditionConfig := range c.controllerConfig.Spec.StatusConditions {
		status, reason, message, err := c.templateEngine.RenderStatusCondition(conditionConfig.Name, cluster, refreshedResources)
		if err != nil {
			c.logger.Error("Failed to render status condition",
				zap.String("condition", conditionConfig.Name),
				zap.Error(err),
			)
			continue
		}

		c.logger.Info("Evaluated status condition",
			zap.String("condition", conditionConfig.Name),
			zap.String("status", status),
			zap.String("reason", reason),
			zap.String("message", message),
		)

		update.AddCondition(controllersdk.NewCondition(
			conditionConfig.Name,
			status,
			reason,
			message,
		))
	}

	// Add resource status metadata using refreshed resources
	resourceStatuses := make(map[string]interface{})
	for name, resource := range refreshedResources {
		if resource != nil {
			resourceStatus := map[string]interface{}{
				"status": "Created",
			}

			// Add current resource status from Kubernetes
			if status, exists, _ := unstructured.NestedMap(resource.Object, "status"); exists {
				resourceStatus["resource_status"] = status
			}

			resourceStatuses[name] = resourceStatus
		}
	}

	update.SetMetadata("resources", resourceStatuses)

	c.logger.Info("Publishing status update to cls-backend with organization context",
		zap.String("cluster_id", event.ClusterID),
		zap.String("organization_domain", organizationDomain),
		zap.Int("condition_count", len(update.Conditions)),
	)

	// Use organization-aware status reporting
	mtClient := c.sdkClient.GetMultiTenantAPIClient()
	if mtClient != nil {
		err = mtClient.ReportClusterStatusWithOrg(ctx, organizationDomain, update)
		if err != nil {
			c.logger.Error("Failed to publish status update with organization context", zap.Error(err))
			return err
		}

		c.logger.Info("Status update published successfully with organization context",
			zap.String("cluster_id", event.ClusterID),
			zap.String("organization_domain", organizationDomain),
		)
		return nil
	}

	// Fallback to legacy reporting
	err = c.sdkClient.ReportStatus(update)
	if err != nil {
		c.logger.Error("Failed to publish status update (legacy)", zap.Error(err))
		return err
	}

	c.logger.Info("Status update published successfully (legacy)",
		zap.String("cluster_id", event.ClusterID),
	)
	return nil
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