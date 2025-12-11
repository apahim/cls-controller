package template

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/apahim/cls-controller/internal/crd"
	"github.com/apahim/cls-controller/internal/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPreprocessTemplate(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	engine := NewEngine(0, logger)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "hyphenated resource name",
			input:    "{{- if .resources.pull-secret -}}",
			expected: `{{- if (index .resources "pull-secret") -}}`,
		},
		{
			name:     "multiple hyphenated resources",
			input:    "{{- if and .resources.namespace .resources.pull-secret .resources.hostedcluster -}}",
			expected: `{{- if and .resources.namespace (index .resources "pull-secret") .resources.hostedcluster -}}`,
		},
		{
			name:     "nested access with hyphens",
			input:    "{{ .resources.pull-secret.status.conditions }}",
			expected: `{{ (index .resources "pull-secret").status.conditions }}`,
		},
		{
			name:     "no hyphens - should not change",
			input:    "{{ .resources.namespace }}",
			expected: "{{ .resources.namespace }}",
		},
		{
			name:     "complex template from Applied condition",
			input:    `{{- if and .resources.namespace .resources.pull-secret .resources.hostedcluster -}} True {{- else -}} False {{- end }}`,
			expected: `{{- if and .resources.namespace (index .resources "pull-secret") .resources.hostedcluster -}} True {{- else -}} False {{- end }}`,
		},
		{
			name:     "multi-hyphen resource name",
			input:    "{{ .resources.my-cool-resource-name }}",
			expected: `{{ (index .resources "my-cool-resource-name") }}`,
		},
		{
			name:     "resource name with underscore and hyphen",
			input:    "{{ .resources.pull_secret-v2 }}",
			expected: `{{ (index .resources "pull_secret-v2") }}`,
		},
		{
			name:     "message template with multiple references",
			input:    `Waiting for resources (namespace: {{ if .resources.namespace }}✓{{ else }}✗{{ end }}, pull-secret: {{ if .resources.pull-secret }}✓{{ else }}✗{{ end }})`,
			expected: `Waiting for resources (namespace: {{ if .resources.namespace }}✓{{ else }}✗{{ end }}, pull-secret: {{ if (index .resources "pull-secret") }}✓{{ else }}✗{{ end }})`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := engine.preprocessTemplate(tt.input)
			assert.Equal(t, tt.expected, result, "Template preprocessing mismatch")
		})
	}
}

func TestPreprocessTemplateEdgeCases(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	engine := NewEngine(0, logger)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "no resources reference",
			input:    "{{ .cluster.id }}",
			expected: "{{ .cluster.id }}",
		},
		{
			name:     "resources but not in correct pattern",
			input:    "{{ .other.pull-secret }}",
			expected: "{{ .other.pull-secret }}",
		},
		{
			name:     "hyphen at end",
			input:    "{{ .resources.resource- }}",
			expected: `{{ (index .resources "resource-") }}`, // Preprocessor handles this edge case
		},
		{
			name:     "hyphen at start",
			input:    "{{ .resources.-resource }}",
			expected: `{{ (index .resources "-resource") }}`, // Preprocessor handles this edge case
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := engine.preprocessTemplate(tt.input)
			assert.Equal(t, tt.expected, result, "Template preprocessing mismatch")
		})
	}
}

