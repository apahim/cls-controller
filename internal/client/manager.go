package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/apahim/cls-controller/internal/crd"
	"github.com/apahim/cls-controller/internal/sdk"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// ResourceClient defines the interface for creating and managing Kubernetes resources
type ResourceClient interface {
	Create(ctx context.Context, obj *unstructured.Unstructured) error
	Update(ctx context.Context, obj *unstructured.Unstructured) error
	Get(ctx context.Context, name, namespace string, obj *unstructured.Unstructured) error
	Delete(ctx context.Context, obj *unstructured.Unstructured) error
	List(ctx context.Context, namespace string, labels map[string]string, list *unstructured.UnstructuredList) error
}

// ClusterInfo contains connection information for a remote cluster
type ClusterInfo struct {
	Endpoint string
	CACert   []byte // Optional CA certificate for TLS verification
}

// Manager manages different types of resource clients
type Manager struct {
	localClient     ctrlclient.Client
	remoteClients   map[string]ctrlclient.Client // cached remote clients by secret key
	maestroClients  map[string]*MaestroClient    // cached Maestro clients by endpoint/consumer
	secretClient    ctrlclient.Client            // client for reading secrets
	secretNamespace string                        // namespace to read secrets from (same as ControllerConfig)
	logger          *zap.Logger
	mu              sync.RWMutex
}

// NewManager creates a new client manager
func NewManager(localClient ctrlclient.Client, logger *zap.Logger) (*Manager, error) {
	return &Manager{
		localClient:     localClient,
		remoteClients:   make(map[string]ctrlclient.Client),
		maestroClients:  make(map[string]*MaestroClient),
		secretClient:    localClient, // Use local client to read secrets
		secretNamespace: "cls-system", // Default namespace, will be updated when ControllerConfig is loaded
		logger:          logger.Named("client-manager"),
	}, nil
}

// SetSecretNamespace updates the namespace used for reading kubeconfig secrets
func (m *Manager) SetSecretNamespace(namespace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secretNamespace = namespace
	m.logger.Debug("Updated secret namespace", zap.String("namespace", namespace))
}

// GetClient returns the appropriate client based on target configuration
func (m *Manager) GetClient(ctx context.Context, target *crd.TargetConfig, cluster *sdk.Cluster) (ResourceClient, error) {
	if target == nil || target.Type == "" || target.Type == crd.TargetTypeKubeAPI {
		return m.getKubeAPIClient(ctx, target, cluster)
	}

	switch target.Type {
	case crd.TargetTypeKubeAPI:
		return m.getKubeAPIClient(ctx, target, cluster)
	case crd.TargetTypeMaestro:
		return m.getMaestroClient(ctx, target, cluster)
	default:
		return nil, fmt.Errorf("unsupported target type: %s", target.Type)
	}
}

// getKubeAPIClient returns local or remote Kubernetes client
func (m *Manager) getKubeAPIClient(ctx context.Context, target *crd.TargetConfig, cluster *sdk.Cluster) (ResourceClient, error) {
	// Handle new structure with SecretRef
	if target != nil && target.SecretRef != nil {
		switch target.AuthMethod {
		case crd.AuthMethodWorkloadIdentity:
			return m.getWorkloadIdentityClient(ctx, target, cluster)
		default: // kubeconfig or empty (default to kubeconfig)
			return m.getKubeConfigClientFromSecretRef(ctx, target.SecretRef)
		}
	}

	// Handle legacy structure (backward compatibility)
	if target != nil && target.KubeConfig != nil {
		return m.getKubeConfigClientFromSecretRef(ctx, &target.KubeConfig.SecretRef)
	}

	// No config - use local cluster
	return &KubeAPIClient{Client: m.localClient}, nil
}

