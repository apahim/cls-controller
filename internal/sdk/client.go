package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"go.uber.org/zap"
	"google.golang.org/api/option"
)

// Client represents the controller SDK client
type Client struct {
	config       Config
	logger       *zap.Logger
	pubsubClient *pubsub.Client
	eventsSub    *pubsub.Subscription

	// Event handling
	eventHandler EventHandler

	// API client for fetching specs and reporting status
	apiClient APIClient

	// Lifecycle management
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
	status string
}

// NewClient creates a new controller SDK client
func NewClient(cfg Config, eventHandler EventHandler) (*Client, error) {
	logger, err := zap.NewProduction()
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// Validate configuration
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	client := &Client{
		config:       cfg,
		logger:       logger.Named("controller-sdk"),
		eventHandler: eventHandler,
		ctx:          ctx,
		cancel:       cancel,
		status:       "initialized",
	}

	// Initialize API client for simplified API
	if cfg.APIBaseURL != "" {
		client.apiClient = NewHTTPAPIClient(cfg.APIBaseURL, cfg.ControllerEmail, cfg.APITimeout, logger)
	}

	// Initialize Pub/Sub client
	if err := client.initPubSub(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize Pub/Sub: %w", err)
	}

	client.logger.Info("Controller SDK client created",
		zap.String("controller_name", cfg.ControllerName),
		zap.String("project_id", cfg.ProjectID),
	)

	return client, nil
}

// Start starts the SDK client
func (c *Client) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.status == "running" {
		return fmt.Errorf("client is already running")
	}

	c.logger.Info("Starting controller SDK client")

	// Start event subscription worker
	c.wg.Add(1)
	go c.eventSubscriptionWorker()

	// Start health check worker
	c.wg.Add(1)
	go c.healthCheckWorker()

	c.status = "running"
	c.logger.Info("Controller SDK client started successfully")
	return nil
}

// Stop stops the SDK client
func (c *Client) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.status != "running" {
		return fmt.Errorf("client is not running")
	}

	c.logger.Info("Stopping controller SDK client")

	// Cancel context to stop all workers
	c.cancel()

	// Wait for all workers to finish
	c.wg.Wait()

	// Close Pub/Sub client
	if c.pubsubClient != nil {
		if err := c.pubsubClient.Close(); err != nil {
			c.logger.Error("Failed to close Pub/Sub client", zap.Error(err))
		}
	}

	c.status = "stopped"
	c.logger.Info("Controller SDK client stopped successfully")
	return nil
}

// ReportStatus reports controller status to the backend via API
func (c *Client) ReportStatus(update *StatusUpdate) error {
	if c.status != "running" {
		return fmt.Errorf("client is not running")
	}

	if c.apiClient == nil {
		return fmt.Errorf("API client is not configured")
	}

	// Set controller name if not provided
	if update.ControllerName == "" {
		update.ControllerName = c.config.ControllerName
	}

	// Set timestamp if not provided
	if update.Timestamp.IsZero() {
		update.Timestamp = time.Now()
	}

	// Route to appropriate API endpoint based on resource type
	ctx, cancel := context.WithTimeout(c.ctx, c.config.APITimeout)
	defer cancel()

	if update.NodePoolID != "" {
		// Report nodepool status
		c.logger.Debug("Reporting nodepool status via API",
			zap.String("nodepool_id", update.NodePoolID),
			zap.String("controller_name", update.ControllerName),
		)

		err := c.apiClient.ReportNodePoolStatus(ctx, update)
		if err != nil {
			c.logger.Error("Failed to report nodepool status via API",
				zap.String("nodepool_id", update.NodePoolID),
				zap.String("controller_name", update.ControllerName),
				zap.Error(err),
			)
			return fmt.Errorf("failed to report nodepool status: %w", err)
		}

		c.logger.Debug("Nodepool status reported successfully via API",
			zap.String("nodepool_id", update.NodePoolID),
			zap.String("controller_name", update.ControllerName),
		)
	} else {
		// Report cluster status
		if update.ClusterID == "" {
			return fmt.Errorf("cluster_id is required for cluster status update")
		}

		c.logger.Debug("Reporting cluster status via API",
			zap.String("cluster_id", update.ClusterID),
			zap.String("controller_name", update.ControllerName),
		)

		err := c.apiClient.ReportClusterStatus(ctx, update)
		if err != nil {
			c.logger.Error("Failed to report cluster status via API",
				zap.String("cluster_id", update.ClusterID),
				zap.String("controller_name", update.ControllerName),
				zap.Error(err),
			)
			return fmt.Errorf("failed to report cluster status: %w", err)
		}

		c.logger.Debug("Cluster status reported successfully via API",
			zap.String("cluster_id", update.ClusterID),
			zap.String("controller_name", update.ControllerName),
		)
	}

	return nil
}

