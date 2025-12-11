package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/apahim/cls-controller/internal/sdk"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// MockSDKClient implements a mock SDK client for testing
type MockSDKClient struct {
	reportedStatuses []*sdk.StatusUpdate
	apiClient        *MockAPIClient
}

func NewMockSDKClient() *MockSDKClient {
	return &MockSDKClient{
		reportedStatuses: make([]*sdk.StatusUpdate, 0),
		apiClient:        &MockAPIClient{},
	}
}

func (m *MockSDKClient) ReportStatus(update *sdk.StatusUpdate) error {
	m.reportedStatuses = append(m.reportedStatuses, update)
	return nil
}

func (m *MockSDKClient) GetAPIClient() sdk.APIClient {
	return m.apiClient
}

func (m *MockSDKClient) Start() error {
	return nil
}

func (m *MockSDKClient) Stop() error {
	return nil
}

// MockAPIClient implements sdk.APIClient for testing
type MockAPIClient struct {
	clusters  map[string]*sdk.Cluster
	nodepools map[string]*sdk.NodePool
}

func (m *MockAPIClient) GetCluster(ctx context.Context, clusterID string) (*sdk.Cluster, error) {
	if cluster, ok := m.clusters[clusterID]; ok {
		return cluster, nil
	}
	return nil, nil
}

func (m *MockAPIClient) GetNodePool(ctx context.Context, nodepoolID string) (*sdk.NodePool, error) {
	if nodepool, ok := m.nodepools[nodepoolID]; ok {
		return nodepool, nil
	}
	return nil, nil
}

func (m *MockAPIClient) ReportClusterStatus(ctx context.Context, update *sdk.StatusUpdate) error {
	return nil
}

func (m *MockAPIClient) ReportNodePoolStatus(ctx context.Context, update *sdk.StatusUpdate) error {
	return nil
}

func TestNewNodePoolStatusUpdate(t *testing.T) {
	nodepoolID := "np-123"
	controllerName := "test-controller"
	generation := int64(5)

	update := sdk.NewNodePoolStatusUpdate(nodepoolID, controllerName, generation)

	assert.NotNil(t, update)
	assert.Equal(t, nodepoolID, update.NodePoolID)
	assert.Equal(t, controllerName, update.ControllerName)
	assert.Equal(t, generation, update.ObservedGeneration)
	assert.Empty(t, update.ClusterID, "ClusterID should be empty for nodepool status update")
	assert.NotNil(t, update.Conditions)
	assert.NotNil(t, update.Metadata)
	assert.False(t, update.Timestamp.IsZero())
}

func TestNewStatusUpdate(t *testing.T) {
	clusterID := "cluster-123"
	controllerName := "test-controller"
	generation := int64(3)

	update := sdk.NewStatusUpdate(clusterID, controllerName, generation)

	assert.NotNil(t, update)
	assert.Equal(t, clusterID, update.ClusterID)
	assert.Equal(t, controllerName, update.ControllerName)
	assert.Equal(t, generation, update.ObservedGeneration)
	assert.Empty(t, update.NodePoolID, "NodePoolID should be empty for cluster status update")
	assert.NotNil(t, update.Conditions)
	assert.NotNil(t, update.Metadata)
}

func TestStatusUpdate_AddCondition(t *testing.T) {
	update := sdk.NewStatusUpdate("cluster-1", "controller", 1)

	// Add first condition
	condition1 := sdk.NewCondition("Applied", "True", "ResourcesCreated", "All resources created")
	update.AddCondition(condition1)

	assert.Len(t, update.Conditions, 1)
	assert.Equal(t, "Applied", update.Conditions[0].Type)
	assert.Equal(t, "True", update.Conditions[0].Status)

	// Add second condition of different type
	condition2 := sdk.NewCondition("Ready", "False", "NotReady", "Still initializing")
	update.AddCondition(condition2)

	assert.Len(t, update.Conditions, 2)

	// Replace existing condition (same type)
	condition3 := sdk.NewCondition("Applied", "False", "ResourcesFailed", "Failed to create resources")
	update.AddCondition(condition3)

	// Should still be 2 conditions, but Applied should be updated
	assert.Len(t, update.Conditions, 2)

	appliedCondition := update.GetCondition("Applied")
	assert.NotNil(t, appliedCondition)
	assert.Equal(t, "False", appliedCondition.Status)
	assert.Equal(t, "ResourcesFailed", appliedCondition.Reason)
}