// getKubeConfigClientFromSecretRef creates a client from kubeconfig in secret
func (m *Manager) getKubeConfigClientFromSecretRef(ctx context.Context, secretRef *crd.SecretReference) (ResourceClient, error) {
	secretKey := fmt.Sprintf("%s/%s", secretRef.Name, secretRef.Key)

	m.mu.RLock()
	if client, exists := m.remoteClients[secretKey]; exists {
		m.mu.RUnlock()
		return &KubeAPIClient{Client: client}, nil
	}
	m.mu.RUnlock()

	// Create new remote client from secret
	kubeconfig, err := m.getKubeConfigFromSecret(ctx, *secretRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig from secret: %w", err)
	}

	remoteClient, err := m.createRemoteClientFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create remote client: %w", err)
	}

	// Cache the client
	m.mu.Lock()
	m.remoteClients[secretKey] = remoteClient
	m.mu.Unlock()

	m.logger.Debug("Created remote Kubernetes client from kubeconfig", zap.String("secret_key", secretKey))

	return &KubeAPIClient{Client: remoteClient}, nil
}

// getWorkloadIdentityClient creates a client using Workload Identity
func (m *Manager) getWorkloadIdentityClient(ctx context.Context, target *crd.TargetConfig, cluster *sdk.Cluster) (ResourceClient, error) {
	secretKey := fmt.Sprintf("wi-%s", target.SecretRef.Name)

	m.mu.RLock()
	if client, exists := m.remoteClients[secretKey]; exists {
		m.mu.RUnlock()
		m.logger.Debug("Reusing cached Workload Identity client with OAuth2 transport",
			zap.String("secret_key", secretKey),
			zap.String("note", "Client will auto-refresh tokens as needed"))
		return &KubeAPIClient{Client: client}, nil
	}
	m.mu.RUnlock()

	// Get cluster info from secret
	clusterInfo, err := m.getClusterInfoFromSecret(ctx, *target.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster info from secret: %w", err)
	}

	// Create client using Workload Identity
	remoteClient, err := m.createWorkloadIdentityClient(ctx, clusterInfo.Endpoint, clusterInfo.CACert)
	if err != nil {
		return nil, fmt.Errorf("failed to create Workload Identity client: %w", err)
	}

	// Cache the client
	m.mu.Lock()
	m.remoteClients[secretKey] = remoteClient
	m.mu.Unlock()

	m.logger.Debug("Created remote Kubernetes client using Workload Identity with OAuth2 transport",
		zap.String("secret_key", secretKey),
		zap.String("endpoint", clusterInfo.Endpoint),
		zap.String("note", "Client will auto-refresh tokens when they expire"))

	return &KubeAPIClient{Client: remoteClient}, nil
}

// getMaestroClient returns Maestro gRPC client with templated configuration
func (m *Manager) getMaestroClient(ctx context.Context, target *crd.TargetConfig, cluster *sdk.Cluster) (ResourceClient, error) {
	// TODO: Implement template rendering for endpoint and consumer
	// For now, return error as Maestro client is not implemented
	return nil, fmt.Errorf("Maestro client not implemented yet")
}

// getKubeConfigFromSecret reads kubeconfig from a secret
func (m *Manager) getKubeConfigFromSecret(ctx context.Context, secretRef crd.SecretReference) ([]byte, error) {
	m.mu.RLock()
	namespace := m.secretNamespace
	m.mu.RUnlock()

	// Create a secret object to fetch
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: namespace,
	}

	// Fetch the secret
	if err := m.secretClient.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretRef.Name, err)
	}

	// Extract kubeconfig data from the specified key
	kubeconfigData, exists := secret.Data[secretRef.Key]
	if !exists {
		return nil, fmt.Errorf("key %s not found in secret %s/%s", secretRef.Key, namespace, secretRef.Name)
	}

	if len(kubeconfigData) == 0 {
		return nil, fmt.Errorf("kubeconfig data is empty in secret %s/%s key %s", namespace, secretRef.Name, secretRef.Key)
	}

	// Validate that the kubeconfig can be parsed
	_, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("invalid kubeconfig in secret %s/%s key %s: %w", namespace, secretRef.Name, secretRef.Key, err)
	}

	m.logger.Debug("Successfully read and validated kubeconfig from secret",
		zap.String("secret_name", secretRef.Name),
		zap.String("secret_key", secretRef.Key),
		zap.String("namespace", namespace),
		zap.Int("kubeconfig_size", len(kubeconfigData)),
	)

	return kubeconfigData, nil
}

