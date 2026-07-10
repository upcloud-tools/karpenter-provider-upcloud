package v1alpha1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
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
	if err := SchemeBuilder.AddToScheme(scheme.Scheme); err != nil {
		panic(fmt.Sprintf("adding UpCloudNodeClass to scheme: %v", err))
	}
}
