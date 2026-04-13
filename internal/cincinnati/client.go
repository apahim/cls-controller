package cincinnati

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://api.openshift.com/api/upgrades_info/v1/graph"
	DefaultArch    = "amd64"
)

// Node represents a single version node in the Cincinnati graph.
type Node struct {
	Version string `json:"version"`
	Payload string `json:"payload"`
}

// Graph represents the Cincinnati update graph response.
type Graph struct {
	Nodes []Node    `json:"nodes"`
	Edges [][]int64 `json:"edges"`
}

// Client queries the Cincinnati update service to resolve OCP versions to release images.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Cincinnati client.
func NewClient(baseURL string, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// ResolveVersion queries Cincinnati for the given version and returns the release image pullspec
// and the derived channel name. It derives the channel from the channelGroup and the major.minor
// of the version.
// For example: version "4.22.0-ec.4" with channelGroup "candidate" queries channel "candidate-4.22".
func (c *Client) ResolveVersion(ctx context.Context, version, channelGroup, arch string) (image string, channel string, err error) {
	if version == "" {
		return "", "", fmt.Errorf("version is required")
	}
	if channelGroup == "" {
		channelGroup = "stable"
	}
	if arch == "" {
		arch = DefaultArch
	}

	channel, err = deriveChannel(version, channelGroup)
	if err != nil {
		return "", "", err
	}

	graph, err := c.fetchGraph(ctx, channel, arch)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch Cincinnati graph for channel %s: %w", channel, err)
	}

	for _, node := range graph.Nodes {
		if node.Version == version {
			if node.Payload == "" {
				return "", "", fmt.Errorf("version %s found in channel %s but has no payload image", version, channel)
			}
			return node.Payload, channel, nil
		}
	}

	return "", "", fmt.Errorf("version %s not found in channel %s", version, channel)
}

// deriveChannel derives the Cincinnati channel name from a version and channel group.
// e.g., version "4.22.0-ec.4", channelGroup "candidate" → "candidate-4.22"
func deriveChannel(version, channelGroup string) (string, error) {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid version format %q: expected at least major.minor", version)
	}
	return fmt.Sprintf("%s-%s.%s", channelGroup, parts[0], parts[1]), nil
}

// fetchGraph fetches the Cincinnati update graph for the given channel and architecture.
func (c *Client) fetchGraph(ctx context.Context, channel, arch string) (*Graph, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	q := req.URL.Query()
	q.Set("channel", channel)
	q.Set("arch", arch)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Cincinnati returned status %d: %s", resp.StatusCode, string(body))
	}

	var graph Graph
	if err := json.NewDecoder(resp.Body).Decode(&graph); err != nil {
		return nil, fmt.Errorf("failed to decode Cincinnati response: %w", err)
	}

	return &graph, nil
}
