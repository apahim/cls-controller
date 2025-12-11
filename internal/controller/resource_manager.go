package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/apahim/cls-controller/internal/client"
	"github.com/apahim/cls-controller/internal/crd"
	"github.com/apahim/cls-controller/internal/sdk"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// evaluatePreconditions checks if cluster meets all preconditions
func (c *Controller) evaluatePreconditions(cluster *sdk.Cluster) bool {
	if c.controllerConfig == nil || len(c.controllerConfig.Spec.Preconditions) == 0 {
		return true // No preconditions means always pass
	}

	// Parse cluster spec for field evaluation
	var clusterData map[string]interface{}
	clusterData = map[string]interface{}{
		"id":         cluster.ID,
		"name":       cluster.Name,
		"generation": cluster.Generation,
	}

	// Add parsed spec
	var spec map[string]interface{}
	if err := json.Unmarshal(cluster.Spec, &spec); err == nil {
		clusterData["spec"] = spec
	}

	// TODO: Add cluster status when available in cluster object
	// For now, we don't have status in the cluster object from the API

	// All preconditions must be true
	for _, precondition := range c.controllerConfig.Spec.Preconditions {
		if !c.evaluatePreconditionRule(clusterData, precondition) {
			c.logger.Debug("Precondition failed",
				zap.String("field", precondition.Field),
				zap.String("operator", precondition.Operator),
				zap.String("cluster_id", cluster.ID),
			)
			return false
		}
	}

	return true
}

// evaluatePreconditionRule evaluates a single precondition rule
func (c *Controller) evaluatePreconditionRule(data map[string]interface{}, rule crd.PreconditionRule) bool {
	// Extract field value
	value, exists := c.extractFieldValue(data, rule.Field)

	switch rule.Operator {
	case crd.OperatorExists:
		return exists
	case crd.OperatorNotExists:
		return !exists
	case crd.OperatorEqual:
		if !exists {
			return false
		}
		expectedValue := c.extractRawExtensionValue(rule.Value)
		return c.compareValues(value, expectedValue)
	case crd.OperatorNotEqual:
		if !exists {
			return true
		}
		expectedValue := c.extractRawExtensionValue(rule.Value)
		return !c.compareValues(value, expectedValue)
	case crd.OperatorIn:
		if !exists {
			return false
		}
		expectedArray := c.extractRawExtensionArray(rule.Value)
		return c.valueInArray(value, expectedArray)
	case crd.OperatorNotIn:
		if !exists {
			return true
		}
		expectedArray := c.extractRawExtensionArray(rule.Value)
		return !c.valueInArray(value, expectedArray)
	default:
		c.logger.Warn("Unknown precondition operator", zap.String("operator", rule.Operator))
		return false
	}
}

// extractFieldValue extracts a field value from nested data using dot notation
func (c *Controller) extractFieldValue(data map[string]interface{}, fieldPath string) (interface{}, bool) {
	parts := strings.Split(fieldPath, ".")
	current := data

	for _, part := range parts {
		if current == nil {
			return nil, false
		}

		value, exists := current[part]
		if !exists {
			return nil, false
		}

		// If this is the last part, return the value
		if part == parts[len(parts)-1] {
			return value, true
		}

		// Otherwise, continue traversing
		if nextMap, ok := value.(map[string]interface{}); ok {
			current = nextMap
		} else {
			return nil, false
		}
	}

	return nil, false
}

// extractRawExtensionValue extracts a value from runtime.RawExtension
func (c *Controller) extractRawExtensionValue(raw runtime.RawExtension) interface{} {
	if len(raw.Raw) == 0 {
		return nil
	}

	var value interface{}
	if err := json.Unmarshal(raw.Raw, &value); err != nil {
		return string(raw.Raw) // Fallback to string
	}
	return value
}

// extractRawExtensionArray extracts an array from runtime.RawExtension
func (c *Controller) extractRawExtensionArray(raw runtime.RawExtension) []interface{} {
	value := c.extractRawExtensionValue(raw)
	if array, ok := value.([]interface{}); ok {
		return array
	}
	// If not an array, treat as single element array
	return []interface{}{value}
}

// compareValues compares two values for equality
func (c *Controller) compareValues(a, b interface{}) bool {
	return reflect.DeepEqual(a, b)
}

// valueInArray checks if a value exists in an array
func (c *Controller) valueInArray(value interface{}, array []interface{}) bool {
	for _, item := range array {
		if c.compareValues(value, item) {
			return true
		}
	}
	return false
}

// reportPreconditionFailure reports when preconditions are not met
func (c *Controller) reportPreconditionFailure(event *sdk.ClusterEvent, cluster *sdk.Cluster) error {
	failedPreconditions := c.getFailedPreconditions(cluster)

	update := sdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)

	update.AddCondition(sdk.NewCondition(
		"Applied",
		"False",
		"PreconditionsNotMet",
		fmt.Sprintf("Preconditions not met: %s", strings.Join(failedPreconditions, ", ")),
	))

	return c.sdkClient.ReportStatus(update)
}

// getFailedPreconditions returns a list of failed precondition descriptions
func (c *Controller) getFailedPreconditions(cluster *sdk.Cluster) []string {
	var failed []string

	if c.controllerConfig == nil {
		return failed
	}

	// Parse cluster data
	var clusterData map[string]interface{}
	clusterData = map[string]interface{}{
		"id":         cluster.ID,
		"name":       cluster.Name,
		"generation": cluster.Generation,
	}

	var spec map[string]interface{}
	if err := json.Unmarshal(cluster.Spec, &spec); err == nil {
		clusterData["spec"] = spec
	}

	for _, precondition := range c.controllerConfig.Spec.Preconditions {
		if !c.evaluatePreconditionRule(clusterData, precondition) {
			description := c.describePreconditionFailure(clusterData, precondition)
			failed = append(failed, description)
		}
	}

	return failed
}

// describePreconditionFailure creates a human-readable description of why a precondition failed
func (c *Controller) describePreconditionFailure(data map[string]interface{}, rule crd.PreconditionRule) string {
	value, exists := c.extractFieldValue(data, rule.Field)
	expectedValue := c.extractRawExtensionValue(rule.Value)

	switch rule.Operator {
	case crd.OperatorExists:
		return fmt.Sprintf("%s must exist", rule.Field)
	case crd.OperatorNotExists:
		return fmt.Sprintf("%s must not exist", rule.Field)
	case crd.OperatorEqual:
		if !exists {
			return fmt.Sprintf("%s must equal '%v' (field does not exist)", rule.Field, expectedValue)
		}
		return fmt.Sprintf("%s must equal '%v' (got '%v')", rule.Field, expectedValue, value)
	case crd.OperatorNotEqual:
		return fmt.Sprintf("%s must not equal '%v'", rule.Field, expectedValue)
	case crd.OperatorIn:
		expectedArray := c.extractRawExtensionArray(rule.Value)
		if !exists {
			return fmt.Sprintf("%s must be one of %v (field does not exist)", rule.Field, expectedArray)
		}
		return fmt.Sprintf("%s must be one of %v (got '%v')", rule.Field, expectedArray, value)
	case crd.OperatorNotIn:
		expectedArray := c.extractRawExtensionArray(rule.Value)
		return fmt.Sprintf("%s must not be one of %v", rule.Field, expectedArray)
	default:
		return fmt.Sprintf("%s failed unknown operator %s", rule.Field, rule.Operator)
	}
}

