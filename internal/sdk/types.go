package sdk

import (
	"context"
	"encoding/json"
	"time"
)

// Condition represents a condition on a resource
type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"` // True, False, Unknown
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

// ErrorInfo represents detailed error information
type ErrorInfo struct {
	ErrorType      string                 `json:"error_type"` // Fatal, Configuration, Transient, System
	ErrorCode      string                 `json:"error_code"` // HTTP status, k8s error code, etc.
	Message        string                 `json:"message"`    // Human-readable error message
	Details        map[string]interface{} `json:"details,omitempty"`
	UserActionable bool                   `json:"user_actionable"` // Whether user can fix this
	RetryAfter     *time.Duration         `json:"retry_after,omitempty"`
	Timestamp      time.Time              `json:"timestamp"`
}

// StatusUpdate represents a status update from a controller
type StatusUpdate struct {
	ClusterID          string                 `json:"cluster_id"`
	NodePoolID         string                 `json:"nodepool_id,omitempty"`
	ControllerName     string                 `json:"controller_name"`
	ObservedGeneration int64                  `json:"observed_generation"`
	Conditions         []Condition            `json:"conditions"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
	LastError          *ErrorInfo             `json:"last_error,omitempty"`
	Timestamp          time.Time              `json:"timestamp"`
}

// ClusterEvent represents a cluster lifecycle event that controllers receive
// Following Kubernetes pattern: lightweight trigger messages, controllers fetch via API
type ClusterEvent struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	ClusterID  string                 `json:"cluster_id"`
	Generation int64                  `json:"generation"` // Current cluster generation
	Timestamp  time.Time              `json:"timestamp"`
	Source     string                 `json:"source"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"` // Additional event metadata
	// Removed: Organization ID (not used in new architecture)
	// Removed: Cluster object (controllers should fetch via API)
	// Removed: Changes object (controllers should determine changes by comparing spec)
}

// NodePoolEvent represents a nodepool lifecycle event that controllers receive
type NodePoolEvent struct {
	ID         string      `json:"id"`
	Type       string      `json:"type"`
	ClusterID  string      `json:"cluster_id"`
	NodePoolID string      `json:"nodepool_id"`
	NodePool   *NodePool   `json:"nodepool,omitempty"`
	Changes    interface{} `json:"changes,omitempty"`
	Timestamp  time.Time   `json:"timestamp"`
	Source     string      `json:"source"`
}

// ControllerEvent represents controller lifecycle events
type ControllerEvent struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	ControllerName string    `json:"controller_name"`
	ClusterID      string    `json:"cluster_id"`
	NodePoolID     string    `json:"nodepool_id,omitempty"`
	Action         string    `json:"action"` // start, stop, restart
	Timestamp      time.Time `json:"timestamp"`
	Source         string    `json:"source"`
}

// ClusterStatusInfo represents the Kubernetes-like status block for clusters
type ClusterStatusInfo struct {
	ObservedGeneration int64       `json:"observedGeneration"`
	Conditions         []Condition `json:"conditions"`
	Phase              string      `json:"phase,omitempty"`   // Current lifecycle phase
	Message            string      `json:"message,omitempty"` // Human-readable status message
	Reason             string      `json:"reason,omitempty"`  // Machine-readable reason
	LastUpdateTime     time.Time   `json:"lastUpdateTime"`
}

// Cluster represents cluster information
type Cluster struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Generation      int64              `json:"generation"`
	ResourceVersion string             `json:"resource_version"`
	Spec            json.RawMessage    `json:"spec"`
	Status          *ClusterStatusInfo `json:"status,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	CreatedBy       string             `json:"created_by"` // User email who created the cluster
}

// NodePool represents nodepool information
type NodePool struct {
	ID              string          `json:"id"`
	ClusterID       string          `json:"cluster_id"`
	Name            string          `json:"name"`
	Generation      int64           `json:"generation"`
	ResourceVersion string          `json:"resource_version"`
	Spec            json.RawMessage `json:"spec"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// EventHandler defines the interface for handling events
type EventHandler interface {
	HandleClusterEvent(event *ClusterEvent) error
	HandleNodePoolEvent(event *NodePoolEvent) error
	HandleControllerEvent(event *ControllerEvent) error
}

// StatusReporter defines the interface for reporting status
type StatusReporter interface {
	ReportStatus(update *StatusUpdate) error
}

// APIClient defines the interface for interacting with the cls-backend API
type APIClient interface {
	// GetCluster fetches cluster spec from the API
	GetCluster(ctx context.Context, clusterID string) (*Cluster, error)

	// GetNodePool fetches nodepool spec from the API
	GetNodePool(ctx context.Context, nodepoolID string) (*NodePool, error)

	// ReportClusterStatus reports cluster status via API
	ReportClusterStatus(ctx context.Context, update *StatusUpdate) error

	// ReportNodePoolStatus reports nodepool status via API
	ReportNodePoolStatus(ctx context.Context, update *StatusUpdate) error
}

