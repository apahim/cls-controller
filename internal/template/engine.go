package template

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/apahim/cls-controller/internal/crd"
	"github.com/apahim/cls-controller/internal/sdk"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Engine handles template rendering for Kubernetes resources
type Engine struct {
	timeout   time.Duration
	logger    *zap.Logger
	templates map[string]*template.Template // keyed by resource name
}

// NewEngine creates a new template engine
func NewEngine(timeout time.Duration, logger *zap.Logger) *Engine {
	return &Engine{
		timeout:   timeout,
		logger:    logger.Named("template"),
		templates: make(map[string]*template.Template),
	}
}

// CompileTemplates compiles all templates in a ControllerConfig
func (e *Engine) CompileTemplates(config *crd.ControllerConfig) error {
	e.logger.Info("Compiling templates",
		zap.String("controller", config.Spec.Name),
		zap.Int("resource_count", len(config.Spec.Resources)),
	)

	e.templates = make(map[string]*template.Template)

	for _, resource := range config.Spec.Resources {
		tmpl, err := e.compileTemplate(resource.Name, resource.Template)
		if err != nil {
			return fmt.Errorf("failed to compile template for resource %s: %w", resource.Name, err)
		}
		e.templates[resource.Name] = tmpl
	}

	// Compile status condition templates
	for _, condition := range config.Spec.StatusConditions {
		conditionKey := fmt.Sprintf("status_%s", condition.Name)

		// Compile status template
		statusTmpl, err := e.compileTemplate(conditionKey+"_status", condition.Status)
		if err != nil {
			return fmt.Errorf("failed to compile status template for condition %s: %w", condition.Name, err)
		}
		e.templates[conditionKey+"_status"] = statusTmpl

		// Compile reason template
		reasonTmpl, err := e.compileTemplate(conditionKey+"_reason", condition.Reason)
		if err != nil {
			return fmt.Errorf("failed to compile reason template for condition %s: %w", condition.Name, err)
		}
		e.templates[conditionKey+"_reason"] = reasonTmpl

		// Compile message template
		messageTmpl, err := e.compileTemplate(conditionKey+"_message", condition.Message)
		if err != nil {
			return fmt.Errorf("failed to compile message template for condition %s: %w", condition.Name, err)
		}
		e.templates[conditionKey+"_message"] = messageTmpl
	}

	e.logger.Info("Templates compiled successfully", zap.Int("total_templates", len(e.templates)))
	return nil
}

// RenderResource renders a Kubernetes resource from a template
func (e *Engine) RenderResource(resourceName string, cluster *sdk.Cluster, resources map[string]*unstructured.Unstructured) (*unstructured.Unstructured, error) {
	tmpl, exists := e.templates[resourceName]
	if !exists {
		return nil, fmt.Errorf("template not found for resource: %s", resourceName)
	}

	// Build template context
	context := e.buildTemplateContext(cluster, resources)

	// Render template
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, context); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	// Parse YAML to unstructured
	var obj unstructured.Unstructured
	if err := yaml.Unmarshal(buf.Bytes(), &obj); err != nil {
		return nil, fmt.Errorf("failed to parse rendered YAML: %w", err)
	}

	e.logger.Debug("Rendered resource template",
		zap.String("resource_name", resourceName),
		zap.String("kind", obj.GetKind()),
		zap.String("name", obj.GetName()),
		zap.String("namespace", obj.GetNamespace()),
	)

	return &obj, nil
}

// RenderStatusCondition renders a status condition template
func (e *Engine) RenderStatusCondition(conditionName string, cluster *sdk.Cluster, resources map[string]*unstructured.Unstructured) (status, reason, message string, err error) {
	context := e.buildTemplateContext(cluster, resources)
	conditionKey := fmt.Sprintf("status_%s", conditionName)

	// Render status
	if statusTmpl, exists := e.templates[conditionKey+"_status"]; exists {
		status, err = e.renderStringTemplate(statusTmpl, context)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to render status for condition %s: %w", conditionName, err)
		}
	}

	// Render reason
	if reasonTmpl, exists := e.templates[conditionKey+"_reason"]; exists {
		reason, err = e.renderStringTemplate(reasonTmpl, context)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to render reason for condition %s: %w", conditionName, err)
		}
	}

	// Render message
	if messageTmpl, exists := e.templates[conditionKey+"_message"]; exists {
		message, err = e.renderStringTemplate(messageTmpl, context)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to render message for condition %s: %w", conditionName, err)
		}
	}

	return status, reason, message, nil
}