// getOrCreateAllResources creates or updates all resources defined in the controller configuration
func (c *Controller) getOrCreateAllResources(cluster *sdk.Cluster) (map[string]*unstructured.Unstructured, error) {
	if c.controllerConfig == nil {
		return nil, fmt.Errorf("no controller configuration loaded")
	}

	// Create context with timeout for resource operations
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second) // 2 minutes for all operations
	defer cancel()
	resources := make(map[string]*unstructured.Unstructured)

	// Get the appropriate client for this target
	c.logger.Debug("Getting client for target cluster",
		zap.String("cluster_id", cluster.ID),
		zap.Duration("timeout", 120*time.Second),
	)
	resourceClient, err := c.clientManager.GetClient(ctx, c.controllerConfig.Spec.Target, cluster)
	if err != nil {
		c.logger.Error("Failed to get resource client",
			zap.String("cluster_id", cluster.ID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("failed to get resource client: %w", err)
	}
	c.logger.Debug("Successfully obtained resource client",
		zap.String("cluster_id", cluster.ID),
	)

	// Process each resource
	for _, resourceConfig := range c.controllerConfig.Spec.Resources {
		c.logger.Debug("Processing resource",
			zap.String("resource_name", resourceConfig.Name),
			zap.String("cluster_id", cluster.ID),
		)

		resource, err := c.getOrCreateResource(ctx, resourceClient, resourceConfig, cluster, resources)
		if err != nil {
			c.logger.Error("Failed to process resource",
				zap.String("resource_name", resourceConfig.Name),
				zap.String("cluster_id", cluster.ID),
				zap.Error(err),
				zap.String("note", "Check network connectivity to remote cluster if timeout"),
			)
			return nil, fmt.Errorf("failed to create resource %s: %w", resourceConfig.Name, err)
		}
		resources[resourceConfig.Name] = resource

		c.logger.Info("Resource processed successfully",
			zap.String("resource_name", resourceConfig.Name),
			zap.String("kind", resource.GetKind()),
			zap.String("name", resource.GetName()),
			zap.String("cluster_id", cluster.ID),
		)
	}

	return resources, nil
}

// getOrCreateResource creates or updates a single resource
func (c *Controller) getOrCreateResource(ctx context.Context, resourceClient client.ResourceClient, resourceConfig crd.ResourceConfig, cluster *sdk.Cluster, existingResources map[string]*unstructured.Unstructured) (*unstructured.Unstructured, error) {
	// Render the resource template
	resource, err := c.templateEngine.RenderResource(resourceConfig.Name, cluster, existingResources)
	if err != nil {
		return nil, fmt.Errorf("failed to render resource template: %w", err)
	}

	// Determine update strategy
	strategy := c.determineUpdateStrategy(resourceConfig, resource.GetKind())

	if strategy == crd.UpdateStrategyVersioned {
		return c.getOrCreateVersionedResource(ctx, resourceClient, resourceConfig, resource, cluster)
	} else {
		return c.getOrUpdateInPlaceResource(ctx, resourceClient, resource, cluster)
	}
}

// determineUpdateStrategy determines the appropriate update strategy for a resource
func (c *Controller) determineUpdateStrategy(resourceConfig crd.ResourceConfig, resourceKind string) string {
	// Force versioned strategy for immutable resources
	if c.isImmutableResourceKind(resourceKind) {
		return crd.UpdateStrategyVersioned
	}

	// Use per-resource configured strategy, defaulting to in_place for mutable resources
	if resourceConfig.ResourceManagement != nil && resourceConfig.ResourceManagement.UpdateStrategy != "" {
		return resourceConfig.ResourceManagement.UpdateStrategy
	}

	return crd.UpdateStrategyInPlace // Default for mutable resources
}

// isImmutableResourceKind checks if a resource kind is immutable
func (c *Controller) isImmutableResourceKind(kind string) bool {
	immutableKinds := []string{"Job", "Pod"}
	for _, immutable := range immutableKinds {
		if kind == immutable {
			return true
		}
	}
	return false
}

// getOrUpdateInPlaceResource handles in-place resource updates
func (c *Controller) getOrUpdateInPlaceResource(ctx context.Context, resourceClient client.ResourceClient, resource *unstructured.Unstructured, cluster *sdk.Cluster) (*unstructured.Unstructured, error) {
	// Add standard labels to resource before any operations
	// This ensures labels are present for both create and update paths
	c.addStandardLabels(resource, cluster)

	// Try to get existing resource
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(resource.GroupVersionKind())

	err := resourceClient.Get(ctx, resource.GetName(), resource.GetNamespace(), existing)
	if err != nil {
		// Resource doesn't exist - create it
		if err := resourceClient.Create(ctx, resource); err != nil {
			return nil, fmt.Errorf("failed to create resource: %w", err)
		}
		return resource, nil
	}

	// Resource exists - update it if needed
	if c.needsUpdate(existing, resource) {
		// Preserve resource version for update
		resource.SetResourceVersion(existing.GetResourceVersion())
		if err := resourceClient.Update(ctx, resource); err != nil {
			return nil, fmt.Errorf("failed to update resource: %w", err)
		}
		// Return the updated resource
		return resource, nil
	}

	// No update needed - return existing resource with current status
	return existing, nil
}

// getOrCreateVersionedResource handles versioned resource creation following DESIGN.md pattern with time-based intervals
func (c *Controller) getOrCreateVersionedResource(ctx context.Context, resourceClient client.ResourceClient, resourceConfig crd.ResourceConfig, resource *unstructured.Unstructured, cluster *sdk.Cluster) (*unstructured.Unstructured, error) {
	// 1. Cleanup old generation resources first
	if err := c.cleanupOldGenerations(ctx, resourceClient, cluster.ID, cluster.Generation, resource.GetKind(), resource.GetNamespace()); err != nil {
		c.logger.Warn("Failed to cleanup old generations", zap.Error(err))
		// Don't fail - continue with resource creation
	}

	// 2. Check for existing resource in current generation
	currentGenResource, err := c.findResourceForGeneration(ctx, resourceClient, cluster.ID, cluster.Generation, resource.GetKind(), resource.GetNamespace())
	if err != nil {
		c.logger.Debug("No existing resource found for current generation", zap.Error(err))
	}

	if currentGenResource != nil {
		if c.isResourceRunning(currentGenResource) {
			// Resource is running - report its status (don't create new one)
			c.logger.Debug("Found running resource for current generation",
				zap.String("resource_name", currentGenResource.GetName()),
				zap.String("cluster_id", cluster.ID),
				zap.Int64("generation", cluster.Generation),
			)
			return currentGenResource, nil
		} else {
			// Resource completed/failed - check if we should recreate based on version interval
			shouldRecreate, err := c.shouldRecreateVersionedResource(currentGenResource, resourceConfig)
			if err != nil {
				c.logger.Warn("Failed to check version interval, recreating anyway", zap.Error(err))
				shouldRecreate = true
			}

			if !shouldRecreate {
				c.logger.Info("Version interval not elapsed, keeping existing completed resource",
					zap.String("resource_name", currentGenResource.GetName()),
					zap.String("cluster_id", cluster.ID),
				)
				return currentGenResource, nil
			}

			// Version interval elapsed - delete and recreate for continuous enforcement
			c.logger.Info("Version interval elapsed, deleting completed resource for continuous enforcement",
				zap.String("resource_name", currentGenResource.GetName()),
				zap.String("cluster_id", cluster.ID),
			)
			if err := resourceClient.Delete(ctx, currentGenResource); err != nil {
				c.logger.Warn("Failed to delete completed resource", zap.Error(err))
				// Continue anyway - might be already deleted
			}
		}
	}

	// 3. Create new resource with incremental naming
	c.logger.Info("Creating new versioned resource",
		zap.String("resource_name", resource.GetName()),
		zap.String("cluster_id", cluster.ID),
		zap.Int64("generation", cluster.Generation),
	)

	// Add standard labels for cleanup tracking
	c.addStandardLabels(resource, cluster)

	if err := resourceClient.Create(ctx, resource); err != nil {
		return nil, fmt.Errorf("failed to create versioned resource: %w", err)
	}

	// 4. Cleanup old completed versions if keepVersions is configured
	if err := c.cleanupCompletedVersions(ctx, resourceClient, resourceConfig, cluster.ID, cluster.Generation, resource.GetKind(), resource.GetNamespace()); err != nil {
		c.logger.Warn("Failed to cleanup completed versions", zap.Error(err))
		// Don't fail - cleanup is best effort
	}

	return resource, nil
}

// needsUpdate compares two resources to determine if update is needed
func (c *Controller) needsUpdate(existing, desired *unstructured.Unstructured) bool {
	// Simple comparison - compare specs
	existingSpec, existingHasSpec, _ := unstructured.NestedMap(existing.Object, "spec")
	desiredSpec, desiredHasSpec, _ := unstructured.NestedMap(desired.Object, "spec")

	if existingHasSpec != desiredHasSpec {
		return true
	}

	if existingHasSpec && !reflect.DeepEqual(existingSpec, desiredSpec) {
		return true
	}

	// Also compare labels and annotations
	if !reflect.DeepEqual(existing.GetLabels(), desired.GetLabels()) {
		return true
	}

	if !reflect.DeepEqual(existing.GetAnnotations(), desired.GetAnnotations()) {
		return true
	}

	return false
}

// evaluateAndReportStatus evaluates resource status and reports to cls-backend
func (c *Controller) evaluateAndReportStatus(event *sdk.ClusterEvent, cluster *sdk.Cluster, resources map[string]*unstructured.Unstructured) error {
	c.logger.Info("Starting status evaluation and reporting",
		zap.String("cluster_id", event.ClusterID),
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

	update := sdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)

	// Add default Applied condition
	update.AddCondition(sdk.NewCondition(
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

		update.AddCondition(sdk.NewCondition(
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

	c.logger.Info("Publishing status update to cls-backend",
		zap.String("cluster_id", event.ClusterID),
		zap.Int("condition_count", len(update.Conditions)),
	)

	err = c.sdkClient.ReportStatus(update)
	if err != nil {
		c.logger.Error("Failed to publish status update", zap.Error(err))
		return err
	}

	c.logger.Info("Status update published successfully",
		zap.String("cluster_id", event.ClusterID),
	)

	return nil
}

// findResourceForGeneration finds existing resources for a specific cluster+generation
func (c *Controller) findResourceForGeneration(ctx context.Context, resourceClient client.ResourceClient, clusterID string, generation int64, kind, namespace string) (*unstructured.Unstructured, error) {
	// List resources with cluster-id and cluster-generation labels
	labels := map[string]string{
		"cluster-id":         clusterID,
		"cluster-generation": fmt.Sprintf("%d", generation),
	}

	c.logger.Info("Looking for existing resource",
		zap.String("cluster_id", clusterID),
		zap.Int64("generation", generation),
		zap.String("kind", kind),
		zap.String("namespace", namespace),
		zap.Any("labels", labels),
	)

	resourceList := &unstructured.UnstructuredList{}
	// Set the GroupVersionKind for the list based on the resource kind
	switch kind {
	case "Job":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "batch",
			Version: "v1",
			Kind:    "JobList",
		})
	case "Pod":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "PodList",
		})
	default:
		// For unknown kinds, try to infer the list kind
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    kind + "List",
		})
	}

	c.logger.Info("Attempting to list resources with GVK",
		zap.Any("gvk", resourceList.GroupVersionKind()),
	)

	err := resourceClient.List(ctx, namespace, labels, resourceList)
	if err != nil {
		c.logger.Error("Failed to list resources",
			zap.String("cluster_id", clusterID),
			zap.Int64("generation", generation),
			zap.Error(err),
		)
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	c.logger.Info("List operation completed",
		zap.String("cluster_id", clusterID),
		zap.Int64("generation", generation),
		zap.Int("items_found", len(resourceList.Items)),
	)

	if len(resourceList.Items) == 0 {
		c.logger.Info("No resources found for cluster and generation",
			zap.String("cluster_id", clusterID),
			zap.Int64("generation", generation),
		)
		return nil, fmt.Errorf("no resources found for cluster %s generation %d", clusterID, generation)
	}

	// Return the first resource found (should only be one per generation)
	resource := &resourceList.Items[0]

	c.logger.Info("Found existing resource",
		zap.String("cluster_id", clusterID),
		zap.Int64("generation", generation),
		zap.String("resource_name", resource.GetName()),
		zap.String("resource_namespace", resource.GetNamespace()),
	)

	// Refresh the resource to get current status
	refreshed := &unstructured.Unstructured{}
	refreshed.SetGroupVersionKind(resource.GroupVersionKind())

	if err := resourceClient.Get(ctx, resource.GetName(), resource.GetNamespace(), refreshed); err == nil {
		c.logger.Info("Successfully refreshed existing resource",
			zap.String("resource_name", resource.GetName()),
		)
		return refreshed, nil
	}

	c.logger.Warn("Failed to refresh existing resource, returning original",
		zap.String("resource_name", resource.GetName()),
		zap.Error(err),
	)

	// If refresh failed, return the original
	return resource, nil
}