// initPubSub initializes the Pub/Sub client and resources
func (c *Client) initPubSub() error {
	var err error

	// Create Pub/Sub client
	if c.config.PubSubEmulatorHost != "" {
		// Use emulator for local development
		c.logger.Info("Connecting to Pub/Sub emulator",
			zap.String("emulator_host", c.config.PubSubEmulatorHost),
		)
		c.pubsubClient, err = pubsub.NewClient(c.ctx, c.config.ProjectID,
			option.WithEndpoint(c.config.PubSubEmulatorHost),
			option.WithoutAuthentication(),
		)
	} else {
		// Use production Pub/Sub
		c.logger.Info("Connecting to Google Cloud Pub/Sub",
			zap.String("project_id", c.config.ProjectID),
		)
		if c.config.CredentialsFile != "" {
			c.pubsubClient, err = pubsub.NewClient(c.ctx, c.config.ProjectID,
				option.WithCredentialsFile(c.config.CredentialsFile),
			)
		} else {
			c.pubsubClient, err = pubsub.NewClient(c.ctx, c.config.ProjectID)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to create Pub/Sub client: %w", err)
	}

	// Get events subscription
	c.eventsSub = c.pubsubClient.Subscription(c.config.EventsSubscription)
	exists, err := c.eventsSub.Exists(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to check if events subscription exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("events subscription %s does not exist", c.config.EventsSubscription)
	}

	// Configure subscription settings
	c.eventsSub.ReceiveSettings.NumGoroutines = 10
	c.eventsSub.ReceiveSettings.MaxOutstandingMessages = 100

	c.logger.Info("Pub/Sub resources initialized",
		zap.String("events_subscription", c.config.EventsSubscription),
	)

	return nil
}

// eventSubscriptionWorker handles incoming events
func (c *Client) eventSubscriptionWorker() {
	defer c.wg.Done()

	c.logger.Info("Event subscription worker started",
		zap.String("subscription", c.config.EventsSubscription),
	)

	err := c.eventsSub.Receive(c.ctx, func(ctx context.Context, msg *pubsub.Message) {
		start := time.Now()

		// Handle the event
		err := c.handleEventMessage(ctx, msg)
		duration := time.Since(start)

		if err != nil {
			c.logger.Error("Event handler failed",
				zap.String("message_id", msg.ID),
				zap.Duration("duration", duration),
				zap.Error(err),
			)
			msg.Nack()
			return
		}

		c.logger.Debug("Event processed successfully",
			zap.String("message_id", msg.ID),
			zap.Duration("duration", duration),
		)
		msg.Ack()
	})

	if err != nil {
		c.logger.Error("Event subscription failed", zap.Error(err))
	}

	c.logger.Info("Event subscription worker stopped")
}

// handleEventMessage handles a single event message
func (c *Client) handleEventMessage(ctx context.Context, msg *pubsub.Message) error {
	eventType := msg.Attributes["event_type"]
	if eventType == "" {
		return fmt.Errorf("missing event_type attribute")
	}

	c.logger.Debug("Handling event",
		zap.String("event_type", eventType),
		zap.String("message_id", msg.ID),
	)

	switch eventType {
	case EventTypeClusterCreated, EventTypeClusterUpdated, EventTypeClusterDeleted, EventTypeClusterReconcile:
		return c.handleClusterEvent(ctx, msg.Data, eventType)
	case EventTypeNodePoolCreated, EventTypeNodePoolUpdated, EventTypeNodePoolDeleted, EventTypeNodePoolReconcile:
		return c.handleNodePoolEvent(ctx, msg.Data, eventType)
	case EventTypeControllerStart, EventTypeControllerStop:
		return c.handleControllerEvent(ctx, msg.Data, eventType)
	default:
		c.logger.Warn("Unknown event type", zap.String("event_type", eventType))
		return nil // Don't fail on unknown events
	}
}

// handleClusterEvent handles cluster events
func (c *Client) handleClusterEvent(ctx context.Context, data []byte, eventType string) error {
	var event ClusterEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("failed to unmarshal cluster event: %w", err)
	}

	return c.eventHandler.HandleClusterEvent(&event)
}

