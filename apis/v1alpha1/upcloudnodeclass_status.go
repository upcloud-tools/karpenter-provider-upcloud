package v1alpha1

import (
	"github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ConditionTypeReady = "Ready"
)

var nodeClassConditionTypes = status.NewReadyConditions()

func (u *UpCloudNodeClass) GetConditions() []status.Condition {
	conditions := make([]status.Condition, len(u.Status.Conditions))
	for i := range u.Status.Conditions {
		conditions[i] = status.Condition(u.Status.Conditions[i])
	}
	return conditions
}

func (u *UpCloudNodeClass) SetConditions(conditions []status.Condition) {
	u.Status.Conditions = make([]metav1.Condition, len(conditions))
	for i := range conditions {
		u.Status.Conditions[i] = metav1.Condition(conditions[i])
	}
}

func (u *UpCloudNodeClass) StatusConditions(opts ...status.ForOption) status.ConditionSet {
	return nodeClassConditionTypes.For(u, opts...)
}
