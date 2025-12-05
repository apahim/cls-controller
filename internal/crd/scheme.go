package crd

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "cls.redhat.com", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(&ControllerConfig{}, &ControllerConfigList{})
}

// EnsureTypesRegistered explicitly registers types with the scheme
func EnsureTypesRegistered(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &ControllerConfig{}, &ControllerConfigList{})
	return nil
}