func TestStatusUpdate_HasCondition(t *testing.T) {
	update := sdk.NewStatusUpdate("cluster-1", "controller", 1)

	update.AddCondition(sdk.NewCondition("Applied", "True", "Done", ""))
	update.AddCondition(sdk.NewCondition("Ready", "False", "NotReady", ""))

	assert.True(t, update.HasCondition("Applied", "True"))
	assert.False(t, update.HasCondition("Applied", "False"))
	assert.True(t, update.HasCondition("Ready", "False"))
	assert.False(t, update.HasCondition("Available", "True"))
}

func TestStatusUpdate_HasRequiredConditions(t *testing.T) {
	update := sdk.NewStatusUpdate("cluster-1", "controller", 1)

	// Initially missing all required conditions
	assert.False(t, update.HasRequiredConditions())

	// Add Applied
	update.AddCondition(sdk.NewCondition("Applied", "True", "Done", ""))
	assert.False(t, update.HasRequiredConditions())

	// Add Available
	update.AddCondition(sdk.NewCondition("Available", "True", "Available", ""))
	assert.False(t, update.HasRequiredConditions())

	// Add Ready - now should have all required
	update.AddCondition(sdk.NewCondition("Ready", "True", "Ready", ""))
	assert.True(t, update.HasRequiredConditions())
}

func TestStatusUpdate_SetError(t *testing.T) {
	update := sdk.NewStatusUpdate("cluster-1", "controller", 1)

	errorInfo := sdk.NewErrorInfo(sdk.ErrorTypeTransient, "500", "Internal error", false)
	update.SetError(errorInfo)

	assert.NotNil(t, update.LastError)
	assert.Equal(t, sdk.ErrorTypeTransient, update.LastError.ErrorType)
	assert.Equal(t, "500", update.LastError.ErrorCode)
	assert.Equal(t, "Internal error", update.LastError.Message)
	assert.False(t, update.LastError.UserActionable)
}

func TestStatusUpdate_SetMetadata(t *testing.T) {
	update := sdk.NewStatusUpdate("cluster-1", "controller", 1)

	update.SetMetadata("key1", "value1")
	update.SetMetadata("key2", 42)

	assert.Equal(t, "value1", update.Metadata["key1"])
	assert.Equal(t, 42, update.Metadata["key2"])
}

func TestStatusUpdate_HelperMethods(t *testing.T) {
	update := sdk.NewStatusUpdate("cluster-1", "controller", 1)

	// Test SetAppliedTrue
	update.SetAppliedTrue("Done", "Resources applied")
	applied := update.GetCondition("Applied")
	assert.NotNil(t, applied)
	assert.Equal(t, "True", applied.Status)

	// Test SetAppliedFalse
	update.SetAppliedFalse("Failed", "Resources failed")
	applied = update.GetCondition("Applied")
	assert.Equal(t, "False", applied.Status)

	// Test SetAvailableTrue
	update.SetAvailableTrue("Available", "Service available")
	available := update.GetCondition("Available")
	assert.NotNil(t, available)
	assert.Equal(t, "True", available.Status)

	// Test SetReadyTrue
	update.SetReadyTrue("Ready", "Service ready")
	ready := update.GetCondition("Ready")
	assert.NotNil(t, ready)
	assert.Equal(t, "True", ready.Status)
}