// cleanupOldGenerations removes resources from previous generations
func (c *Controller) cleanupOldGenerations(ctx context.Context, resourceClient client.ResourceClient, clusterID string, currentGeneration int64, kind, namespace string) error {
	// List all resources for this cluster (any generation)
	labels := map[string]string{
		"cluster-id": clusterID,
	}

	resourceList := &unstructured.UnstructuredList{}
	// Set the GroupVersionKind for the list based on the resource kind
	switch kind {
	case "Job":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "batch",
			Version: "v1",
			Kind:    "JobList",
		})
	case "Pod":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "PodList",
		})
	default:
		// For unknown kinds, try to infer the list kind
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    kind + "List",
		})
	}

	err := resourceClient.List(ctx, namespace, labels, resourceList)
	if err != nil {
		return fmt.Errorf("failed to list resources for cleanup: %w", err)
	}

	for _, resource := range resourceList.Items {
		// Get generation from label
		generationLabel := resource.GetLabels()["cluster-generation"]
		if generationLabel == "" {
			continue // Skip resources without generation label
		}

		var resourceGeneration int64
		if _, err := fmt.Sscanf(generationLabel, "%d", &resourceGeneration); err != nil {
			continue // Skip resources with invalid generation
		}

		// Delete resources from previous generations
		if resourceGeneration < currentGeneration {
			c.logger.Info("Cleaning up old generation resource",
				zap.String("resource_name", resource.GetName()),
				zap.String("cluster_id", clusterID),
				zap.Int64("resource_generation", resourceGeneration),
				zap.Int64("current_generation", currentGeneration),
			)

			if err := resourceClient.Delete(ctx, &resource); err != nil {
				c.logger.Warn("Failed to delete old generation resource",
					zap.String("resource_name", resource.GetName()),
					zap.Error(err),
				)
				// Continue cleanup - don't fail on individual deletion errors
			}
		}
	}

	return nil
}

// isResourceRunning checks if a resource is still running (not completed/failed)
func (c *Controller) isResourceRunning(resource *unstructured.Unstructured) bool {
	kind := resource.GetKind()

	switch kind {
	case "Job":
		return c.isJobRunning(resource)
	case "Pod":
		return c.isPodRunning(resource)
	default:
		// For other resources, consider them "running" if they exist
		// This prevents immediate recreation of non-completion-based resources
		return true
	}
}