// createRemoteClientFromKubeConfig creates a Kubernetes client from kubeconfig data
func (m *Manager) createRemoteClientFromKubeConfig(kubeconfig []byte) (ctrlclient.Client, error) {
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create REST config from kubeconfig: %w", err)
	}

	client, err := ctrlclient.New(config, ctrlclient.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return client, nil
}

// getClusterInfoFromSecret retrieves cluster connection info from a secret
func (m *Manager) getClusterInfoFromSecret(ctx context.Context, secretRef crd.SecretReference) (*ClusterInfo, error) {
	m.mu.RLock()
	namespace := m.secretNamespace
	m.mu.RUnlock()

	// Create a secret object to fetch
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: namespace,
	}

	// Fetch the secret
	if err := m.secretClient.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretRef.Name, err)
	}

	// Extract endpoint data
	endpointData, exists := secret.Data["endpoint"]
	if !exists {
		return nil, fmt.Errorf("endpoint key not found in secret %s/%s", namespace, secretRef.Name)
	}

	if len(endpointData) == 0 {
		return nil, fmt.Errorf("endpoint data is empty in secret %s/%s", namespace, secretRef.Name)
	}

	clusterInfo := &ClusterInfo{
		Endpoint: string(endpointData),
	}

	// Extract optional CA certificate
	if caCertData, exists := secret.Data["ca-cert"]; exists && len(caCertData) > 0 {
		// Try to decode as base64 first
		if decoded, err := base64.StdEncoding.DecodeString(string(caCertData)); err == nil {
			clusterInfo.CACert = decoded
		} else {
			// Use as-is if not base64 encoded
			clusterInfo.CACert = caCertData
		}
	}

	m.logger.Debug("Successfully read cluster info from secret",
		zap.String("secret_name", secretRef.Name),
		zap.String("namespace", namespace),
		zap.String("endpoint", clusterInfo.Endpoint),
		zap.Bool("has_ca_cert", len(clusterInfo.CACert) > 0),
	)

	return clusterInfo, nil
}

// getEndpointFromSecret retrieves the endpoint from a secret (kept for backward compatibility)
func (m *Manager) getEndpointFromSecret(ctx context.Context, secretRef crd.SecretReference) (string, error) {
	clusterInfo, err := m.getClusterInfoFromSecret(ctx, secretRef)
	if err != nil {
		return "", err
	}
	return clusterInfo.Endpoint, nil
}

// createWorkloadIdentityClient creates a Kubernetes client using Workload Identity with automatic token refresh
func (m *Manager) createWorkloadIdentityClient(ctx context.Context, endpoint string, caCert []byte) (ctrlclient.Client, error) {
	// Use ambient Google Cloud credentials (Workload Identity)
	tokenSource, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("failed to get default token source: %w", err)
	}

	// Create custom HTTP transport that handles TLS configuration
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()

	// Configure TLS in the transport (not in REST config to avoid conflicts)
	if len(caCert) > 0 {
		// Use provided CA certificate
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		baseTransport.TLSClientConfig = &tls.Config{
			RootCAs: caCertPool,
		}
		m.logger.Debug("Using provided CA certificate for TLS verification in transport")
	} else {
		// For GKE clusters without provided CA cert, we need to handle the certificate issue
		// GKE private clusters often have cert issues with internal IPs
		// For now, we'll use insecure mode as a fallback
		baseTransport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
		m.logger.Warn("No CA certificate provided, using insecure TLS connection in transport",
			zap.String("endpoint", endpoint))
	}

	// Create OAuth2 transport with custom base transport that handles TLS
	// ReuseTokenSource ensures tokens are cached and automatically refreshed when they expire
	transport := &oauth2.Transport{
		Source: oauth2.ReuseTokenSource(nil, tokenSource),
		Base:   baseTransport,
	}

	// Create REST config with endpoint and OAuth2 transport for automatic token refresh
	// Important: Do NOT set TLSClientConfig here when using custom transport
	config := &rest.Config{
		Host:      endpoint,
		Transport: transport, // OAuth2 transport handles both auth and TLS
		// Increase timeouts for better reliability with GKE clusters
		Timeout: 60 * time.Second, // 60 seconds for overall operations
		// Add additional timeout configurations
		QPS:   50,  // Allow more requests per second
		Burst: 100, // Allow burst of requests
	}

	// Create Kubernetes client
	client, err := ctrlclient.New(config, ctrlclient.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Test the token source by getting an initial token (for logging purposes)
	initialToken, err := tokenSource.Token()
	if err != nil {
		m.logger.Warn("Failed to get initial token for logging, but client should still work with auto-refresh",
			zap.Error(err))
	}

	m.logger.Info("Successfully created Workload Identity client with automatic token refresh",
		zap.String("endpoint", endpoint),
		zap.Bool("has_ca_cert", len(caCert) > 0),
		zap.Bool("insecure_tls", len(caCert) == 0),
		zap.Duration("timeout", config.Timeout),
		zap.Bool("token_available", initialToken != nil),
	)

	// Log client creation success - actual connectivity will be tested during use
	m.logger.Info("Workload Identity client created with OAuth2 transport - tokens will auto-refresh",
		zap.String("endpoint", endpoint),
		zap.String("note", "Client will automatically refresh tokens when they expire (~1 hour)"),
	)

	return client, nil
}