func TestBuildNodePoolContext(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	engine := NewEngine(time.Second*30, logger)

	spec := map[string]interface{}{
		"replicas": 3,
		"platform": map[string]interface{}{
			"gcp": map[string]interface{}{
				"machineType": "n2-standard-4",
				"zone":        "us-central1-a",
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

	ctx := engine.buildNodePoolContext(nodepool)

	assert.Equal(t, "np-123", ctx["id"])
	assert.Equal(t, "cluster-456", ctx["cluster_id"])
	assert.Equal(t, "worker-pool", ctx["name"])
	assert.Equal(t, int64(2), ctx["generation"])

	// Verify spec was parsed
	parsedSpec, ok := ctx["spec"].(map[string]interface{})
	require.True(t, ok, "spec should be a map")
	assert.Equal(t, float64(3), parsedSpec["replicas"])

	platform, ok := parsedSpec["platform"].(map[string]interface{})
	require.True(t, ok, "platform should be a map")
	gcp, ok := platform["gcp"].(map[string]interface{})
	require.True(t, ok, "gcp should be a map")
	assert.Equal(t, "n2-standard-4", gcp["machineType"])
}

func TestBuildNodePoolTemplateContext(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	engine := NewEngine(time.Second*30, logger)

	specJSON, _ := json.Marshal(map[string]interface{}{"replicas": 2})
	nodepool := &sdk.NodePool{
		ID:         "np-123",
		ClusterID:  "cluster-456",
		Name:       "test-pool",
		Generation: 1,
		Spec:       specJSON,
	}

	clusterSpecJSON, _ := json.Marshal(map[string]interface{}{"version": "4.14"})
	cluster := &sdk.Cluster{
		ID:         "cluster-456",
		Name:       "test-cluster",
		Generation: 1,
		Spec:       clusterSpecJSON,
	}

	// Create mock resources
	resources := map[string]*unstructured.Unstructured{
		"nodepool": {
			Object: map[string]interface{}{
				"apiVersion": "hypershift.openshift.io/v1beta1",
				"kind":       "NodePool",
				"metadata": map[string]interface{}{
					"name":      "test-pool",
					"namespace": "clusters-cluster-456",
				},
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{
							"type":   "Ready",
							"status": "True",
						},
					},
				},
			},
		},
	}

	ctx := engine.buildNodePoolTemplateContext(nodepool, cluster, resources)

	// Verify nodepool context
	nodepoolCtx, ok := ctx["nodepool"].(map[string]interface{})
	require.True(t, ok, "nodepool should be present in context")
	assert.Equal(t, "np-123", nodepoolCtx["id"])
	assert.Equal(t, "cluster-456", nodepoolCtx["cluster_id"])
	assert.Equal(t, "test-pool", nodepoolCtx["name"])

	// Verify cluster context
	clusterCtx, ok := ctx["cluster"].(map[string]interface{})
	require.True(t, ok, "cluster should be present in context")
	assert.Equal(t, "cluster-456", clusterCtx["id"])
	assert.Equal(t, "test-cluster", clusterCtx["name"])

	// Verify resources context
	resourcesCtx, ok := ctx["resources"].(map[string]interface{})
	require.True(t, ok, "resources should be present in context")
	assert.Contains(t, resourcesCtx, "nodepool")

	// Verify controller context
	controllerCtx, ok := ctx["controller"].(map[string]interface{})
	require.True(t, ok, "controller should be present in context")
	assert.Equal(t, "cls-controller", controllerCtx["name"])

	// Verify timestamp
	timestamp, ok := ctx["timestamp"].(int64)
	require.True(t, ok, "timestamp should be present")
	assert.Greater(t, timestamp, int64(0))
}

func TestRenderNodePoolResource(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	engine := NewEngine(time.Second*30, logger)

	// Create a simple ControllerConfig with a nodepool resource template
	config := &crd.ControllerConfig{
		Spec: crd.ControllerConfigSpec{
			Name: "test-controller",
			Resources: []crd.ResourceConfig{
				{
					Name: "nodepool",
					Template: `apiVersion: hypershift.openshift.io/v1beta1
kind: NodePool
metadata:
  name: {{ .nodepool.name }}
  namespace: clusters-{{ .nodepool.cluster_id }}
spec:
  clusterName: {{ .nodepool.name }}
  replicas: {{ .nodepool.spec.replicas }}`,
				},
			},
		},
	}

	err := engine.CompileTemplates(config)
	require.NoError(t, err)

	spec := map[string]interface{}{
		"replicas": 3,
	}
	specJSON, _ := json.Marshal(spec)

	nodepool := &sdk.NodePool{
		ID:         "np-123",
		ClusterID:  "cluster-456",
		Name:       "worker-pool",
		Generation: 1,
		Spec:       specJSON,
	}

	clusterSpecJSON, _ := json.Marshal(map[string]interface{}{"version": "4.14"})
	cluster := &sdk.Cluster{
		ID:         "cluster-456",
		Name:       "test-cluster",
		Generation: 1,
		Spec:       clusterSpecJSON,
	}

	rendered, err := engine.RenderNodePoolResource("nodepool", nodepool, cluster, nil)
	require.NoError(t, err)
	require.NotNil(t, rendered)

	assert.Equal(t, "NodePool", rendered.GetKind())
	assert.Equal(t, "worker-pool", rendered.GetName())
	assert.Equal(t, "clusters-cluster-456", rendered.GetNamespace())

	// Verify spec - check replicas exists (type may be int64 or float64 depending on template rendering)
	replicasVal, found, err := unstructured.NestedFieldNoCopy(rendered.Object, "spec", "replicas")
	require.NoError(t, err)
	require.True(t, found)
	// Template rendered values may be int64 or float64
	switch v := replicasVal.(type) {
	case int64:
		assert.Equal(t, int64(3), v)
	case float64:
		assert.Equal(t, float64(3), v)
	default:
		t.Fatalf("unexpected type for replicas: %T", replicasVal)
	}
}

func TestRenderNodePoolStatusCondition(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	engine := NewEngine(time.Second*30, logger)

	// Create a ControllerConfig with status conditions
	config := &crd.ControllerConfig{
		Spec: crd.ControllerConfigSpec{
			Name: "test-controller",
			StatusConditions: []crd.StatusConditionConfig{
				{
					Name:    "Applied",
					Status:  `{{- if .resources.nodepool -}}True{{- else -}}False{{- end }}`,
					Reason:  `{{- if .resources.nodepool -}}ResourceCreated{{- else -}}ResourceNotCreated{{- end }}`,
					Message: `{{- if .resources.nodepool -}}NodePool {{ .nodepool.name }} created{{- else -}}Waiting for NodePool{{- end }}`,
				},
				{
					Name:    "Ready",
					Status:  `{{- if .resources.nodepool -}}{{- range .resources.nodepool.status.conditions -}}{{- if eq .type "Ready" -}}{{ .status }}{{- end -}}{{- end -}}{{- else -}}Unknown{{- end }}`,
					Reason:  `{{- if .resources.nodepool -}}ConditionFound{{- else -}}ResourceNotFound{{- end }}`,
					Message: `NodePool readiness status`,
				},
			},
		},
	}

	err := engine.CompileTemplates(config)
	require.NoError(t, err)

	specJSON, _ := json.Marshal(map[string]interface{}{"replicas": 2})
	nodepool := &sdk.NodePool{
		ID:         "np-123",
		ClusterID:  "cluster-456",
		Name:       "test-pool",
		Generation: 1,
		Spec:       specJSON,
	}

	clusterSpecJSON, _ := json.Marshal(map[string]interface{}{"version": "4.14"})
	cluster := &sdk.Cluster{
		ID:         "cluster-456",
		Name:       "test-cluster",
		Generation: 1,
		Spec:       clusterSpecJSON,
	}

	// Test with nodepool resource present
	resources := map[string]*unstructured.Unstructured{
		"nodepool": {
			Object: map[string]interface{}{
				"apiVersion": "hypershift.openshift.io/v1beta1",
				"kind":       "NodePool",
				"metadata": map[string]interface{}{
					"name": "test-pool",
				},
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{
							"type":   "Ready",
							"status": "True",
						},
					},
				},
			},
		},
	}

	status, reason, message, err := engine.RenderNodePoolStatusCondition("Applied", nodepool, cluster, resources)
	require.NoError(t, err)
	assert.Equal(t, "True", status)
	assert.Equal(t, "ResourceCreated", reason)
	assert.Equal(t, "NodePool test-pool created", message)

	// Test Ready condition
	status, reason, message, err = engine.RenderNodePoolStatusCondition("Ready", nodepool, cluster, resources)
	require.NoError(t, err)
	assert.Equal(t, "True", status)
	assert.Equal(t, "ConditionFound", reason)

	// Test without resources
	status, reason, message, err = engine.RenderNodePoolStatusCondition("Applied", nodepool, cluster, nil)
	require.NoError(t, err)
	assert.Equal(t, "False", status)
	assert.Equal(t, "ResourceNotCreated", reason)
	assert.Equal(t, "Waiting for NodePool", message)
}