// isJobRunning checks if a Job is still running
func (c *Controller) isJobRunning(job *unstructured.Unstructured) bool {
	// Check job status conditions
	conditions, found, err := unstructured.NestedSlice(job.Object, "status", "conditions")
	if err != nil || !found {
		// No status yet - consider it running
		return true
	}

	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]interface{})
		if !ok {
			continue
		}

		conditionType, found := conditionMap["type"]
		if !found {
			continue
		}

		conditionStatus, found := conditionMap["status"]
		if !found {
			continue
		}

		// Check for completion or failure
		if conditionType == "Complete" && conditionStatus == "True" {
			return false // Job completed
		}
		if conditionType == "Failed" && conditionStatus == "True" {
			return false // Job failed
		}
	}

	// No completion/failure condition found - still running
	return true
}

// isPodRunning checks if a Pod is still running
func (c *Controller) isPodRunning(pod *unstructured.Unstructured) bool {
	phase, found, err := unstructured.NestedString(pod.Object, "status", "phase")
	if err != nil || !found {
		// No status yet - consider it running
		return true
	}

	// Pod is not running if it's succeeded or failed
	return phase != "Succeeded" && phase != "Failed"
}

// shouldRecreateVersionedResource checks if enough time has elapsed to create a new version
func (c *Controller) shouldRecreateVersionedResource(resource *unstructured.Unstructured, resourceConfig crd.ResourceConfig) (bool, error) {
	// Check if newGenerationOnly mode is enabled
	if resourceConfig.ResourceManagement != nil && resourceConfig.ResourceManagement.NewGenerationOnly {
		c.logger.Debug("NewGenerationOnly mode enabled, skipping time-based version interval check",
			zap.String("resource_name", resource.GetName()),
		)
		// In newGenerationOnly mode, we never recreate based on time - only on generation changes
		// Since this function is only called for completed resources, return false to keep them
		return false, nil
	}

	// Get version interval from resource config
	versionInterval := "5m" // default
	if resourceConfig.ResourceManagement != nil && resourceConfig.ResourceManagement.VersionInterval != "" {
		versionInterval = resourceConfig.ResourceManagement.VersionInterval
	}

	// Parse the interval
	duration, err := time.ParseDuration(versionInterval)
	if err != nil {
		return false, fmt.Errorf("invalid version interval '%s': %w", versionInterval, err)
	}

	// Get resource creation time
	creationTime := resource.GetCreationTimestamp()
	if creationTime.IsZero() {
		c.logger.Debug("Resource has no creation timestamp, recreating",
			zap.String("resource_name", resource.GetName()))
		return true, nil
	}

	// Check if enough time has elapsed
	elapsed := time.Since(creationTime.Time)
	shouldRecreate := elapsed >= duration

	c.logger.Debug("Version interval check",
		zap.String("resource_name", resource.GetName()),
		zap.Duration("version_interval", duration),
		zap.Duration("elapsed", elapsed),
		zap.Bool("should_recreate", shouldRecreate),
	)

	return shouldRecreate, nil
}

// cleanupCompletedVersions removes old completed versions beyond the keepVersions limit
func (c *Controller) cleanupCompletedVersions(ctx context.Context, resourceClient client.ResourceClient, resourceConfig crd.ResourceConfig, clusterID string, currentGeneration int64, kind, namespace string) error {
	// Get keepVersions setting (default to 2 if not specified)
	keepVersions := 2
	if resourceConfig.ResourceManagement != nil && resourceConfig.ResourceManagement.KeepVersions > 0 {
		keepVersions = resourceConfig.ResourceManagement.KeepVersions
	}

	c.logger.Info("Starting cleanup of completed versions",
		zap.String("cluster_id", clusterID),
		zap.Int64("generation", currentGeneration),
		zap.String("kind", kind),
		zap.Int("keep_versions", keepVersions),
	)

	// List all resources for this cluster and generation
	labels := map[string]string{
		"cluster-id":         clusterID,
		"cluster-generation": fmt.Sprintf("%d", currentGeneration),
	}

	resourceList := &unstructured.UnstructuredList{}
	// Set the GroupVersionKind for the list based on the resource kind
	switch kind {
	case "Job":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "batch",
			Version: "v1",
			Kind:    "JobList",
		})
	case "Pod":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "PodList",
		})
	default:
		// For unknown kinds, try to infer the list kind
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    kind + "List",
		})
	}

	err := resourceClient.List(ctx, namespace, labels, resourceList)
	if err != nil {
		return fmt.Errorf("failed to list resources for version cleanup: %w", err)
	}

	if len(resourceList.Items) <= keepVersions {
		c.logger.Debug("No cleanup needed - resource count within keepVersions limit",
			zap.Int("current_count", len(resourceList.Items)),
			zap.Int("keep_versions", keepVersions),
		)
		return nil
	}

	// Filter to only completed resources and sort by creation time (newest first)
	var completedResources []*unstructured.Unstructured
	for i := range resourceList.Items {
		resource := &resourceList.Items[i]
		if !c.isResourceRunning(resource) {
			completedResources = append(completedResources, resource)
		}
	}

	if len(completedResources) <= keepVersions {
		c.logger.Debug("No cleanup needed - completed resource count within keepVersions limit",
			zap.Int("completed_count", len(completedResources)),
			zap.Int("keep_versions", keepVersions),
		)
		return nil
	}

	// Sort by creation time (newest first)
	for i := 0; i < len(completedResources)-1; i++ {
		for j := i + 1; j < len(completedResources); j++ {
			if completedResources[i].GetCreationTimestamp().Time.Before(completedResources[j].GetCreationTimestamp().Time) {
				completedResources[i], completedResources[j] = completedResources[j], completedResources[i]
			}
		}
	}

	// Delete old completed resources beyond keepVersions limit
	resourcesToDelete := completedResources[keepVersions:]
	for _, resource := range resourcesToDelete {
		c.logger.Info("Cleaning up old completed version",
			zap.String("resource_name", resource.GetName()),
			zap.String("cluster_id", clusterID),
			zap.Int64("generation", currentGeneration),
			zap.Time("creation_time", resource.GetCreationTimestamp().Time),
		)

		if err := resourceClient.Delete(ctx, resource); err != nil {
			c.logger.Warn("Failed to delete old completed version",
				zap.String("resource_name", resource.GetName()),
				zap.Error(err),
			)
			// Continue cleanup - don't fail on individual deletion errors
		}
	}

	c.logger.Info("Completed version cleanup finished",
		zap.String("cluster_id", clusterID),
		zap.Int("deleted_count", len(resourcesToDelete)),
		zap.Int("kept_count", keepVersions),
	)

	return nil
}

// addStandardLabels adds tracking labels to resources for cleanup purposes
func (c *Controller) addStandardLabels(resource *unstructured.Unstructured, cluster *sdk.Cluster) {
	labels := resource.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	// Add cluster ID label for resource identification during cleanup
	labels["cls.redhat.com/cluster-id"] = cluster.ID
	labels["cluster-id"] = cluster.ID // Also add unprefixed for versioned resource queries

	// Add generation label for versioned resource tracking
	labels["cluster-generation"] = fmt.Sprintf("%d", cluster.Generation)

	// Add controller name label for safety (only delete resources we created)
	if c.config != nil && c.config.ControllerName != "" {
		labels["cls.redhat.com/controller"] = c.config.ControllerName
	}

	resource.SetLabels(labels)

	c.logger.Debug("Added standard labels to resource",
		zap.String("resource_name", resource.GetName()),
		zap.String("cluster_id", cluster.ID),
		zap.String("controller", c.config.ControllerName),
	)
}

