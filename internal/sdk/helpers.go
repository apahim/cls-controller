package sdk

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// ControllerBase provides a base implementation for controllers
type ControllerBase struct {
	client       *Client
	logger       *zap.Logger
	name         string
	clusterID    string
	nodePoolID   string
	generation   int64
	retryAttempts int
	retryBackoff  time.Duration
}

// NewControllerBase creates a new controller base
func NewControllerBase(client *Client, controllerName, clusterID string) *ControllerBase {
	logger, _ := zap.NewProduction()

	return &ControllerBase{
		client:       client,
		logger:       logger.Named(controllerName),
		name:         controllerName,
		clusterID:    clusterID,
		generation:   1,
		retryAttempts: 3,
		retryBackoff:  5 * time.Second,
	}
}

// SetNodePoolID sets the nodepool ID for nodepool controllers
func (cb *ControllerBase) SetNodePoolID(nodePoolID string) {
	cb.nodePoolID = nodePoolID
}

// SetGeneration sets the observed generation
func (cb *ControllerBase) SetGeneration(generation int64) {
	cb.generation = generation
}

// ReportProgress reports that the controller is making progress
func (cb *ControllerBase) ReportProgress(message string, metadata map[string]interface{}) error {
	update := NewStatusUpdate(cb.clusterID, cb.name, cb.generation)
	update.NodePoolID = cb.nodePoolID

	update.AddCondition(NewCondition(
		ConditionProgressing,
		"True",
		"InProgress",
		message,
	))

	if metadata != nil {
		for k, v := range metadata {
			update.SetMetadata(k, v)
		}
	}

	return cb.client.ReportStatus(update)
}

// ReportReady reports that the controller has completed successfully
func (cb *ControllerBase) ReportReady(message string, metadata map[string]interface{}) error {
	update := NewStatusUpdate(cb.clusterID, cb.name, cb.generation)
	update.NodePoolID = cb.nodePoolID

	update.AddCondition(NewCondition(
		ConditionAvailable,
		"True",
		"Ready",
		message,
	))

	update.AddCondition(NewCondition(
		ConditionReady,
		"True",
		"Ready",
		message,
	))

	if metadata != nil {
		for k, v := range metadata {
			update.SetMetadata(k, v)
		}
	}

	return cb.client.ReportStatus(update)
}

// ReportError reports an error condition
func (cb *ControllerBase) ReportError(errorType, message string, userActionable bool, metadata map[string]interface{}) error {
	update := NewStatusUpdate(cb.clusterID, cb.name, cb.generation)
	update.NodePoolID = cb.nodePoolID

	// Add error condition
	update.AddCondition(NewCondition(
		ConditionAvailable,
		"False",
		"Error",
		message,
	))

	// Set error information
	errorInfo := NewErrorInfo(errorType, "", message, userActionable)
	update.SetError(errorInfo)

	if metadata != nil {
		for k, v := range metadata {
			update.SetMetadata(k, v)
		}
	}

	return cb.client.ReportStatus(update)
}

// ReportDegraded reports a degraded condition
func (cb *ControllerBase) ReportDegraded(reason, message string, metadata map[string]interface{}) error {
	update := NewStatusUpdate(cb.clusterID, cb.name, cb.generation)
	update.NodePoolID = cb.nodePoolID

	update.AddCondition(NewCondition(
		ConditionAvailable,
		"True",
		reason,
		message,
	))

	update.AddCondition(NewCondition(
		ConditionDegraded,
		"True",
		reason,
		message,
	))

	if metadata != nil {
		for k, v := range metadata {
			update.SetMetadata(k, v)
		}
	}

	return cb.client.ReportStatus(update)
}

// WithRetry executes a function with retry logic
func (cb *ControllerBase) WithRetry(ctx context.Context, operation string, fn func() error) error {
	var lastErr error

	for attempt := 0; attempt <= cb.retryAttempts; attempt++ {
		if attempt > 0 {
			// Report retry attempt
			cb.logger.Info("Retrying operation",
				zap.String("operation", operation),
				zap.Int("attempt", attempt),
				zap.Int("max_attempts", cb.retryAttempts),
			)

			// Wait before retry
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cb.retryBackoff):
			}
		}

		lastErr = fn()
		if lastErr == nil {
			if attempt > 0 {
				cb.logger.Info("Operation succeeded after retry",
					zap.String("operation", operation),
					zap.Int("attempts", attempt+1),
				)
			}
			return nil
		}

		cb.logger.Warn("Operation failed",
			zap.String("operation", operation),
			zap.Int("attempt", attempt+1),
			zap.Error(lastErr),
		)
	}

	return fmt.Errorf("operation %s failed after %d attempts: %w", operation, cb.retryAttempts+1, lastErr)
}

// ProgressReporter helps with reporting incremental progress
type ProgressReporter struct {
	base       *ControllerBase
	totalSteps int
	completed  int
	stepNames  []string
}