// handleNodePoolEvent handles nodepool events
func (c *Client) handleNodePoolEvent(ctx context.Context, data []byte, eventType string) error {
	var event NodePoolEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("failed to unmarshal nodepool event: %w", err)
	}

	return c.eventHandler.HandleNodePoolEvent(&event)
}

// handleControllerEvent handles controller events
func (c *Client) handleControllerEvent(ctx context.Context, data []byte, eventType string) error {
	var event ControllerEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("failed to unmarshal controller event: %w", err)
	}

	return c.eventHandler.HandleControllerEvent(&event)
}

// healthCheckWorker performs periodic health checks
func (c *Client) healthCheckWorker() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.config.HealthCheckInterval)
	defer ticker.Stop()

	c.logger.Info("Health check worker started",
		zap.Duration("interval", c.config.HealthCheckInterval),
	)

	for {
		select {
		case <-c.ctx.Done():
			c.logger.Info("Health check worker stopped")
			return
		case <-ticker.C:
			c.performHealthCheck()
		}
	}
}

// performHealthCheck performs a health check
func (c *Client) performHealthCheck() {
	c.logger.Debug("Performing health check")

	// Check if Pub/Sub client is healthy by trying to list topics
	it := c.pubsubClient.Topics(c.ctx)
	_, err := it.Next()
	if err != nil && err.Error() != "no more items in iterator" {
		c.logger.Error("Health check failed: Pub/Sub client unhealthy", zap.Error(err))
		return
	}

	c.logger.Debug("Health check passed")
}

// GetStatus returns the current client status
func (c *Client) GetStatus() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// IsRunning returns true if the client is running
func (c *Client) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status == "running"
}

// GetAPIClient returns the API client for fetching specs and reporting status
func (c *Client) GetAPIClient() APIClient {
	return c.apiClient
}

// validateConfig validates the SDK configuration
func validateConfig(cfg Config) error {
	if cfg.ProjectID == "" {
		return fmt.Errorf("project_id is required")
	}
	if cfg.ControllerName == "" {
		return fmt.Errorf("controller_name is required")
	}
	if cfg.EventsSubscription == "" {
		return fmt.Errorf("events_subscription is required")
	}
	if cfg.RetryAttempts < 0 {
		return fmt.Errorf("retry_attempts must be non-negative")
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 5 * time.Second
	}
	if cfg.HealthCheckInterval <= 0 {
		cfg.HealthCheckInterval = 60 * time.Second
	}
	if cfg.APITimeout <= 0 {
		cfg.APITimeout = 30 * time.Second
	}

	// Validate API configuration is required for status reporting
	if cfg.APIBaseURL == "" {
		return fmt.Errorf("api_base_url is required for status reporting")
	}

	// Controller email validation
	if cfg.ControllerEmail == "" {
		cfg.ControllerEmail = "controller@system.local"
	}

	return nil
}