// deleteAllResources deletes all resources created by this controller for a specific cluster
func (c *Controller) deleteAllResources(clusterID string) error {
	if c.controllerConfig == nil {
		c.logger.Warn("No controller configuration loaded, skipping resource cleanup")
		return nil
	}

	c.logger.Info("Starting resource cleanup for deleted cluster", zap.String("cluster_id", clusterID))

	// Create context with timeout for deletion operations
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // 1 minute for cleanup
	defer cancel()

	// Create a minimal cluster object for client resolution (we only need the ID)
	minimalCluster := &sdk.Cluster{ID: clusterID}

	// Get the appropriate client for this target
	resourceClient, err := c.clientManager.GetClient(ctx, c.controllerConfig.Spec.Target, minimalCluster)
	if err != nil {
		c.logger.Error("Failed to get resource client for cleanup",
			zap.String("cluster_id", clusterID),
			zap.Error(err),
		)
		return fmt.Errorf("failed to get resource client: %w", err)
	}

	var deletionErrors []error

	// Process each resource for deletion using label-based discovery
	for _, resourceConfig := range c.controllerConfig.Spec.Resources {
		c.logger.Debug("Attempting to delete resources by labels",
			zap.String("resource_name", resourceConfig.Name),
			zap.String("cluster_id", clusterID),
		)

		// Extract GroupVersionKind from template (full YAML parsing)
		gvk, err := c.extractGroupVersionKind(resourceConfig.Template)
		if err != nil {
			deletionErrors = append(deletionErrors, err)
			c.logger.Warn("Failed to extract GroupVersionKind for cleanup",
				zap.String("resource_name", resourceConfig.Name),
				zap.String("cluster_id", clusterID),
				zap.Error(err),
			)
			continue
		}

		// Delete resources using CORRECT GroupVersionKind from template
		deletedCount, err := c.deleteResourcesByLabels(ctx, resourceClient, gvk, clusterID)
		if err != nil {
			deletionErrors = append(deletionErrors, err)
			c.logger.Warn("Failed to delete resources during cleanup",
				zap.String("resource_name", resourceConfig.Name),
				zap.String("group", gvk.Group),
				zap.String("version", gvk.Version),
				zap.String("kind", gvk.Kind),
				zap.String("cluster_id", clusterID),
				zap.Error(err),
			)
			// Continue with other resources even if one fails
		} else {
			c.logger.Info("Successfully deleted resources by labels",
				zap.String("resource_name", resourceConfig.Name),
				zap.String("group", gvk.Group),
				zap.String("version", gvk.Version),
				zap.String("kind", gvk.Kind),
				zap.String("cluster_id", clusterID),
				zap.Int("deleted_count", deletedCount),
			)
		}
	}

	if len(deletionErrors) > 0 {
		c.logger.Warn("Some resource deletions failed during cleanup",
			zap.String("cluster_id", clusterID),
			zap.Int("failure_count", len(deletionErrors)),
		)
		// Return error summary but this won't cause Pub/Sub retry
		return fmt.Errorf("failed to delete %d resources during cleanup", len(deletionErrors))
	}

	c.logger.Info("Resource cleanup completed successfully", zap.String("cluster_id", clusterID))
	return nil
}

// extractGroupVersionKind extracts the full GroupVersionKind from a YAML template string
func (c *Controller) extractGroupVersionKind(template string) (schema.GroupVersionKind, error) {
	lines := strings.Split(template, "\n")
	var apiVersion, kind string

	// Parse both apiVersion and kind fields
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "apiVersion:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				apiVersion = strings.TrimSpace(parts[1])
				apiVersion = strings.Trim(apiVersion, `"'`)
			}
		}

		if strings.HasPrefix(trimmed, "kind:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				kind = strings.TrimSpace(parts[1])
				kind = strings.Trim(kind, `"'`)
			}
		}
	}

	if kind == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("no 'kind' field found in template")
	}
	if apiVersion == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("no 'apiVersion' field found in template")
	}

	// Parse apiVersion into group and version
	group, version := c.parseApiVersion(apiVersion)

	return schema.GroupVersionKind{
		Group:   group,
		Version: version,
		Kind:    kind,
	}, nil
}

// parseApiVersion parses an apiVersion string into group and version components
func (c *Controller) parseApiVersion(apiVersion string) (group, version string) {
	if !strings.Contains(apiVersion, "/") {
		return "", apiVersion // Core API: "v1" → group="", version="v1"
	}
	parts := strings.SplitN(apiVersion, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1] // Grouped API: "hypershift.openshift.io/v1beta1"
	}
	return "", apiVersion // Fallback
}

// deleteResourcesByLabels finds and deletes all resources with matching labels
func (c *Controller) deleteResourcesByLabels(ctx context.Context, resourceClient client.ResourceClient, gvk schema.GroupVersionKind, clusterID string) (int, error) {
	// Create label selector for resources to delete
	labelSelector := map[string]string{
		"cls.redhat.com/cluster-id": clusterID,
	}

	// Add controller name to selector for safety if available
	if c.config != nil && c.config.ControllerName != "" {
		labelSelector["cls.redhat.com/controller"] = c.config.ControllerName
	}

	c.logger.Debug("Searching for resources to delete",
		zap.String("group", gvk.Group),
		zap.String("version", gvk.Version),
		zap.String("kind", gvk.Kind),
		zap.String("cluster_id", clusterID),
		zap.Any("label_selector", labelSelector),
	)

	// Create resource list with CORRECT GroupVersionKind from template
	resourceList := &unstructured.UnstructuredList{}
	resourceList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind + "List",
	})

	// List resources with matching labels - use empty namespace to search all namespaces
	err := resourceClient.List(ctx, "", labelSelector, resourceList)
	if err != nil {
		return 0, fmt.Errorf("failed to list resources by labels: %w", err)
	}

	deletedCount := 0
	for _, resource := range resourceList.Items {
		c.logger.Debug("Deleting resource found by labels",
			zap.String("resource_name", resource.GetName()),
			zap.String("kind", gvk.Kind),
			zap.String("namespace", resource.GetNamespace()),
		)

		err := resourceClient.Delete(ctx, &resource)
		if err != nil {
			c.logger.Warn("Failed to delete individual resource",
				zap.String("resource_name", resource.GetName()),
				zap.Error(err),
			)
			// Continue with other resources - don't fail entirely
		} else {
			deletedCount++
		}
	}

	return deletedCount, nil
}

// ==================== NodePool Resource Management ====================

// evaluateNodePoolPreconditions checks if nodepool meets all preconditions
func (c *Controller) evaluateNodePoolPreconditions(nodepool *sdk.NodePool) bool {
	if c.controllerConfig == nil || len(c.controllerConfig.Spec.Preconditions) == 0 {
		return true // No preconditions means always pass
	}

	// Parse nodepool data for field evaluation
	nodepoolData := map[string]interface{}{
		"id":         nodepool.ID,
		"cluster_id": nodepool.ClusterID,
		"name":       nodepool.Name,
		"generation": nodepool.Generation,
	}

	// Add parsed spec
	var spec map[string]interface{}
	if err := json.Unmarshal(nodepool.Spec, &spec); err == nil {
		nodepoolData["spec"] = spec
	}

	// All preconditions must be true
	for _, precondition := range c.controllerConfig.Spec.Preconditions {
		if !c.evaluatePreconditionRule(nodepoolData, precondition) {
			c.logger.Debug("Precondition failed for nodepool",
				zap.String("field", precondition.Field),
				zap.String("operator", precondition.Operator),
				zap.String("nodepool_id", nodepool.ID),
			)
			return false
		}
	}

	return true
}

