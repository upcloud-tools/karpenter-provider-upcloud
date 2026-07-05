package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	Group   = "karpenter.upcloud.com"
	Version = "v1alpha1"
)

var (
	GroupVersion  = schema.GroupVersion{Group: Group, Version: Version}
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		metav1.AddToGroupVersion(s, GroupVersion)
		s.AddKnownTypes(GroupVersion,
			&UpCloudNodeClass{},
			&UpCloudNodeClassList{},
		)
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.AddToScheme(scheme.Scheme)
}