// RenderNodePoolResource renders a Kubernetes resource from a template with nodepool context
func (e *Engine) RenderNodePoolResource(resourceName string, nodepool *sdk.NodePool, cluster *sdk.Cluster, resources map[string]*unstructured.Unstructured) (*unstructured.Unstructured, error) {
	tmpl, exists := e.templates[resourceName]
	if !exists {
		return nil, fmt.Errorf("template not found for resource: %s", resourceName)
	}

	// Build template context for nodepool (includes cluster context)
	context := e.buildNodePoolTemplateContext(nodepool, cluster, resources)

	// Render template
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, context); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	// Parse YAML to unstructured
	var obj unstructured.Unstructured
	if err := yaml.Unmarshal(buf.Bytes(), &obj); err != nil {
		return nil, fmt.Errorf("failed to parse rendered YAML: %w", err)
	}

	e.logger.Debug("Rendered nodepool resource template",
		zap.String("resource_name", resourceName),
		zap.String("kind", obj.GetKind()),
		zap.String("name", obj.GetName()),
		zap.String("namespace", obj.GetNamespace()),
	)

	return &obj, nil
}

// RenderNodePoolStatusCondition renders a status condition template for nodepools
func (e *Engine) RenderNodePoolStatusCondition(conditionName string, nodepool *sdk.NodePool, cluster *sdk.Cluster, resources map[string]*unstructured.Unstructured) (status, reason, message string, err error) {
	context := e.buildNodePoolTemplateContext(nodepool, cluster, resources)
	conditionKey := fmt.Sprintf("status_%s", conditionName)

	// Render status
	if statusTmpl, exists := e.templates[conditionKey+"_status"]; exists {
		status, err = e.renderStringTemplate(statusTmpl, context)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to render status for condition %s: %w", conditionName, err)
		}
	}

	// Render reason
	if reasonTmpl, exists := e.templates[conditionKey+"_reason"]; exists {
		reason, err = e.renderStringTemplate(reasonTmpl, context)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to render reason for condition %s: %w", conditionName, err)
		}
	}

	// Render message
	if messageTmpl, exists := e.templates[conditionKey+"_message"]; exists {
		message, err = e.renderStringTemplate(messageTmpl, context)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to render message for condition %s: %w", conditionName, err)
		}
	}

	return status, reason, message, nil
}

// buildNodePoolTemplateContext builds the context object for nodepool template rendering
func (e *Engine) buildNodePoolTemplateContext(nodepool *sdk.NodePool, cluster *sdk.Cluster, resources map[string]*unstructured.Unstructured) map[string]interface{} {
	context := map[string]interface{}{
		"nodepool":  e.buildNodePoolContext(nodepool),
		"cluster":   e.buildClusterContext(cluster),
		"resources": e.buildResourcesContext(resources),
		"controller": map[string]interface{}{
			"name": "cls-controller", // TODO: get from config
		},
		"timestamp": time.Now().Unix(),
	}

	return context
}

// buildNodePoolContext builds the nodepool context from the nodepool spec
func (e *Engine) buildNodePoolContext(nodepool *sdk.NodePool) map[string]interface{} {
	nodepoolCtx := map[string]interface{}{
		"id":         nodepool.ID,
		"cluster_id": nodepool.ClusterID,
		"name":       nodepool.Name,
		"generation": nodepool.Generation,
	}

	// Parse spec JSON to make it accessible in templates
	var spec map[string]interface{}
	if err := json.Unmarshal(nodepool.Spec, &spec); err == nil {
		nodepoolCtx["spec"] = spec
	}

	return nodepoolCtx
}

// compileTemplate compiles a single template with our function map
func (e *Engine) compileTemplate(name, templateStr string) (*template.Template, error) {
	// Preprocess template to handle hyphenated resource names
	processedTemplate := e.preprocessTemplate(templateStr)

	tmpl := template.New(name).Funcs(e.getFunctionMap())

	compiled, err := tmpl.Parse(processedTemplate)
	if err != nil {
		return nil, fmt.Errorf("template parse error: %w", err)
	}

	return compiled, nil
}