// getOrCreateAllNodePoolResources creates or updates all resources defined in the controller configuration for a nodepool
func (c *Controller) getOrCreateAllNodePoolResources(nodepool *sdk.NodePool, cluster *sdk.Cluster) (map[string]*unstructured.Unstructured, error) {
	if c.controllerConfig == nil {
		return nil, fmt.Errorf("no controller configuration loaded")
	}

	// Create context with timeout for resource operations
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second) // 2 minutes for all operations
	defer cancel()
	resources := make(map[string]*unstructured.Unstructured)

	// Get the appropriate client for this target
	c.logger.Debug("Getting client for target cluster (nodepool)",
		zap.String("nodepool_id", nodepool.ID),
		zap.String("cluster_id", nodepool.ClusterID),
		zap.Duration("timeout", 120*time.Second),
	)
	resourceClient, err := c.clientManager.GetClient(ctx, c.controllerConfig.Spec.Target, cluster)
	if err != nil {
		c.logger.Error("Failed to get resource client for nodepool",
			zap.String("nodepool_id", nodepool.ID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("failed to get resource client: %w", err)
	}
	c.logger.Debug("Successfully obtained resource client for nodepool",
		zap.String("nodepool_id", nodepool.ID),
	)

	// Process each resource
	for _, resourceConfig := range c.controllerConfig.Spec.Resources {
		c.logger.Debug("Processing nodepool resource",
			zap.String("resource_name", resourceConfig.Name),
			zap.String("nodepool_id", nodepool.ID),
		)

		resource, err := c.getOrCreateNodePoolResource(ctx, resourceClient, resourceConfig, nodepool, cluster, resources)
		if err != nil {
			c.logger.Error("Failed to process nodepool resource",
				zap.String("resource_name", resourceConfig.Name),
				zap.String("nodepool_id", nodepool.ID),
				zap.Error(err),
				zap.String("note", "Check network connectivity to remote cluster if timeout"),
			)
			return nil, fmt.Errorf("failed to create resource %s: %w", resourceConfig.Name, err)
		}
		resources[resourceConfig.Name] = resource

		c.logger.Info("NodePool resource processed successfully",
			zap.String("resource_name", resourceConfig.Name),
			zap.String("kind", resource.GetKind()),
			zap.String("name", resource.GetName()),
			zap.String("nodepool_id", nodepool.ID),
		)
	}

	return resources, nil
}

// getOrCreateNodePoolResource creates or updates a single resource for a nodepool
func (c *Controller) getOrCreateNodePoolResource(ctx context.Context, resourceClient client.ResourceClient, resourceConfig crd.ResourceConfig, nodepool *sdk.NodePool, cluster *sdk.Cluster, existingResources map[string]*unstructured.Unstructured) (*unstructured.Unstructured, error) {
	// Render the resource template with nodepool and cluster context
	resource, err := c.templateEngine.RenderNodePoolResource(resourceConfig.Name, nodepool, cluster, existingResources)
	if err != nil {
		return nil, fmt.Errorf("failed to render resource template: %w", err)
	}

	// Determine update strategy
	strategy := c.determineUpdateStrategy(resourceConfig, resource.GetKind())

	if strategy == crd.UpdateStrategyVersioned {
		return c.getOrCreateVersionedNodePoolResource(ctx, resourceClient, resourceConfig, resource, nodepool)
	} else {
		return c.getOrUpdateInPlaceNodePoolResource(ctx, resourceClient, resource, nodepool)
	}
}

// getOrUpdateInPlaceNodePoolResource handles in-place resource updates for nodepools
func (c *Controller) getOrUpdateInPlaceNodePoolResource(ctx context.Context, resourceClient client.ResourceClient, resource *unstructured.Unstructured, nodepool *sdk.NodePool) (*unstructured.Unstructured, error) {
	// Add standard labels to resource before any operations
	c.addNodePoolStandardLabels(resource, nodepool)

	// Try to get existing resource
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(resource.GroupVersionKind())

	err := resourceClient.Get(ctx, resource.GetName(), resource.GetNamespace(), existing)
	if err != nil {
		// Resource doesn't exist - create it
		if err := resourceClient.Create(ctx, resource); err != nil {
			return nil, fmt.Errorf("failed to create resource: %w", err)
		}
		return resource, nil
	}

	// Resource exists - update it if needed
	if c.needsUpdate(existing, resource) {
		// Preserve resource version for update
		resource.SetResourceVersion(existing.GetResourceVersion())
		if err := resourceClient.Update(ctx, resource); err != nil {
			return nil, fmt.Errorf("failed to update resource: %w", err)
		}
		// Return the updated resource
		return resource, nil
	}

	// No update needed - return existing resource with current status
	return existing, nil
}

// getOrCreateVersionedNodePoolResource handles versioned resource creation for nodepools
func (c *Controller) getOrCreateVersionedNodePoolResource(ctx context.Context, resourceClient client.ResourceClient, resourceConfig crd.ResourceConfig, resource *unstructured.Unstructured, nodepool *sdk.NodePool) (*unstructured.Unstructured, error) {
	// 1. Cleanup old generation resources first
	if err := c.cleanupOldNodePoolGenerations(ctx, resourceClient, nodepool.ID, nodepool.Generation, resource.GetKind(), resource.GetNamespace()); err != nil {
		c.logger.Warn("Failed to cleanup old nodepool generations", zap.Error(err))
		// Don't fail - continue with resource creation
	}

	// 2. Check for existing resource in current generation
	currentGenResource, err := c.findNodePoolResourceForGeneration(ctx, resourceClient, nodepool.ID, nodepool.Generation, resource.GetKind(), resource.GetNamespace())
	if err != nil {
		c.logger.Debug("No existing nodepool resource found for current generation", zap.Error(err))
	}

	if currentGenResource != nil {
		if c.isResourceRunning(currentGenResource) {
			// Resource is running - report its status (don't create new one)
			c.logger.Debug("Found running resource for current nodepool generation",
				zap.String("resource_name", currentGenResource.GetName()),
				zap.String("nodepool_id", nodepool.ID),
				zap.Int64("generation", nodepool.Generation),
			)
			return currentGenResource, nil
		} else {
			// Resource completed/failed - check if we should recreate based on version interval
			shouldRecreate, err := c.shouldRecreateVersionedResource(currentGenResource, resourceConfig)
			if err != nil {
				c.logger.Warn("Failed to check version interval for nodepool, recreating anyway", zap.Error(err))
				shouldRecreate = true
			}

			if !shouldRecreate {
				c.logger.Info("Version interval not elapsed for nodepool, keeping existing completed resource",
					zap.String("resource_name", currentGenResource.GetName()),
					zap.String("nodepool_id", nodepool.ID),
				)
				return currentGenResource, nil
			}

			// Version interval elapsed - delete and recreate
			c.logger.Info("Version interval elapsed for nodepool, deleting completed resource",
				zap.String("resource_name", currentGenResource.GetName()),
				zap.String("nodepool_id", nodepool.ID),
			)
			if err := resourceClient.Delete(ctx, currentGenResource); err != nil {
				c.logger.Warn("Failed to delete completed nodepool resource", zap.Error(err))
			}
		}
	}

	// 3. Create new resource with incremental naming
	c.logger.Info("Creating new versioned nodepool resource",
		zap.String("resource_name", resource.GetName()),
		zap.String("nodepool_id", nodepool.ID),
		zap.Int64("generation", nodepool.Generation),
	)

	// Add standard labels for cleanup tracking
	c.addNodePoolStandardLabels(resource, nodepool)

	if err := resourceClient.Create(ctx, resource); err != nil {
		return nil, fmt.Errorf("failed to create versioned nodepool resource: %w", err)
	}

	// 4. Cleanup old completed versions if keepVersions is configured
	if err := c.cleanupCompletedNodePoolVersions(ctx, resourceClient, resourceConfig, nodepool.ID, nodepool.Generation, resource.GetKind(), resource.GetNamespace()); err != nil {
		c.logger.Warn("Failed to cleanup completed nodepool versions", zap.Error(err))
	}

	return resource, nil
}

// addNodePoolStandardLabels adds tracking labels to resources for nodepool cleanup purposes
func (c *Controller) addNodePoolStandardLabels(resource *unstructured.Unstructured, nodepool *sdk.NodePool) {
	labels := resource.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	// Add nodepool ID label for resource identification during cleanup
	labels["cls.redhat.com/nodepool-id"] = nodepool.ID
	labels["nodepool-id"] = nodepool.ID // Also add unprefixed for versioned resource queries

	// Add cluster ID for hierarchy/context
	labels["cls.redhat.com/cluster-id"] = nodepool.ClusterID
	labels["cluster-id"] = nodepool.ClusterID

	// Add generation label for versioned resource tracking
	labels["nodepool-generation"] = fmt.Sprintf("%d", nodepool.Generation)

	// Add controller name label for safety (only delete resources we created)
	if c.config != nil && c.config.ControllerName != "" {
		labels["cls.redhat.com/controller"] = c.config.ControllerName
	}

	resource.SetLabels(labels)

	c.logger.Debug("Added standard labels to nodepool resource",
		zap.String("resource_name", resource.GetName()),
		zap.String("nodepool_id", nodepool.ID),
		zap.String("cluster_id", nodepool.ClusterID),
		zap.String("controller", c.config.ControllerName),
	)
}

// findNodePoolResourceForGeneration finds existing resources for a specific nodepool+generation
func (c *Controller) findNodePoolResourceForGeneration(ctx context.Context, resourceClient client.ResourceClient, nodepoolID string, generation int64, kind, namespace string) (*unstructured.Unstructured, error) {
	// List resources with nodepool-id and nodepool-generation labels
	labels := map[string]string{
		"nodepool-id":         nodepoolID,
		"nodepool-generation": fmt.Sprintf("%d", generation),
	}

	c.logger.Info("Looking for existing nodepool resource",
		zap.String("nodepool_id", nodepoolID),
		zap.Int64("generation", generation),
		zap.String("kind", kind),
		zap.String("namespace", namespace),
		zap.Any("labels", labels),
	)

	resourceList := &unstructured.UnstructuredList{}
	// Set the GroupVersionKind for the list based on the resource kind
	switch kind {
	case "Job":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "batch",
			Version: "v1",
			Kind:    "JobList",
		})
	case "Pod":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "PodList",
		})
	case "NodePool":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "hypershift.openshift.io",
			Version: "v1beta1",
			Kind:    "NodePoolList",
		})
	default:
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    kind + "List",
		})
	}

	err := resourceClient.List(ctx, namespace, labels, resourceList)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodepool resources: %w", err)
	}

	if len(resourceList.Items) == 0 {
		return nil, fmt.Errorf("no resources found for nodepool %s generation %d", nodepoolID, generation)
	}

	// Return the first resource found (should only be one per generation)
	resource := &resourceList.Items[0]

	// Refresh the resource to get current status
	refreshed := &unstructured.Unstructured{}
	refreshed.SetGroupVersionKind(resource.GroupVersionKind())

	if err := resourceClient.Get(ctx, resource.GetName(), resource.GetNamespace(), refreshed); err == nil {
		return refreshed, nil
	}

	return resource, nil
}

