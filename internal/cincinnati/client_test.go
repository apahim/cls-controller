package cincinnati

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveChannel(t *testing.T) {
	tests := []struct {
		name         string
		version      string
		channelGroup string
		expected     string
		expectErr    bool
	}{
		{
			name:         "candidate channel",
			version:      "4.22.0-ec.4",
			channelGroup: "candidate",
			expected:     "candidate-4.22",
		},
		{
			name:         "stable channel",
			version:      "4.22.0",
			channelGroup: "stable",
			expected:     "stable-4.22",
		},
		{
			name:         "fast channel",
			version:      "4.22.1",
			channelGroup: "fast",
			expected:     "fast-4.22",
		},
		{
			name:         "invalid version - no minor",
			version:      "4",
			channelGroup: "stable",
			expectErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := deriveChannel(tt.version, tt.channelGroup)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestResolveVersion(t *testing.T) {
	graph := Graph{
		Nodes: []Node{
			{Version: "4.22.0-ec.4", Payload: "quay.io/openshift-release-dev/ocp-release@sha256:abc123"},
			{Version: "4.22.0-ec.3", Payload: "quay.io/openshift-release-dev/ocp-release@sha256:def456"},
		},
		Edges: [][]int64{{0, 1}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "candidate-4.22", r.URL.Query().Get("channel"))
		assert.Equal(t, "amd64", r.URL.Query().Get("arch"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graph)
	}))
	defer server.Close()

	client := NewClient(server.URL, 5*time.Second)

	image, channel, err := client.ResolveVersion(context.Background(), "4.22.0-ec.4", "candidate", "amd64")
	require.NoError(t, err)
	assert.Equal(t, "quay.io/openshift-release-dev/ocp-release@sha256:abc123", image)
	assert.Equal(t, "candidate-4.22", channel)
}

func TestResolveVersion_NotFound(t *testing.T) {
	graph := Graph{
		Nodes: []Node{
			{Version: "4.22.0-ec.3", Payload: "quay.io/openshift-release-dev/ocp-release@sha256:def456"},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graph)
	}))
	defer server.Close()

	client := NewClient(server.URL, 5*time.Second)

	_, _, err := client.ResolveVersion(context.Background(), "4.22.0-ec.4", "candidate", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveVersion_EmptyVersion(t *testing.T) {
	client := NewClient("http://unused", 5*time.Second)
	_, _, err := client.ResolveVersion(context.Background(), "", "candidate", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version is required")
}

func TestResolveVersion_DefaultChannelGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "stable-4.22", r.URL.Query().Get("channel"))
		graph := Graph{
			Nodes: []Node{
				{Version: "4.22.0", Payload: "quay.io/openshift-release-dev/ocp-release@sha256:stable123"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graph)
	}))
	defer server.Close()

	client := NewClient(server.URL, 5*time.Second)

	image, channel, err := client.ResolveVersion(context.Background(), "4.22.0", "", "")
	require.NoError(t, err)
	assert.Equal(t, "quay.io/openshift-release-dev/ocp-release@sha256:stable123", image)
	assert.Equal(t, "stable-4.22", channel)
}

func TestResolveVersion_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewClient(server.URL, 5*time.Second)

	_, _, err := client.ResolveVersion(context.Background(), "4.22.0", "stable", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
}
