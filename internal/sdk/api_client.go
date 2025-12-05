package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// HTTPAPIClient implements APIClient interface using HTTP REST calls for the new simplified API
type HTTPAPIClient struct {
	baseURL         string
	controllerEmail string // Email to use for X-User-Email header
	httpClient      *http.Client
	logger          *zap.Logger
}

// NewHTTPAPIClient creates a new HTTP-based API client for the simplified API
func NewHTTPAPIClient(baseURL string, controllerEmail string, timeout time.Duration, logger *zap.Logger) *HTTPAPIClient {
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	if controllerEmail == "" {
		controllerEmail = "controller@system.local"
	}

	return &HTTPAPIClient{
		baseURL:         baseURL,
		controllerEmail: controllerEmail,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		logger: logger.Named("api-client"),
	}
}

// addAuthHeaders adds authentication headers to the request
func (c *HTTPAPIClient) addAuthHeaders(req *http.Request) {
	req.Header.Set("X-User-Email", c.controllerEmail)
	req.Header.Set("Accept", "application/json")
}

// GetCluster fetches cluster spec from the simplified API
func (c *HTTPAPIClient) GetCluster(ctx context.Context, clusterID string) (*Cluster, error) {
	url := fmt.Sprintf("%s/clusters/%s", c.baseURL, clusterID)

	c.logger.Debug("Fetching cluster from API",
		zap.String("cluster_id", clusterID),
		zap.String("url", url),
	)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.addAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch cluster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("cluster %s not found", clusterID)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var clusterResponse struct {
		*Cluster
		Status map[string]interface{} `json:"status,omitempty"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if err := json.Unmarshal(body, &clusterResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cluster response: %w", err)
	}

	c.logger.Debug("Successfully fetched cluster from API",
		zap.String("cluster_id", clusterID),
		zap.String("cluster_name", clusterResponse.Name),
		zap.Int64("generation", clusterResponse.Generation),
	)

	return clusterResponse.Cluster, nil
}

// GetNodePool fetches nodepool spec from the simplified API
func (c *HTTPAPIClient) GetNodePool(ctx context.Context, nodepoolID string) (*NodePool, error) {
	url := fmt.Sprintf("%s/nodepools/%s", c.baseURL, nodepoolID)

	c.logger.Debug("Fetching nodepool from API",
		zap.String("nodepool_id", nodepoolID),
		zap.String("url", url),
	)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.addAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch nodepool: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("nodepool %s not found", nodepoolID)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var nodepool NodePool
	if err := json.NewDecoder(resp.Body).Decode(&nodepool); err != nil {
		return nil, fmt.Errorf("failed to unmarshal nodepool response: %w", err)
	}

	c.logger.Debug("Successfully fetched nodepool from API",
		zap.String("nodepool_id", nodepoolID),
		zap.String("nodepool_name", nodepool.Name),
		zap.Int64("generation", nodepool.Generation),
	)

	return &nodepool, nil
}

// ReportClusterStatus reports cluster status via simplified API
func (c *HTTPAPIClient) ReportClusterStatus(ctx context.Context, update *StatusUpdate) error {
	if update.ClusterID == "" {
		return fmt.Errorf("cluster_id is required for cluster status update")
	}

	url := fmt.Sprintf("%s/clusters/%s/status", c.baseURL, update.ClusterID)

	c.logger.Debug("Reporting cluster status to API",
		zap.String("cluster_id", update.ClusterID),
		zap.String("controller_name", update.ControllerName),
		zap.Int64("observed_generation", update.ObservedGeneration),
		zap.String("url", url),
	)

	// Convert StatusUpdate to the format expected by cls-backend
	statusPayload := map[string]interface{}{
		"cluster_id":          update.ClusterID,
		"controller_name":     update.ControllerName,
		"observed_generation": update.ObservedGeneration,
		"conditions":          update.Conditions,
		"metadata":            update.Metadata,
		"last_error":          update.LastError,
		"updated_at":          update.Timestamp,
	}

	jsonData, err := json.Marshal(statusPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal status update: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	c.addAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to report cluster status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status report failed with status %d: %s", resp.StatusCode, string(body))
	}

	c.logger.Debug("Successfully reported cluster status to API",
		zap.String("cluster_id", update.ClusterID),
		zap.String("controller_name", update.ControllerName),
	)

	return nil
}

// ReportNodePoolStatus reports nodepool status via simplified API
func (c *HTTPAPIClient) ReportNodePoolStatus(ctx context.Context, update *StatusUpdate) error {
	if update.NodePoolID == "" {
		return fmt.Errorf("nodepool_id is required for nodepool status update")
	}

	url := fmt.Sprintf("%s/nodepools/%s/status", c.baseURL, update.NodePoolID)

	c.logger.Debug("Reporting nodepool status to API",
		zap.String("nodepool_id", update.NodePoolID),
		zap.String("controller_name", update.ControllerName),
		zap.Int64("observed_generation", update.ObservedGeneration),
		zap.String("url", url),
	)

	// Convert StatusUpdate to the format expected by cls-backend
	statusPayload := map[string]interface{}{
		"nodepool_id":         update.NodePoolID,
		"controller_name":     update.ControllerName,
		"observed_generation": update.ObservedGeneration,
		"conditions":          update.Conditions,
		"metadata":            update.Metadata,
		"last_error":          update.LastError,
		"updated_at":          update.Timestamp,
	}

	jsonData, err := json.Marshal(statusPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal status update: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	c.addAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to report nodepool status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status report failed with status %d: %s", resp.StatusCode, string(body))
	}

	c.logger.Debug("Successfully reported nodepool status to API",
		zap.String("nodepool_id", update.NodePoolID),
		zap.String("controller_name", update.ControllerName),
	)

	return nil
}
