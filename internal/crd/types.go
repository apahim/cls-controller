package crd

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ControllerConfig defines the configuration for a generalized controller
type ControllerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ControllerConfigSpec   `json:"spec,omitempty"`
	Status ControllerConfigStatus `json:"status,omitempty"`
}

// ControllerConfigSpec defines the desired state of a controller
type ControllerConfigSpec struct {
	// Name is the controller name
	Name string `json:"name"`

	// Description describes what this controller does
	Description string `json:"description,omitempty"`

	// Target configures where resources are created
	// +optional
	Target *TargetConfig `json:"target,omitempty"`

	// Preconditions define conditions that must be met for resource creation
	// +optional
	Preconditions []PreconditionRule `json:"preconditions,omitempty"`

	// Resources defines the Kubernetes resources to create
	Resources []ResourceConfig `json:"resources"`

	// StatusConditions define how to report status to cls-backend
	// +optional
	StatusConditions []StatusConditionConfig `json:"statusConditions,omitempty"`
}

// TargetConfig defines where resources should be created
type TargetConfig struct {
	// Type specifies the target type: "kube-api" or "maestro"
	// +kubebuilder:default="kube-api"
	Type string `json:"type,omitempty"`

	// AuthMethod specifies authentication method: "kubeconfig" or "workload-identity"
	// +kubebuilder:default="kubeconfig"
	// +optional
	AuthMethod string `json:"authMethod,omitempty"`

	// SecretRef references a secret containing authentication data
	// For kubeconfig: secret should contain "kubeconfig" key
	// For workload-identity: secret should contain "endpoint" key
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`

	// KubeConfig configures remote Kubernetes API access (DEPRECATED: use AuthMethod + SecretRef)
	// +optional
	KubeConfig *KubeConfigReference `json:"kubeConfig,omitempty"`

	// MaestroConfig configures Maestro API access
	// +optional
	MaestroConfig *MaestroConfig `json:"maestroConfig,omitempty"`
}

// KubeConfigReference references a secret containing kubeconfig
type KubeConfigReference struct {
	// SecretRef references the secret containing kubeconfig
	SecretRef SecretReference `json:"secretRef"`
}

// SecretReference references a secret
type SecretReference struct {
	// Name is the name of the secret
	Name string `json:"name"`

	// Key is the key within the secret containing the data
	Key string `json:"key"`
}

// MaestroConfig configures Maestro API access
type MaestroConfig struct {
	// Endpoint is the templated maestro gRPC endpoint
	Endpoint string `json:"endpoint"`

	// Consumer is the templated maestro consumer ID
	Consumer string `json:"consumer"`
}

// PreconditionRule defines a condition that must be met
type PreconditionRule struct {
	// Field is the cluster field path (e.g., 'spec.provider', 'status.assignedConsumer')
	Field string `json:"field"`

	// Operator is the comparison operator
	// +kubebuilder:validation:Enum=eq;ne;in;notin;exists;notexists
	Operator string `json:"operator"`

	// Value is the value to compare against (optional for exists/notexists)
	// +optional
	Value runtime.RawExtension `json:"value,omitempty"`
}

// ResourceConfig defines a Kubernetes resource to create
type ResourceConfig struct {
	// Name is a unique name for this resource within the controller
	Name string `json:"name"`

	// Description describes what this resource does
	// +optional
	Description string `json:"description,omitempty"`

	// Template is the Kubernetes resource YAML template
	Template string `json:"template"`

	// ResourceManagement defines resource-specific management policies
	// +optional
	ResourceManagement *ResourceManagement `json:"resourceManagement,omitempty"`
}

// ResourceManagement defines how to manage resource lifecycle
type ResourceManagement struct {
	// UpdateStrategy defines how to handle resource updates
	// +kubebuilder:validation:Enum=in_place;versioned
	// +kubebuilder:default="in_place"
	UpdateStrategy string `json:"updateStrategy,omitempty"`

	// VersionInterval defines minimum time between creating new versions (only applies when updateStrategy is 'versioned')
	// New versions are only created if this interval has elapsed since the last version, unless generation changes
	// +kubebuilder:default="5m"
	// +optional
	VersionInterval string `json:"versionInterval,omitempty"`

	// NewGenerationOnly when set to true ensures new versioned resources are only created when the cluster generation changes,
	// ignoring the versionInterval time-based constraint
	// +kubebuilder:default=false
	// +optional
	NewGenerationOnly bool `json:"newGenerationOnly,omitempty"`

	// KeepVersions defines how many completed versions to keep for versioned resources
	// Older completed versions beyond this count will be deleted automatically
	// +kubebuilder:default=2
	// +optional
	KeepVersions int `json:"keepVersions,omitempty"`
}

// StatusConditionConfig defines a status condition to report
type StatusConditionConfig struct {
	// Name is the condition name (e.g., 'Ready', 'Progressing', 'Available')
	Name string `json:"name"`

	// Status is a template expression for condition status (True/False/Unknown)
	Status string `json:"status"`

	// Reason is a template expression for condition reason
	Reason string `json:"reason"`

	// Message is a template expression for condition message
	Message string `json:"message"`
}

// ControllerConfigStatus defines the observed state of a controller configuration
type ControllerConfigStatus struct {
	// Phase indicates the overall status of the controller configuration
	// +kubebuilder:validation:Enum=Valid;Invalid
	Phase string `json:"phase,omitempty"`

	// Message provides additional status information
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations of the controller config state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed ControllerConfig
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true

// ControllerConfigList contains a list of ControllerConfig
type ControllerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ControllerConfig `json:"items"`
}

// Constants for operators
const (
	OperatorEqual       = "eq"
	OperatorNotEqual    = "ne"
	OperatorIn          = "in"
	OperatorNotIn       = "notin"
	OperatorExists      = "exists"
	OperatorNotExists   = "notexists"
)

// Constants for update strategies
const (
	UpdateStrategyInPlace   = "in_place"
	UpdateStrategyVersioned = "versioned"
)

// Constants for target types
const (
	TargetTypeKubeAPI = "kube-api"
	TargetTypeMaestro = "maestro"
)

// Constants for authentication methods
const (
	AuthMethodKubeConfig      = "kubeconfig"
	AuthMethodWorkloadIdentity = "workload-identity"
)

// Constants for status phases
const (
	PhaseValid   = "Valid"
	PhaseInvalid = "Invalid"
)

// DeepCopyObject implements runtime.Object interface
func (in *ControllerConfig) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopy creates a deep copy of ControllerConfig
func (in *ControllerConfig) DeepCopy() *ControllerConfig {
	if in == nil {
		return nil
	}
	out := new(ControllerConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies all properties from src to dst
func (in *ControllerConfig) DeepCopyInto(out *ControllerConfig) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopyInto copies all properties of ControllerConfigSpec
func (in *ControllerConfigSpec) DeepCopyInto(out *ControllerConfigSpec) {
	*out = *in
	if in.Target != nil {
		in, out := &in.Target, &out.Target
		*out = new(TargetConfig)
		(*in).DeepCopyInto(*out)
	}
	if in.Preconditions != nil {
		in, out := &in.Preconditions, &out.Preconditions
		*out = make([]PreconditionRule, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.Resources != nil {
		in, out := &in.Resources, &out.Resources
		*out = make([]ResourceConfig, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.StatusConditions != nil {
		in, out := &in.StatusConditions, &out.StatusConditions
		*out = make([]StatusConditionConfig, len(*in))
		copy(*out, *in)
	}
}

// DeepCopyInto copies all properties of TargetConfig
func (in *TargetConfig) DeepCopyInto(out *TargetConfig) {
	*out = *in
	if in.KubeConfig != nil {
		in, out := &in.KubeConfig, &out.KubeConfig
		*out = new(KubeConfigReference)
		**out = **in
	}
	if in.MaestroConfig != nil {
		in, out := &in.MaestroConfig, &out.MaestroConfig
		*out = new(MaestroConfig)
		**out = **in
	}
}

// DeepCopyInto copies all properties of PreconditionRule
func (in *PreconditionRule) DeepCopyInto(out *PreconditionRule) {
	*out = *in
	in.Value.DeepCopyInto(&out.Value)
}

// DeepCopyInto copies all properties of ResourceConfig
func (in *ResourceConfig) DeepCopyInto(out *ResourceConfig) {
	*out = *in
	if in.ResourceManagement != nil {
		in, out := &in.ResourceManagement, &out.ResourceManagement
		*out = new(ResourceManagement)
		(*in).DeepCopyInto(*out)
	}
}

// DeepCopyInto copies all properties of ResourceManagement
func (in *ResourceManagement) DeepCopyInto(out *ResourceManagement) {
	*out = *in
}

// DeepCopyInto copies all properties of ControllerConfigStatus
func (in *ControllerConfigStatus) DeepCopyInto(out *ControllerConfigStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopyObject implements runtime.Object interface for ControllerConfigList
func (in *ControllerConfigList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopy creates a deep copy of ControllerConfigList
func (in *ControllerConfigList) DeepCopy() *ControllerConfigList {
	if in == nil {
		return nil
	}
	out := new(ControllerConfigList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies all properties from src to dst for ControllerConfigList
func (in *ControllerConfigList) DeepCopyInto(out *ControllerConfigList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]ControllerConfig, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func init() {
	SchemeBuilder.Register(&ControllerConfig{}, &ControllerConfigList{})
}