// preprocessTemplate converts hyphenated resource references to use index notation
// For example: .resources.pull-secret becomes (index .resources "pull-secret")
func (e *Engine) preprocessTemplate(templateStr string) string {
	// Regular expression to match .resources.RESOURCE-NAME where RESOURCE-NAME contains hyphens
	// This pattern matches:
	// - .resources. (literal)
	// - followed by a resource name that contains at least one hyphen
	// - resource name can contain alphanumeric, hyphens, and underscores
	// - but must contain at least one hyphen to trigger the replacement
	re := regexp.MustCompile(`\.resources\.([a-zA-Z0-9_]*[a-zA-Z0-9_-]*-[a-zA-Z0-9_-]*)`)

	// Replace with index notation
	processed := re.ReplaceAllStringFunc(templateStr, func(match string) string {
		// Extract the resource name (everything after .resources.)
		resourceName := strings.TrimPrefix(match, ".resources.")
		// Replace with index notation
		return fmt.Sprintf(`(index .resources "%s")`, resourceName)
	})

	return processed
}

// renderStringTemplate renders a template to a string
func (e *Engine) renderStringTemplate(tmpl *template.Template, context interface{}) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, context); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// buildTemplateContext builds the context object for template rendering
func (e *Engine) buildTemplateContext(cluster *sdk.Cluster, resources map[string]*unstructured.Unstructured) map[string]interface{} {
	context := map[string]interface{}{
		"cluster":   e.buildClusterContext(cluster),
		"resources": e.buildResourcesContext(resources),
		"controller": map[string]interface{}{
			"name": "cls-controller", // TODO: get from config
		},
		"timestamp": time.Now().Unix(),
	}

	return context
}

// buildClusterContext builds the cluster context from the cluster spec
func (e *Engine) buildClusterContext(cluster *sdk.Cluster) map[string]interface{} {
	clusterCtx := map[string]interface{}{
		"id":         cluster.ID,
		"name":       cluster.Name,
		"generation": cluster.Generation,
		"created_by": cluster.CreatedBy,
	}

	// Parse spec JSON to make it accessible in templates
	var spec map[string]interface{}
	if err := json.Unmarshal(cluster.Spec, &spec); err == nil {
		clusterCtx["spec"] = spec
	}

	return clusterCtx
}

// buildResourcesContext builds the resources context from created Kubernetes resources
func (e *Engine) buildResourcesContext(resources map[string]*unstructured.Unstructured) map[string]interface{} {
	resourcesCtx := make(map[string]interface{})

	for name, resource := range resources {
		if resource != nil {
			resourcesCtx[name] = resource.Object
		}
	}

	return resourcesCtx
}

// getFunctionMap returns the template function map with all available functions
func (e *Engine) getFunctionMap() template.FuncMap {
	return template.FuncMap{
		// String manipulation functions
		"join":    strings.Join,
		"split":   strings.Split,
		"replace": func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },
		"trim":    strings.TrimSpace,
		"lower":   strings.ToLower,
		"upper":   strings.ToUpper,
		"substr":  substr,

		// Encoding functions
		"toJson":       toJson,
		"fromJson":     fromJson,
		"base64encode": base64encode,
		"base64decode": base64decode,

		// Utility functions
		"default":      defaultValue,
		"randomString": randomString,

		// Type conversion functions
		"int": toInt,
	}
}

// Template function implementations

func substr(start, length int, s string) string {
	if start < 0 || start >= len(s) {
		return ""
	}
	end := start + length
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

func toJson(obj interface{}) string {
	data, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	return string(data)
}

func fromJson(jsonStr string) (interface{}, error) {
	var obj interface{}
	err := json.Unmarshal([]byte(jsonStr), &obj)
	return obj, err
}

func base64encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func base64decode(s string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func defaultValue(defaultVal, val interface{}) interface{} {
	if val == nil || val == "" {
		return defaultVal
	}
	return val
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return string(b)
}

// toInt converts various numeric types to int for use with comparison functions
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	default:
		return 0
	}
}
