package template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
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