// NewProgressReporter creates a new progress reporter
func (cb *ControllerBase) NewProgressReporter(steps []string) *ProgressReporter {
	return &ProgressReporter{
		base:       cb,
		totalSteps: len(steps),
		completed:  0,
		stepNames:  steps,
	}
}

// CompleteStep marks a step as completed and reports progress
func (pr *ProgressReporter) CompleteStep(stepIndex int, metadata map[string]interface{}) error {
	if stepIndex >= len(pr.stepNames) {
		return fmt.Errorf("invalid step index: %d", stepIndex)
	}

	pr.completed = stepIndex + 1

	stepName := pr.stepNames[stepIndex]
	message := fmt.Sprintf("Completed step %d/%d: %s", pr.completed, pr.totalSteps, stepName)

	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["progress_step"] = stepIndex + 1
	metadata["progress_total"] = pr.totalSteps
	metadata["progress_percentage"] = (float64(pr.completed) / float64(pr.totalSteps)) * 100
	metadata["current_step"] = stepName

	return pr.base.ReportProgress(message, metadata)
}

// Complete marks all steps as completed
func (pr *ProgressReporter) Complete(message string, metadata map[string]interface{}) error {
	pr.completed = pr.totalSteps

	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["progress_step"] = pr.totalSteps
	metadata["progress_total"] = pr.totalSteps
	metadata["progress_percentage"] = 100.0

	return pr.base.ReportReady(message, metadata)
}

// EventHandlerBase provides a base implementation for event handlers
type EventHandlerBase struct {
	logger *zap.Logger
}

// NewEventHandlerBase creates a new event handler base
func NewEventHandlerBase(controllerName string) *EventHandlerBase {
	logger, _ := zap.NewProduction()
	return &EventHandlerBase{
		logger: logger.Named(controllerName + "-events"),
	}
}

// HandleClusterEvent provides a default implementation (no-op)
func (ehb *EventHandlerBase) HandleClusterEvent(event *ClusterEvent) error {
	ehb.logger.Debug("Received cluster event",
		zap.String("event_type", event.Type),
		zap.String("cluster_id", event.ClusterID),
	)
	return nil
}

// HandleNodePoolEvent provides a default implementation (no-op)
func (ehb *EventHandlerBase) HandleNodePoolEvent(event *NodePoolEvent) error {
	ehb.logger.Debug("Received nodepool event",
		zap.String("event_type", event.Type),
		zap.String("cluster_id", event.ClusterID),
		zap.String("nodepool_id", event.NodePoolID),
	)
	return nil
}

// HandleControllerEvent provides a default implementation (no-op)
func (ehb *EventHandlerBase) HandleControllerEvent(event *ControllerEvent) error {
	ehb.logger.Debug("Received controller event",
		zap.String("event_type", event.Type),
		zap.String("controller_name", event.ControllerName),
		zap.String("cluster_id", event.ClusterID),
		zap.String("action", event.Action),
	)
	return nil
}

// ConfigLoader helps load configuration from environment variables
type ConfigLoader struct{}

// LoadFromEnv loads SDK configuration from environment variables
func (cl *ConfigLoader) LoadFromEnv() Config {
	cfg := Config{
		ProjectID:           getEnvWithDefault("GOOGLE_CLOUD_PROJECT", ""),
		ControllerName:      getEnvWithDefault("CONTROLLER_NAME", ""),
		StatusTopic:         getEnvWithDefault("PUBSUB_STATUS_TOPIC", "status-updates"),
		EventsSubscription:  getEnvWithDefault("PUBSUB_EVENTS_SUBSCRIPTION", ""),
		PubSubEmulatorHost:  getEnvWithDefault("PUBSUB_EMULATOR_HOST", ""),
		CredentialsFile:     getEnvWithDefault("GOOGLE_APPLICATION_CREDENTIALS", ""),
		RetryAttempts:       getIntEnvWithDefault("RETRY_ATTEMPTS", 3),
		RetryBackoff:        getDurationEnvWithDefault("RETRY_BACKOFF", 5*time.Second),
		HealthCheckInterval: getDurationEnvWithDefault("HEALTH_CHECK_INTERVAL", 60*time.Second),
		APIBaseURL:          getEnvWithDefault("CLS_BACKEND_API_URL", "http://localhost:8080/api/v1"),
		APITimeout:          getDurationEnvWithDefault("API_TIMEOUT", 30*time.Second),

		// Controller authentication configuration
		ControllerEmail: getEnvWithDefault("CONTROLLER_EMAIL", "controller@system.local"),
	}

	return cfg
}

// Helper functions

func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getIntEnvWithDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func getDurationEnvWithDefault(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}

func getBoolEnvWithDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolVal, err := strconv.ParseBool(value); err == nil {
			return boolVal
		}
	}
	return defaultValue
}