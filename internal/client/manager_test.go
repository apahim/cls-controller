package client

import (
	"context"
	"testing"

	"github.com/apahim/cls-controller/internal/crd"
	"github.com/apahim/cls-controller/internal/sdk"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Helper function to create a valid kubeconfig for testing
func createTestKubeconfig() []byte {
	config := &clientcmdapi.Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: map[string]*clientcmdapi.Cluster{
			"test-cluster": {
				Server:                "https://kubernetes.test.example.com",
				InsecureSkipTLSVerify: true, // For testing only
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"test-user": {
				Token: "test-token",
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"test-context": {
				Cluster:  "test-cluster",
				AuthInfo: "test-user",
			},
		},
		CurrentContext: "test-context",
	}

	kubeconfigBytes, _ := clientcmd.Write(*config)
	return kubeconfigBytes
}

func TestManager_GetClient_LocalTarget(t *testing.T) {
	logger := zap.NewNop()
	fakeClient := fake.NewClientBuilder().Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()
	cluster := &sdk.Cluster{ID: "test-cluster"}

	// Test nil target (should use local client)
	client, err := manager.GetClient(ctx, nil, cluster)
	if err != nil {
		t.Fatalf("Failed to get local client: %v", err)
	}

	if client == nil {
		t.Fatal("Expected client, got nil")
	}

	// Test empty target (should use local client)
	target := &crd.TargetConfig{}
	client, err = manager.GetClient(ctx, target, cluster)
	if err != nil {
		t.Fatalf("Failed to get local client with empty target: %v", err)
	}

	if client == nil {
		t.Fatal("Expected client, got nil")
	}

	// Test explicit kube-api target without kubeconfig (should use local client)
	target = &crd.TargetConfig{
		Type: crd.TargetTypeKubeAPI,
	}
	client, err = manager.GetClient(ctx, target, cluster)
	if err != nil {
		t.Fatalf("Failed to get local client with kube-api target: %v", err)
	}

	if client == nil {
		t.Fatal("Expected client, got nil")
	}
}

func TestManager_GetClient_UnsupportedTarget(t *testing.T) {
	logger := zap.NewNop()
	fakeClient := fake.NewClientBuilder().Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()
	cluster := &sdk.Cluster{ID: "test-cluster"}

	// Test unsupported target type
	target := &crd.TargetConfig{
		Type: "unsupported",
	}

	client, err := manager.GetClient(ctx, target, cluster)
	if err == nil {
		t.Fatal("Expected error for unsupported target type")
	}

	if client != nil {
		t.Fatal("Expected nil client for unsupported target")
	}

	expectedError := "unsupported target type: unsupported"
	if err.Error() != expectedError {
		t.Fatalf("Expected error %q, got %q", expectedError, err.Error())
	}
}

func TestManager_GetKubeConfigFromSecret_Success(t *testing.T) {
	logger := zap.NewNop()

	// Create a secret with valid kubeconfig
	kubeconfigData := createTestKubeconfig()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "test-namespace",
		},
		Data: map[string][]byte{
			"kubeconfig": kubeconfigData,
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Set the namespace for secret reading
	manager.SetSecretNamespace("test-namespace")

	ctx := context.Background()
	secretRef := crd.SecretReference{
		Name: "test-secret",
		Key:  "kubeconfig",
	}

	// Test successful secret reading
	data, err := manager.getKubeConfigFromSecret(ctx, secretRef)
	if err != nil {
		t.Fatalf("Failed to get kubeconfig from secret: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("Expected kubeconfig data, got empty")
	}

	// Verify the kubeconfig is valid
	_, err = clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		t.Fatalf("Retrieved kubeconfig is invalid: %v", err)
	}
}

func TestManager_GetKubeConfigFromSecret_SecretNotFound(t *testing.T) {
	logger := zap.NewNop()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	manager.SetSecretNamespace("test-namespace")

	ctx := context.Background()
	secretRef := crd.SecretReference{
		Name: "nonexistent-secret",
		Key:  "kubeconfig",
	}

	// Test secret not found
	data, err := manager.getKubeConfigFromSecret(ctx, secretRef)
	if err == nil {
		t.Fatal("Expected error for nonexistent secret")
	}

	if data != nil {
		t.Fatal("Expected nil data for nonexistent secret")
	}

	expectedError := "failed to get secret test-namespace/nonexistent-secret"
	if !contains(err.Error(), expectedError) {
		t.Fatalf("Expected error containing %q, got %q", expectedError, err.Error())
	}
}

func TestManager_GetKubeConfigFromSecret_KeyNotFound(t *testing.T) {
	logger := zap.NewNop()

	// Create a secret without the expected key
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "test-namespace",
		},
		Data: map[string][]byte{
			"other-key": []byte("some-data"),
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	manager.SetSecretNamespace("test-namespace")

	ctx := context.Background()
	secretRef := crd.SecretReference{
		Name: "test-secret",
		Key:  "kubeconfig",
	}

	// Test key not found
	data, err := manager.getKubeConfigFromSecret(ctx, secretRef)
	if err == nil {
		t.Fatal("Expected error for missing key")
	}

	if data != nil {
		t.Fatal("Expected nil data for missing key")
	}

	expectedError := "key kubeconfig not found in secret test-namespace/test-secret"
	if err.Error() != expectedError {
		t.Fatalf("Expected error %q, got %q", expectedError, err.Error())
	}
}

func TestManager_GetKubeConfigFromSecret_EmptyData(t *testing.T) {
	logger := zap.NewNop()

	// Create a secret with empty kubeconfig data
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "test-namespace",
		},
		Data: map[string][]byte{
			"kubeconfig": []byte(""),
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	manager.SetSecretNamespace("test-namespace")

	ctx := context.Background()
	secretRef := crd.SecretReference{
		Name: "test-secret",
		Key:  "kubeconfig",
	}

	// Test empty data
	data, err := manager.getKubeConfigFromSecret(ctx, secretRef)
	if err == nil {
		t.Fatal("Expected error for empty kubeconfig data")
	}

	if data != nil {
		t.Fatal("Expected nil data for empty kubeconfig")
	}

	expectedError := "kubeconfig data is empty in secret test-namespace/test-secret key kubeconfig"
	if err.Error() != expectedError {
		t.Fatalf("Expected error %q, got %q", expectedError, err.Error())
	}
}

func TestManager_GetKubeConfigFromSecret_InvalidKubeconfig(t *testing.T) {
	logger := zap.NewNop()

	// Create a secret with invalid kubeconfig data
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "test-namespace",
		},
		Data: map[string][]byte{
			"kubeconfig": []byte("invalid-yaml-data"),
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	manager.SetSecretNamespace("test-namespace")

	ctx := context.Background()
	secretRef := crd.SecretReference{
		Name: "test-secret",
		Key:  "kubeconfig",
	}

	// Test invalid kubeconfig
	data, err := manager.getKubeConfigFromSecret(ctx, secretRef)
	if err == nil {
		t.Fatal("Expected error for invalid kubeconfig")
	}

	if data != nil {
		t.Fatal("Expected nil data for invalid kubeconfig")
	}

	expectedError := "invalid kubeconfig in secret test-namespace/test-secret key kubeconfig"
	if !contains(err.Error(), expectedError) {
		t.Fatalf("Expected error containing %q, got %q", expectedError, err.Error())
	}
}

func TestManager_SetSecretNamespace(t *testing.T) {
	logger := zap.NewNop()
	fakeClient := fake.NewClientBuilder().Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Test default namespace
	if manager.secretNamespace != "cls-system" {
		t.Fatalf("Expected default namespace 'cls-system', got %q", manager.secretNamespace)
	}

	// Test setting new namespace
	manager.SetSecretNamespace("custom-namespace")
	if manager.secretNamespace != "custom-namespace" {
		t.Fatalf("Expected namespace 'custom-namespace', got %q", manager.secretNamespace)
	}
}

func TestManager_GetClient_RemoteTarget_SecretNotFound(t *testing.T) {
	logger := zap.NewNop()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	manager.SetSecretNamespace("test-namespace")

	ctx := context.Background()
	cluster := &sdk.Cluster{ID: "test-cluster"}

	// Test remote target with secret not found
	target := &crd.TargetConfig{
		Type: crd.TargetTypeKubeAPI,
		KubeConfig: &crd.KubeConfigReference{
			SecretRef: crd.SecretReference{
				Name: "nonexistent-secret",
				Key:  "kubeconfig",
			},
		},
	}

	client, err := manager.GetClient(ctx, target, cluster)
	if err == nil {
		t.Fatal("Expected error for nonexistent secret")
	}

	if client != nil {
		t.Fatal("Expected nil client for nonexistent secret")
	}

	expectedError := "failed to get kubeconfig from secret"
	if !contains(err.Error(), expectedError) {
		t.Fatalf("Expected error containing %q, got %q", expectedError, err.Error())
	}
}

func TestManager_ClientCaching(t *testing.T) {
	logger := zap.NewNop()

	// Create a secret with valid kubeconfig
	kubeconfigData := createTestKubeconfig()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "test-namespace",
		},
		Data: map[string][]byte{
			"kubeconfig": kubeconfigData,
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	manager.SetSecretNamespace("test-namespace")

	ctx := context.Background()
	cluster := &sdk.Cluster{ID: "test-cluster"}

	target := &crd.TargetConfig{
		Type: crd.TargetTypeKubeAPI,
		KubeConfig: &crd.KubeConfigReference{
			SecretRef: crd.SecretReference{
				Name: "test-secret",
				Key:  "kubeconfig",
			},
		},
	}

	// Test client caching behavior - either both succeed or both fail
	client1, err1 := manager.GetClient(ctx, target, cluster)
	client2, err2 := manager.GetClient(ctx, target, cluster)

	// Both should have consistent results
	if (err1 == nil) != (err2 == nil) {
		t.Fatalf("Inconsistent error results: err1=%v, err2=%v", err1, err2)
	}

	if err1 != nil {
		// Both failed - should be consistent failures
		if client1 != nil {
			t.Fatal("Expected nil client when error occurs")
		}
		if client2 != nil {
			t.Fatal("Expected nil client when error occurs")
		}

		// Error should be related to client creation, not secret reading
		expectedErrorSubstring := "failed to create remote client"
		if !contains(err1.Error(), expectedErrorSubstring) {
			t.Fatalf("Expected error containing %q, got %q", expectedErrorSubstring, err1.Error())
		}

		// The cache should be empty since client creation failed
		if len(manager.remoteClients) > 0 {
			t.Fatal("Expected empty cache when client creation fails")
		}
	} else {
		// Both succeeded - test caching behavior
		if client1 == nil {
			t.Fatal("Expected non-nil client when no error occurs")
		}
		if client2 == nil {
			t.Fatal("Expected non-nil client when no error occurs")
		}

		// Clients should work correctly (we can't test identity equality
		// because they're wrapped in different KubeAPIClient structs)

		// Check that the underlying client was cached
		secretKey := "test-secret/kubeconfig"
		if _, exists := manager.remoteClients[secretKey]; !exists {
			t.Fatal("Expected client to be cached")
		}

		// Verify we only have one cached client
		if len(manager.remoteClients) != 1 {
			t.Fatalf("Expected exactly 1 cached client, got %d", len(manager.remoteClients))
		}
	}
}

func TestManager_SecretReading(t *testing.T) {
	logger := zap.NewNop()

	// Create a secret with valid kubeconfig
	kubeconfigData := createTestKubeconfig()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "test-namespace",
		},
		Data: map[string][]byte{
			"kubeconfig": kubeconfigData,
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	manager.SetSecretNamespace("test-namespace")

	ctx := context.Background()
	secretRef := crd.SecretReference{
		Name: "test-secret",
		Key:  "kubeconfig",
	}

	// Test that secret can be read successfully multiple times
	data1, err1 := manager.getKubeConfigFromSecret(ctx, secretRef)
	data2, err2 := manager.getKubeConfigFromSecret(ctx, secretRef)

	if err1 != nil {
		t.Fatalf("First secret read failed: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("Second secret read failed: %v", err2)
	}

	if len(data1) == 0 {
		t.Fatal("First secret read returned empty data")
	}
	if len(data2) == 0 {
		t.Fatal("Second secret read returned empty data")
	}

	// Data should be identical
	if string(data1) != string(data2) {
		t.Fatal("Secret data should be identical between reads")
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		   (len(substr) == 0 ||
		    s == substr ||
		    (len(s) > len(substr) &&
		     (s[:len(substr)] == substr ||
		      s[len(s)-len(substr):] == substr ||
		      containsSubstring(s, substr))))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestManager_GetClient_NewStructureLocalTarget(t *testing.T) {
	logger := zap.NewNop()
	fakeClient := fake.NewClientBuilder().Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()
	cluster := &sdk.Cluster{ID: "test-cluster"}

	// Test new structure without SecretRef (should use local client)
	target := &crd.TargetConfig{
		Type: crd.TargetTypeKubeAPI,
	}
	client, err := manager.GetClient(ctx, target, cluster)
	if err != nil {
		t.Fatalf("Failed to get local client with new structure: %v", err)
	}

	if client == nil {
		t.Fatal("Expected client, got nil")
	}
}

func TestManager_GetClient_WorkloadIdentityStructure(t *testing.T) {
	logger := zap.NewNop()
	fakeClient := fake.NewClientBuilder().Build()

	manager, err := NewManager(fakeClient, logger)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()
	cluster := &sdk.Cluster{ID: "test-cluster"}

	// Test Workload Identity structure (should fail without secret, but should recognize the auth method)
	target := &crd.TargetConfig{
		Type:       crd.TargetTypeKubeAPI,
		AuthMethod: crd.AuthMethodWorkloadIdentity,
		SecretRef: &crd.SecretReference{
			Name: "nonexistent-secret",
		},
	}

	client, err := manager.GetClient(ctx, target, cluster)
	if err == nil {
		t.Fatal("Expected error for nonexistent secret with Workload Identity")
	}

	if client != nil {
		t.Fatal("Expected nil client for failed Workload Identity auth")
	}

	// Error should be related to getting cluster info from secret
	expectedErrorSubstring := "failed to get cluster info"
	if !contains(err.Error(), expectedErrorSubstring) {
		t.Fatalf("Expected error containing %q, got %q", expectedErrorSubstring, err.Error())
	}
}