// KubeAPIClient wraps controller-runtime client.Client to implement ResourceClient
type KubeAPIClient struct {
	ctrlclient.Client
}

// Create implements ResourceClient for Kubernetes API
func (k *KubeAPIClient) Create(ctx context.Context, obj *unstructured.Unstructured) error {
	return k.Client.Create(ctx, obj)
}

// Update implements ResourceClient for Kubernetes API
func (k *KubeAPIClient) Update(ctx context.Context, obj *unstructured.Unstructured) error {
	return k.Client.Update(ctx, obj)
}

// Get implements ResourceClient for Kubernetes API
func (k *KubeAPIClient) Get(ctx context.Context, name, namespace string, obj *unstructured.Unstructured) error {
	return k.Client.Get(ctx, ctrlclient.ObjectKey{Name: name, Namespace: namespace}, obj)
}

// Delete implements ResourceClient for Kubernetes API
func (k *KubeAPIClient) Delete(ctx context.Context, obj *unstructured.Unstructured) error {
	return k.Client.Delete(ctx, obj)
}

// List implements ResourceClient for Kubernetes API
func (k *KubeAPIClient) List(ctx context.Context, namespace string, labels map[string]string, list *unstructured.UnstructuredList) error {
	listOpts := []ctrlclient.ListOption{}

	if namespace != "" {
		listOpts = append(listOpts, ctrlclient.InNamespace(namespace))
	}

	if len(labels) > 0 {
		listOpts = append(listOpts, ctrlclient.MatchingLabels(labels))
	}

	return k.Client.List(ctx, list, listOpts...)
}

// MaestroClient wraps Maestro gRPC client and converts to Kubernetes-like interface
type MaestroClient struct {
	// TODO: Add Maestro client fields
	consumer string
	endpoint string
	logger   *zap.Logger
}

// Create implements ResourceClient for Maestro
func (mc *MaestroClient) Create(ctx context.Context, obj *unstructured.Unstructured) error {
	// TODO: Implement Maestro resource creation
	return fmt.Errorf("Maestro client not implemented yet")
}

// Update implements ResourceClient for Maestro
func (mc *MaestroClient) Update(ctx context.Context, obj *unstructured.Unstructured) error {
	// TODO: Implement Maestro resource update
	return fmt.Errorf("Maestro client not implemented yet")
}

// Get implements ResourceClient for Maestro
func (mc *MaestroClient) Get(ctx context.Context, name, namespace string, obj *unstructured.Unstructured) error {
	// TODO: Implement Maestro resource get
	return fmt.Errorf("Maestro client not implemented yet")
}

// Delete implements ResourceClient for Maestro
func (mc *MaestroClient) Delete(ctx context.Context, obj *unstructured.Unstructured) error {
	// TODO: Implement Maestro resource deletion
	return fmt.Errorf("Maestro client not implemented yet")
}

// List implements ResourceClient for Maestro
func (mc *MaestroClient) List(ctx context.Context, namespace string, labels map[string]string, list *unstructured.UnstructuredList) error {
	// TODO: Implement Maestro resource listing
	return fmt.Errorf("Maestro client not implemented yet")
}