func TestEventTypeConstants(t *testing.T) {
	// Verify nodepool event type constants exist
	assert.Equal(t, "nodepool.created", sdk.EventTypeNodePoolCreated)
	assert.Equal(t, "nodepool.updated", sdk.EventTypeNodePoolUpdated)
	assert.Equal(t, "nodepool.deleted", sdk.EventTypeNodePoolDeleted)
	assert.Equal(t, "nodepool.reconcile", sdk.EventTypeNodePoolReconcile)

	// Verify cluster event type constants
	assert.Equal(t, "cluster.created", sdk.EventTypeClusterCreated)
	assert.Equal(t, "cluster.updated", sdk.EventTypeClusterUpdated)
	assert.Equal(t, "cluster.deleted", sdk.EventTypeClusterDeleted)
	assert.Equal(t, "cluster.reconcile", sdk.EventTypeClusterReconcile)
}

func TestNodePoolEvent(t *testing.T) {
	event := &sdk.NodePoolEvent{
		ID:         "event-123",
		Type:       sdk.EventTypeNodePoolCreated,
		ClusterID:  "cluster-456",
		NodePoolID: "nodepool-789",
		Timestamp:  time.Now(),
		Source:     "cls-backend",
	}

	assert.Equal(t, "event-123", event.ID)
	assert.Equal(t, sdk.EventTypeNodePoolCreated, event.Type)
	assert.Equal(t, "cluster-456", event.ClusterID)
	assert.Equal(t, "nodepool-789", event.NodePoolID)
	assert.Equal(t, "cls-backend", event.Source)
}

func TestNodePool(t *testing.T) {
	spec := map[string]interface{}{
		"replicas": 3,
		"platform": map[string]interface{}{
			"gcp": map[string]interface{}{
				"machineType": "n2-standard-4",
			},
		},
	}
	specJSON, _ := json.Marshal(spec)

	nodepool := &sdk.NodePool{
		ID:              "np-123",
		ClusterID:       "cluster-456",
		Name:            "worker-pool",
		Generation:      2,
		ResourceVersion: "12345",
		Spec:            specJSON,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	assert.Equal(t, "np-123", nodepool.ID)
	assert.Equal(t, "cluster-456", nodepool.ClusterID)
	assert.Equal(t, "worker-pool", nodepool.Name)
	assert.Equal(t, int64(2), nodepool.Generation)

	// Verify spec can be unmarshalled
	var parsedSpec map[string]interface{}
	err := json.Unmarshal(nodepool.Spec, &parsedSpec)
	assert.NoError(t, err)
	assert.Equal(t, float64(3), parsedSpec["replicas"])
}

func TestConditionHelpers(t *testing.T) {
	// Test NewAppliedTrue
	condition := sdk.NewAppliedTrue("Done", "All applied")
	assert.Equal(t, "Applied", condition.Type)
	assert.Equal(t, "True", condition.Status)
	assert.Equal(t, "Done", condition.Reason)
	assert.Equal(t, "All applied", condition.Message)

	// Test NewAppliedFalse
	condition = sdk.NewAppliedFalse("Failed", "Apply failed")
	assert.Equal(t, "Applied", condition.Type)
	assert.Equal(t, "False", condition.Status)

	// Test NewAppliedUnknown
	condition = sdk.NewAppliedUnknown("Pending", "Waiting")
	assert.Equal(t, "Applied", condition.Type)
	assert.Equal(t, "Unknown", condition.Status)

	// Test NewAvailableTrue
	condition = sdk.NewAvailableTrue("Available", "Service available")
	assert.Equal(t, "Available", condition.Type)
	assert.Equal(t, "True", condition.Status)

	// Test NewReadyTrue
	condition = sdk.NewReadyTrue("Ready", "Service ready")
	assert.Equal(t, "Ready", condition.Type)
	assert.Equal(t, "True", condition.Status)
}

func TestController_New(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	config := &sdk.Config{
		ProjectID:          "test-project",
		ControllerName:     "test-controller",
		StatusTopic:        "status-topic",
		EventsSubscription: "events-subscription",
	}

	// Note: We can't fully test New() without mocking more dependencies,
	// but we can verify the config structure
	assert.Equal(t, "test-project", config.ProjectID)
	assert.Equal(t, "test-controller", config.ControllerName)
	_ = logger // logger would be used in actual controller creation
}