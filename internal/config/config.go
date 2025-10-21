package config

import (
	"fmt"
	"os"
	"time"

	"github.com/apahim/cls-controller/internal/sdk"
	"github.com/spf13/viper"
)

// Config represents the controller configuration
type Config struct {
	// Controller identification
	ControllerName string `mapstructure:"controller_name"`

	// Kubernetes controller configuration
	MetricsAddr           string `mapstructure:"metrics_addr"`
	ProbeAddr            string `mapstructure:"probe_addr"`
	EnableLeaderElection bool   `mapstructure:"enable_leader_election"`

	// CLS Backend configuration
	ProjectID   string `mapstructure:"project_id"`
	APIBaseURL  string `mapstructure:"api_base_url"`
	APITimeout  time.Duration `mapstructure:"api_timeout"`

	// Controller authentication configuration
	ControllerEmail string `mapstructure:"controller_email"`

	// Pub/Sub configuration
	StatusTopic        string `mapstructure:"status_topic"`
	EventsSubscription string `mapstructure:"events_subscription"`
	PubSubEmulatorHost string `mapstructure:"pubsub_emulator_host"`
	CredentialsFile    string `mapstructure:"credentials_file"`

	// Controller operation configuration
	RetryAttempts       int           `mapstructure:"retry_attempts"`
	RetryBackoff        time.Duration `mapstructure:"retry_backoff"`
	HealthCheckInterval time.Duration `mapstructure:"health_check_interval"`

	// ControllerConfig CRD configuration
	ConfigNamespace string `mapstructure:"config_namespace"`
	ConfigName      string `mapstructure:"config_name"`

	// Template configuration
	TemplateTimeout time.Duration `mapstructure:"template_timeout"`

	// SDK configuration (derived from above)
	SDKConfig sdk.Config
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	cfg := &Config{
		// Default values
		MetricsAddr:           ":8080",
		ProbeAddr:            ":8081",
		EnableLeaderElection:  false,
		APITimeout:           30 * time.Second,
		RetryAttempts:        3,
		RetryBackoff:         5 * time.Second,
		HealthCheckInterval:  60 * time.Second,
		ConfigNamespace:      "cls-system",
		TemplateTimeout:      30 * time.Second,
	}

	// Set up Viper
	v := viper.New()

	// Set environment variable handling
	v.SetEnvPrefix("CLS_CONTROLLER")
	v.AutomaticEnv()

	// Bind environment variables
	bindEnvVars(v)

	// Unmarshal configuration
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	// Validate and set defaults
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Build SDK configuration
	cfg.buildSDKConfig()

	return cfg, nil
}

// bindEnvVars binds environment variables to configuration keys
func bindEnvVars(v *viper.Viper) {
	envVars := map[string]string{
		"controller_name":       "CONTROLLER_NAME",
		"project_id":           "PROJECT_ID",
		"api_base_url":         "API_BASE_URL",
		"controller_email":     "CONTROLLER_EMAIL",
		"status_topic":         "STATUS_TOPIC",
		"events_subscription":  "EVENTS_SUBSCRIPTION",
		"pubsub_emulator_host": "PUBSUB_EMULATOR_HOST",
		"credentials_file":     "CREDENTIALS_FILE",
		"config_namespace":     "CONFIG_NAMESPACE",
		"config_name":          "CONFIG_NAME",
		"metrics_addr":         "METRICS_ADDR",
		"probe_addr":           "PROBE_ADDR",
		"enable_leader_election": "ENABLE_LEADER_ELECTION",
	}

	for key, env := range envVars {
		v.BindEnv(key, env)
	}
}

// validate validates the configuration
func (c *Config) validate() error {
	if c.ControllerName == "" {
		c.ControllerName = os.Getenv("CONTROLLER_NAME")
	}

	if c.ProjectID == "" {
		c.ProjectID = os.Getenv("PROJECT_ID")
	}

	if c.ProjectID == "" {
		return fmt.Errorf("project_id is required")
	}

	if c.APIBaseURL == "" {
		c.APIBaseURL = os.Getenv("API_BASE_URL")
		if c.APIBaseURL == "" {
			c.APIBaseURL = "http://localhost:8080"
		}
	}

	if c.StatusTopic == "" {
		c.StatusTopic = "cls-controller-status"
	}

	if c.EventsSubscription == "" {
		if c.ControllerName != "" {
			c.EventsSubscription = fmt.Sprintf("cls-%s-events", c.ControllerName)
		} else {
			c.EventsSubscription = "cls-controller-events"
		}
	}

	// Set config name from controller name if not provided
	if c.ConfigName == "" && c.ControllerName != "" {
		c.ConfigName = c.ControllerName
	}

	return nil
}

// buildSDKConfig builds the SDK configuration from controller config
func (c *Config) buildSDKConfig() {
	// Set default controller email if not provided
	if c.ControllerEmail == "" {
		c.ControllerEmail = "controller@system.local"
	}

	c.SDKConfig = sdk.Config{
		ProjectID:           c.ProjectID,
		ControllerName:      c.ControllerName,
		StatusTopic:         c.StatusTopic,
		EventsSubscription:  c.EventsSubscription,
		PubSubEmulatorHost:  c.PubSubEmulatorHost,
		CredentialsFile:     c.CredentialsFile,
		RetryAttempts:       c.RetryAttempts,
		RetryBackoff:        c.RetryBackoff,
		HealthCheckInterval: c.HealthCheckInterval,
		APIBaseURL:          c.APIBaseURL,
		APITimeout:          c.APITimeout,
		ControllerEmail:     c.ControllerEmail,
	}
}

