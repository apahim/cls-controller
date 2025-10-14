package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/apahim/cls-controller/internal/crd"
	controllersdk "github.com/apahim/controller-sdk"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// Manager manages different types of resource clients
type Manager struct {
	localClient    ctrlclient.Client
	remoteClients  map[string]ctrlclient.Client // cached remote clients by secret key
	maestroClients map[string]*MaestroClient    // cached Maestro clients by endpoint/consumer
	secretClient   ctrlclient.Client            // client for reading secrets
	logger         *zap.Logger
	mu             sync.RWMutex
}

// NewManager creates a new client manager
func NewManager(localClient ctrlclient.Client, logger *zap.Logger) (*Manager, error) {
	return &Manager{
		localClient:    localClient,
		remoteClients:  make(map[string]ctrlclient.Client),
		maestroClients: make(map[string]*MaestroClient),
		secretClient:   localClient, // Use local client to read secrets
		logger:         logger.Named("client-manager"),
	}, nil
}

// GetClient returns the appropriate client based on target configuration
func (m *Manager) GetClient(ctx context.Context, target *crd.TargetConfig, cluster *controllersdk.Cluster) (ResourceClient, error) {
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
func (m *Manager) getKubeAPIClient(ctx context.Context, target *crd.TargetConfig, cluster *controllersdk.Cluster) (ResourceClient, error) {
	// No kubeConfig specified - use local cluster
	if target == nil || target.KubeConfig == nil {
		return &KubeAPIClient{Client: m.localClient}, nil
	}

	// Remote cluster - get client from kubeconfig secret
	secretKey := fmt.Sprintf("%s/%s", target.KubeConfig.SecretRef.Name, target.KubeConfig.SecretRef.Key)

	m.mu.RLock()
	if client, exists := m.remoteClients[secretKey]; exists {
		m.mu.RUnlock()
		return &KubeAPIClient{Client: client}, nil
	}
	m.mu.RUnlock()

	// Create new remote client from secret
	kubeconfig, err := m.getKubeConfigFromSecret(ctx, target.KubeConfig.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig from secret: %w", err)
	}

	remoteClient, err := m.createRemoteClient(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create remote client: %w", err)
	}

	// Cache the client
	m.mu.Lock()
	m.remoteClients[secretKey] = remoteClient
	m.mu.Unlock()

	m.logger.Debug("Created remote Kubernetes client", zap.String("secret_key", secretKey))

	return &KubeAPIClient{Client: remoteClient}, nil
}

// getMaestroClient returns Maestro gRPC client with templated configuration
func (m *Manager) getMaestroClient(ctx context.Context, target *crd.TargetConfig, cluster *controllersdk.Cluster) (ResourceClient, error) {
	// TODO: Implement template rendering for endpoint and consumer
	// For now, return error as Maestro client is not implemented
	return nil, fmt.Errorf("Maestro client not implemented yet")
}

// getKubeConfigFromSecret reads kubeconfig from a secret
func (m *Manager) getKubeConfigFromSecret(ctx context.Context, secretRef crd.SecretReference) ([]byte, error) {
	// TODO: Implement secret reading
	// This would read the secret and extract the kubeconfig data
	return nil, fmt.Errorf("secret reading not implemented yet")
}

// createRemoteClient creates a Kubernetes client from kubeconfig data
func (m *Manager) createRemoteClient(kubeconfig []byte) (ctrlclient.Client, error) {
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