func TestRenderNodePoolResource_TemplateNotFound(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	engine := NewEngine(time.Second*30, logger)

	nodepool := &sdk.NodePool{
		ID:        "np-123",
		ClusterID: "cluster-456",
		Name:      "test-pool",
	}

	cluster := &sdk.Cluster{
		ID:   "cluster-456",
		Name: "test-cluster",
	}

	_, err := engine.RenderNodePoolResource("nonexistent", nodepool, cluster, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "template not found")
}

func TestBuildClusterContext(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	engine := NewEngine(time.Second*30, logger)

	spec := map[string]interface{}{
		"platform": map[string]interface{}{
			"type": "GCP",
			"gcp": map[string]interface{}{
				"projectID": "my-project",
				"region":    "us-central1",
			},
		},
	}
	specJSON, _ := json.Marshal(spec)

	cluster := &sdk.Cluster{
		ID:              "cluster-123",
		Name:            "my-cluster",
		Generation:      5,
		ResourceVersion: "67890",
		Spec:            specJSON,
		CreatedBy:       "user@example.com",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	ctx := engine.buildClusterContext(cluster)

	assert.Equal(t, "cluster-123", ctx["id"])
	assert.Equal(t, "my-cluster", ctx["name"])
	assert.Equal(t, int64(5), ctx["generation"])
	assert.Equal(t, "user@example.com", ctx["created_by"])

	// Verify spec was parsed
	parsedSpec, ok := ctx["spec"].(map[string]interface{})
	require.True(t, ok, "spec should be a map")

	platform, ok := parsedSpec["platform"].(map[string]interface{})
	require.True(t, ok, "platform should be a map")
	assert.Equal(t, "GCP", platform["type"])
}