// cleanupOldNodePoolGenerations removes resources from previous nodepool generations
func (c *Controller) cleanupOldNodePoolGenerations(ctx context.Context, resourceClient client.ResourceClient, nodepoolID string, currentGeneration int64, kind, namespace string) error {
	// List all resources for this nodepool (any generation)
	labels := map[string]string{
		"nodepool-id": nodepoolID,
	}

	resourceList := &unstructured.UnstructuredList{}
	switch kind {
	case "Job":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "batch",
			Version: "v1",
			Kind:    "JobList",
		})
	case "Pod":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "PodList",
		})
	case "NodePool":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "hypershift.openshift.io",
			Version: "v1beta1",
			Kind:    "NodePoolList",
		})
	default:
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    kind + "List",
		})
	}

	err := resourceClient.List(ctx, namespace, labels, resourceList)
	if err != nil {
		return fmt.Errorf("failed to list nodepool resources for cleanup: %w", err)
	}

	for _, resource := range resourceList.Items {
		generationLabel := resource.GetLabels()["nodepool-generation"]
		if generationLabel == "" {
			continue
		}

		var resourceGeneration int64
		if _, err := fmt.Sscanf(generationLabel, "%d", &resourceGeneration); err != nil {
			continue
		}

		// Delete resources from previous generations
		if resourceGeneration < currentGeneration {
			c.logger.Info("Cleaning up old nodepool generation resource",
				zap.String("resource_name", resource.GetName()),
				zap.String("nodepool_id", nodepoolID),
				zap.Int64("resource_generation", resourceGeneration),
				zap.Int64("current_generation", currentGeneration),
			)

			if err := resourceClient.Delete(ctx, &resource); err != nil {
				c.logger.Warn("Failed to delete old nodepool generation resource",
					zap.String("resource_name", resource.GetName()),
					zap.Error(err),
				)
			}
		}
	}

	return nil
}

// cleanupCompletedNodePoolVersions removes old completed versions beyond the keepVersions limit for nodepools
func (c *Controller) cleanupCompletedNodePoolVersions(ctx context.Context, resourceClient client.ResourceClient, resourceConfig crd.ResourceConfig, nodepoolID string, currentGeneration int64, kind, namespace string) error {
	keepVersions := 2
	if resourceConfig.ResourceManagement != nil && resourceConfig.ResourceManagement.KeepVersions > 0 {
		keepVersions = resourceConfig.ResourceManagement.KeepVersions
	}

	labels := map[string]string{
		"nodepool-id":         nodepoolID,
		"nodepool-generation": fmt.Sprintf("%d", currentGeneration),
	}

	resourceList := &unstructured.UnstructuredList{}
	switch kind {
	case "Job":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "batch",
			Version: "v1",
			Kind:    "JobList",
		})
	case "Pod":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "PodList",
		})
	case "NodePool":
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "hypershift.openshift.io",
			Version: "v1beta1",
			Kind:    "NodePoolList",
		})
	default:
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    kind + "List",
		})
	}

	err := resourceClient.List(ctx, namespace, labels, resourceList)
	if err != nil {
		return fmt.Errorf("failed to list nodepool resources for version cleanup: %w", err)
	}

	if len(resourceList.Items) <= keepVersions {
		return nil
	}

	// Filter to only completed resources and sort by creation time (newest first)
	var completedResources []*unstructured.Unstructured
	for i := range resourceList.Items {
		resource := &resourceList.Items[i]
		if !c.isResourceRunning(resource) {
			completedResources = append(completedResources, resource)
		}
	}

	if len(completedResources) <= keepVersions {
		return nil
	}

	// Sort by creation time (newest first)
	for i := 0; i < len(completedResources)-1; i++ {
		for j := i + 1; j < len(completedResources); j++ {
			if completedResources[i].GetCreationTimestamp().Time.Before(completedResources[j].GetCreationTimestamp().Time) {
				completedResources[i], completedResources[j] = completedResources[j], completedResources[i]
			}
		}
	}

	// Delete old completed resources beyond keepVersions limit
	resourcesToDelete := completedResources[keepVersions:]
	for _, resource := range resourcesToDelete {
		c.logger.Info("Cleaning up old completed nodepool version",
			zap.String("resource_name", resource.GetName()),
			zap.String("nodepool_id", nodepoolID),
			zap.Int64("generation", currentGeneration),
		)

		if err := resourceClient.Delete(ctx, resource); err != nil {
			c.logger.Warn("Failed to delete old completed nodepool version",
				zap.String("resource_name", resource.GetName()),
				zap.Error(err),
			)
		}
	}

	return nil
}

