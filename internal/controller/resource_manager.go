package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/apahim/cls-controller/internal/crd"
	"github.com/apahim/cls-controller/internal/client"
	controllersdk "github.com/apahim/controller-sdk"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// evaluatePreconditions checks if cluster meets all preconditions
func (c *Controller) evaluatePreconditions(cluster *controllersdk.Cluster) bool {
	if c.controllerConfig == nil || len(c.controllerConfig.Spec.Preconditions) == 0 {
		return true // No preconditions means always pass
	}

	// Parse cluster spec for field evaluation
	var clusterData map[string]interface{}
	clusterData = map[string]interface{}{
		"id":                 cluster.ID,
		"name":               cluster.Name,
		"organization_domain": cluster.OrganizationDomain,
		"generation":         cluster.Generation,
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
func (c *Controller) reportPreconditionFailure(event *controllersdk.ClusterEvent, cluster *controllersdk.Cluster) error {
	failedPreconditions := c.getFailedPreconditions(cluster)

	update := controllersdk.NewStatusUpdate(event.ClusterID, c.config.ControllerName, event.Generation)

	update.AddCondition(controllersdk.NewCondition(
		"Applied",
		"False",
		"PreconditionsNotMet",
		fmt.Sprintf("Preconditions not met: %s", strings.Join(failedPreconditions, ", ")),
	))

	return c.sdkClient.ReportStatus(update)
}

// getFailedPreconditions returns a list of failed precondition descriptions
func (c *Controller) getFailedPreconditions(cluster *controllersdk.Cluster) []string {
	var failed []string

	if c.controllerConfig == nil {
		return failed
	}

	// Parse cluster data
	var clusterData map[string]interface{}
	clusterData = map[string]interface{}{
		"id":                 cluster.ID,
		"name":               cluster.Name,
		"organization_domain": cluster.OrganizationDomain,
		"generation":         cluster.Generation,
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
func (c *Controller) getOrCreateAllResources(cluster *controllersdk.Cluster) (map[string]*unstructured.Unstructured, error) {
	if c.controllerConfig == nil {
		return nil, fmt.Errorf("no controller configuration loaded")
	}

	ctx := context.Background()
	resources := make(map[string]*unstructured.Unstructured)

	// Get the appropriate client for this target
	resourceClient, err := c.clientManager.GetClient(ctx, c.controllerConfig.Spec.Target, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource client: %w", err)
	}

	// Process each resource
	for _, resourceConfig := range c.controllerConfig.Spec.Resources {
		resource, err := c.getOrCreateResource(ctx, resourceClient, resourceConfig, cluster, resources)
		if err != nil {
			return nil, fmt.Errorf("failed to create resource %s: %w", resourceConfig.Name, err)
		}
		resources[resourceConfig.Name] = resource

		c.logger.Debug("Resource processed",
			zap.String("resource_name", resourceConfig.Name),
			zap.String("kind", resource.GetKind()),
			zap.String("name", resource.GetName()),
		)
	}

	return resources, nil
}

// getOrCreateResource creates or updates a single resource
func (c *Controller) getOrCreateResource(ctx context.Context, resourceClient client.ResourceClient, resourceConfig crd.ResourceConfig, cluster *controllersdk.Cluster, existingResources map[string]*unstructured.Unstructured) (*unstructured.Unstructured, error) {
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
		return c.getOrUpdateInPlaceResource(ctx, resourceClient, resource)
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
func (c *Controller) getOrUpdateInPlaceResource(ctx context.Context, resourceClient client.ResourceClient, resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
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
	}

	// Return the existing resource with current status
	return existing, nil
}

// getOrCreateVersionedResource handles versioned resource creation following DESIGN.md pattern with time-based intervals
func (c *Controller) getOrCreateVersionedResource(ctx context.Context, resourceClient client.ResourceClient, resourceConfig crd.ResourceConfig, resource *unstructured.Unstructured, cluster *controllersdk.Cluster) (*unstructured.Unstructured, error) {
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
func (c *Controller) evaluateAndReportStatus(event *controllersdk.ClusterEvent, cluster *controllersdk.Cluster, resources map[string]*unstructured.Unstructured) error {
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