// Config represents SDK configuration
type Config struct {
	ProjectID           string        `json:"project_id"`
	ControllerName      string        `json:"controller_name"`
	StatusTopic         string        `json:"status_topic"`
	EventsSubscription  string        `json:"events_subscription"`
	PubSubEmulatorHost  string        `json:"pubsub_emulator_host,omitempty"`
	CredentialsFile     string        `json:"credentials_file,omitempty"`
	RetryAttempts       int           `json:"retry_attempts"`
	RetryBackoff        time.Duration `json:"retry_backoff"`
	HealthCheckInterval time.Duration `json:"health_check_interval"`

	// API client configuration for fetching specs and reporting status
	APIBaseURL string        `json:"api_base_url"` // e.g., "http://localhost:8080/api/v1"
	APITimeout time.Duration `json:"api_timeout"`  // HTTP client timeout

	// Controller authentication configuration
	ControllerEmail string `json:"controller_email"` // Email to use for X-User-Email header (e.g., "controller@system.local")
}

// Event types that controllers can receive
const (
	EventTypeClusterCreated   = "cluster.created"
	EventTypeClusterUpdated   = "cluster.updated"
	EventTypeClusterDeleted   = "cluster.deleted"
	EventTypeClusterReconcile = "cluster.reconcile"
	EventTypeNodePoolCreated  = "nodepool.created"
	EventTypeNodePoolUpdated  = "nodepool.updated"
	EventTypeNodePoolDeleted  = "nodepool.deleted"
	EventTypeControllerStart  = "controller.start"
	EventTypeControllerStop   = "controller.stop"
)

// Status values
const (
	StatusPending      = "Pending"
	StatusProvisioning = "Provisioning"
	StatusReady        = "Ready"
	StatusFailed       = "Failed"
	StatusDeleting     = "Deleting"
)

// Health values
const (
	HealthUnknown  = "Unknown"
	HealthHealthy  = "Healthy"
	HealthDegraded = "Degraded"
	HealthFailed   = "Failed"
)

// Condition types
const (
	ConditionApplied     = "Applied"   // Required: Has the controller applied/created its resources?
	ConditionAvailable   = "Available" // Required: Used by cls-backend for status aggregation - are resources available?
	ConditionReady       = "Ready"     // Required: Is everything ready to serve requests?
	ConditionProgressing = "Progressing"
	ConditionDegraded    = "Degraded"
)

// Error types
const (
	ErrorTypeFatal         = "Fatal"
	ErrorTypeConfiguration = "Configuration"
	ErrorTypeTransient     = "Transient"
	ErrorTypeSystem        = "System"
)

// NewCondition creates a new condition
func NewCondition(conditionType, status, reason, message string) Condition {
	return Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: time.Now(),
	}
}

// NewAppliedCondition creates an Applied condition (required for cls-backend aggregation)
func NewAppliedCondition(status, reason, message string) Condition {
	return NewCondition(ConditionApplied, status, reason, message)
}

// NewAvailableCondition creates an Available condition (required for cls-backend aggregation)
func NewAvailableCondition(status, reason, message string) Condition {
	return NewCondition(ConditionAvailable, status, reason, message)
}

// NewReadyCondition creates a Ready condition (required for cls-backend aggregation)
func NewReadyCondition(status, reason, message string) Condition {
	return NewCondition(ConditionReady, status, reason, message)
}

// NewAppliedTrue creates an Applied=True condition
func NewAppliedTrue(reason, message string) Condition {
	return NewAppliedCondition("True", reason, message)
}

// NewAppliedFalse creates an Applied=False condition
func NewAppliedFalse(reason, message string) Condition {
	return NewAppliedCondition("False", reason, message)
}

// NewAppliedUnknown creates an Applied=Unknown condition
func NewAppliedUnknown(reason, message string) Condition {
	return NewAppliedCondition("Unknown", reason, message)
}

// NewAvailableTrue creates an Available=True condition
func NewAvailableTrue(reason, message string) Condition {
	return NewAvailableCondition("True", reason, message)
}

// NewAvailableFalse creates an Available=False condition
func NewAvailableFalse(reason, message string) Condition {
	return NewAvailableCondition("False", reason, message)
}

// NewAvailableUnknown creates an Available=Unknown condition
func NewAvailableUnknown(reason, message string) Condition {
	return NewAvailableCondition("Unknown", reason, message)
}

// NewReadyTrue creates a Ready=True condition
func NewReadyTrue(reason, message string) Condition {
	return NewReadyCondition("True", reason, message)
}