// deleteAllNodePoolResources deletes all resources created by this controller for a specific nodepool
func (c *Controller) deleteAllNodePoolResources(nodepoolID string) error {
	if c.controllerConfig == nil {
		c.logger.Warn("No controller configuration loaded, skipping nodepool resource cleanup")
		return nil
	}

	c.logger.Info("Starting resource cleanup for deleted nodepool", zap.String("nodepool_id", nodepoolID))

	// Create context with timeout for deletion operations
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create a minimal cluster object for client resolution
	// For nodepool cleanup, we need to get cluster ID from somewhere - use empty for now
	// The client manager should handle this case gracefully
	minimalCluster := &sdk.Cluster{ID: ""}

	// Get the appropriate client for this target
	resourceClient, err := c.clientManager.GetClient(ctx, c.controllerConfig.Spec.Target, minimalCluster)
	if err != nil {
		c.logger.Error("Failed to get resource client for nodepool cleanup",
			zap.String("nodepool_id", nodepoolID),
			zap.Error(err),
		)
		return fmt.Errorf("failed to get resource client: %w", err)
	}

	var deletionErrors []error

	// Process each resource for deletion using label-based discovery
	for _, resourceConfig := range c.controllerConfig.Spec.Resources {
		c.logger.Debug("Attempting to delete nodepool resources by labels",
			zap.String("resource_name", resourceConfig.Name),
			zap.String("nodepool_id", nodepoolID),
		)

		gvk, err := c.extractGroupVersionKind(resourceConfig.Template)
		if err != nil {
			deletionErrors = append(deletionErrors, err)
			c.logger.Warn("Failed to extract GroupVersionKind for nodepool cleanup",
				zap.String("resource_name", resourceConfig.Name),
				zap.String("nodepool_id", nodepoolID),
				zap.Error(err),
			)
			continue
		}

		deletedCount, err := c.deleteNodePoolResourcesByLabels(ctx, resourceClient, gvk, nodepoolID)
		if err != nil {
			deletionErrors = append(deletionErrors, err)
			c.logger.Warn("Failed to delete nodepool resources during cleanup",
				zap.String("resource_name", resourceConfig.Name),
				zap.String("nodepool_id", nodepoolID),
				zap.Error(err),
			)
		} else {
			c.logger.Info("Successfully deleted nodepool resources by labels",
				zap.String("resource_name", resourceConfig.Name),
				zap.String("nodepool_id", nodepoolID),
				zap.Int("deleted_count", deletedCount),
			)
		}
	}

	if len(deletionErrors) > 0 {
		c.logger.Warn("Some nodepool resource deletions failed during cleanup",
			zap.String("nodepool_id", nodepoolID),
			zap.Int("failure_count", len(deletionErrors)),
		)
		return fmt.Errorf("failed to delete %d resources during nodepool cleanup", len(deletionErrors))
	}

	c.logger.Info("NodePool resource cleanup completed successfully", zap.String("nodepool_id", nodepoolID))
	return nil
}

// deleteNodePoolResourcesByLabels finds and deletes all resources with matching nodepool labels
func (c *Controller) deleteNodePoolResourcesByLabels(ctx context.Context, resourceClient client.ResourceClient, gvk schema.GroupVersionKind, nodepoolID string) (int, error) {
	labelSelector := map[string]string{
		"cls.redhat.com/nodepool-id": nodepoolID,
	}

	if c.config != nil && c.config.ControllerName != "" {
		labelSelector["cls.redhat.com/controller"] = c.config.ControllerName
	}

	resourceList := &unstructured.UnstructuredList{}
	resourceList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind + "List",
	})

	err := resourceClient.List(ctx, "", labelSelector, resourceList)
	if err != nil {
		return 0, fmt.Errorf("failed to list nodepool resources by labels: %w", err)
	}

	deletedCount := 0
	for _, resource := range resourceList.Items {
		err := resourceClient.Delete(ctx, &resource)
		if err != nil {
			c.logger.Warn("Failed to delete individual nodepool resource",
				zap.String("resource_name", resource.GetName()),
				zap.Error(err),
			)
		} else {
			deletedCount++
		}
	}

	return deletedCount, nil
}

// evaluateAndReportNodePoolStatus evaluates resource status and reports to cls-backend for nodepools
func (c *Controller) evaluateAndReportNodePoolStatus(event *sdk.NodePoolEvent, nodepool *sdk.NodePool, cluster *sdk.Cluster, resources map[string]*unstructured.Unstructured) error {
	c.logger.Info("Starting nodepool status evaluation and reporting",
		zap.String("nodepool_id", event.NodePoolID),
		zap.Int("resource_count", len(resources)),
	)

	if c.controllerConfig == nil {
		return fmt.Errorf("no controller configuration loaded")
	}

	ctx := context.Background()

	// Get the appropriate client for refreshing resource status
	resourceClient, err := c.clientManager.GetClient(ctx, c.controllerConfig.Spec.Target, cluster)
	if err != nil {
		c.logger.Warn("Failed to get resource client for nodepool status refresh", zap.Error(err))
	}

	// Refresh resources with current status from Kubernetes
	refreshedResources := make(map[string]*unstructured.Unstructured)
	for name, resource := range resources {
		if resource != nil && resourceClient != nil {
			refreshed := &unstructured.Unstructured{}
			refreshed.SetGroupVersionKind(resource.GroupVersionKind())

			if err := resourceClient.Get(ctx, resource.GetName(), resource.GetNamespace(), refreshed); err == nil {
				refreshedResources[name] = refreshed
				c.logger.Info("Refreshed nodepool resource status",
					zap.String("resource_name", name),
					zap.String("resource_kind", resource.GetKind()),
				)
			} else {
				refreshedResources[name] = resource
				c.logger.Warn("Failed to refresh nodepool resource status, using cached",
					zap.String("resource_name", name),
					zap.Error(err),
				)
			}
		} else {
			refreshedResources[name] = resource
		}
	}

	update := sdk.NewNodePoolStatusUpdate(event.NodePoolID, c.config.ControllerName, nodepool.Generation)
	update.ClusterID = nodepool.ClusterID

	// Add default Applied condition
	update.AddCondition(sdk.NewCondition(
		"Applied",
		"True",
		"ResourcesCreated",
		fmt.Sprintf("Created %d resources successfully", len(resources)),
	))

	// Evaluate custom status conditions from ControllerConfig using refreshed resources
	for _, conditionConfig := range c.controllerConfig.Spec.StatusConditions {
		status, reason, message, err := c.templateEngine.RenderNodePoolStatusCondition(conditionConfig.Name, nodepool, cluster, refreshedResources)
		if err != nil {
			c.logger.Error("Failed to render nodepool status condition",
				zap.String("condition", conditionConfig.Name),
				zap.Error(err),
			)
			continue
		}

		update.AddCondition(sdk.NewCondition(
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

			if status, exists, _ := unstructured.NestedMap(resource.Object, "status"); exists {
				resourceStatus["resource_status"] = status
			}

			resourceStatuses[name] = resourceStatus
		}
	}

	update.SetMetadata("resources", resourceStatuses)

	c.logger.Info("Publishing nodepool status update to cls-backend",
		zap.String("nodepool_id", event.NodePoolID),
		zap.Int("condition_count", len(update.Conditions)),
	)

	err = c.sdkClient.ReportStatus(update)
	if err != nil {
		c.logger.Error("Failed to publish nodepool status update", zap.Error(err))
		return err
	}

	c.logger.Info("NodePool status update published successfully",
		zap.String("nodepool_id", event.NodePoolID),
	)

	return nil
}