// NewReadyFalse creates a Ready=False condition
func NewReadyFalse(reason, message string) Condition {
	return NewReadyCondition("False", reason, message)
}

// NewReadyUnknown creates a Ready=Unknown condition
func NewReadyUnknown(reason, message string) Condition {
	return NewReadyCondition("Unknown", reason, message)
}

// NewErrorInfo creates a new error info
func NewErrorInfo(errorType, errorCode, message string, userActionable bool) *ErrorInfo {
	return &ErrorInfo{
		ErrorType:      errorType,
		ErrorCode:      errorCode,
		Message:        message,
		UserActionable: userActionable,
		Timestamp:      time.Now(),
	}
}

// NewStatusUpdate creates a new status update
func NewStatusUpdate(clusterID, controllerName string, observedGeneration int64) *StatusUpdate {
	return &StatusUpdate{
		ClusterID:          clusterID,
		ControllerName:     controllerName,
		ObservedGeneration: observedGeneration,
		Conditions:         []Condition{},
		Metadata:           make(map[string]interface{}),
		Timestamp:          time.Now(),
	}
}

// AddCondition adds a condition to the status update
func (su *StatusUpdate) AddCondition(condition Condition) {
	// Remove existing condition of the same type
	for i, existing := range su.Conditions {
		if existing.Type == condition.Type {
			su.Conditions[i] = condition
			return
		}
	}
	// Add new condition
	su.Conditions = append(su.Conditions, condition)
}

// SetError sets the error information
func (su *StatusUpdate) SetError(errorInfo *ErrorInfo) {
	su.LastError = errorInfo
}

// SetMetadata sets metadata
func (su *StatusUpdate) SetMetadata(key string, value interface{}) {
	if su.Metadata == nil {
		su.Metadata = make(map[string]interface{})
	}
	su.Metadata[key] = value
}

// HasCondition checks if a condition type exists with the given status
func (su *StatusUpdate) HasCondition(conditionType, status string) bool {
	for _, condition := range su.Conditions {
		if condition.Type == conditionType && condition.Status == status {
			return true
		}
	}
	return false
}

// GetCondition returns a condition by type
func (su *StatusUpdate) GetCondition(conditionType string) *Condition {
	for _, condition := range su.Conditions {
		if condition.Type == conditionType {
			return &condition
		}
	}
	return nil
}

// SetApplied sets the Applied condition (required for cls-backend aggregation)
func (su *StatusUpdate) SetApplied(status, reason, message string) {
	su.AddCondition(NewAppliedCondition(status, reason, message))
}

// SetAvailable sets the Available condition (required for cls-backend aggregation)
func (su *StatusUpdate) SetAvailable(status, reason, message string) {
	su.AddCondition(NewAvailableCondition(status, reason, message))
}

// SetReady sets the Ready condition (required for cls-backend aggregation)
func (su *StatusUpdate) SetReady(status, reason, message string) {
	su.AddCondition(NewReadyCondition(status, reason, message))
}

// SetAppliedTrue sets Applied=True condition
func (su *StatusUpdate) SetAppliedTrue(reason, message string) {
	su.AddCondition(NewAppliedTrue(reason, message))
}

// SetAppliedFalse sets Applied=False condition
func (su *StatusUpdate) SetAppliedFalse(reason, message string) {
	su.AddCondition(NewAppliedFalse(reason, message))
}

// SetAvailableTrue sets Available=True condition
func (su *StatusUpdate) SetAvailableTrue(reason, message string) {
	su.AddCondition(NewAvailableTrue(reason, message))
}

// SetAvailableFalse sets Available=False condition
func (su *StatusUpdate) SetAvailableFalse(reason, message string) {
	su.AddCondition(NewAvailableFalse(reason, message))
}

// SetReadyTrue sets Ready=True condition
func (su *StatusUpdate) SetReadyTrue(reason, message string) {
	su.AddCondition(NewReadyTrue(reason, message))
}

// SetReadyFalse sets Ready=False condition
func (su *StatusUpdate) SetReadyFalse(reason, message string) {
	su.AddCondition(NewReadyFalse(reason, message))
}

// HasRequiredConditions checks if all three required conditions are present (Applied, Available, Ready)
func (su *StatusUpdate) HasRequiredConditions() bool {
	hasApplied := false
	hasAvailable := false
	hasReady := false

	for _, condition := range su.Conditions {
		if condition.Type == ConditionApplied {
			hasApplied = true
		}
		if condition.Type == ConditionAvailable {
			hasAvailable = true
		}
		if condition.Type == ConditionReady {
			hasReady = true
		}
	}

	return hasApplied && hasAvailable && hasReady
}
