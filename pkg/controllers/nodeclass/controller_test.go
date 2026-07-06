package nodeclass

import (
	"context"
	"testing"

	"github.com/awslabs/operatorpkg/status"
	apiv1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func buildClient(t *testing.T, nc *apiv1.UpCloudNodeClass) crfake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := apiv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add upcloud scheme: %v", err)
	}
	return *crfake.NewClientBuilder().WithScheme(scheme).WithObjects(nc).WithStatusSubresource(&apiv1.UpCloudNodeClass{})
}

func reconcileAndGet(t *testing.T, nc *apiv1.UpCloudNodeClass) *apiv1.UpCloudNodeClass {
	t.Helper()
	builder := buildClient(t, nc)
	client := builder.Build()
	r := &Controller{Client: client}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: nc.Name}}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	got := &apiv1.UpCloudNodeClass{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: nc.Name}, got); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	return got
}

func TestReconcileValidSpecReady(t *testing.T) {
	nc := &apiv1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "valid", Finalizers: []string{Finalizer}},
		Spec:       apiv1.UpCloudNodeClassSpec{Zone: "de-fra1", Plan: "2xCPU-4GB"},
	}
	got := reconcileAndGet(t, nc)
	cond := got.StatusConditions().Get(status.ConditionReady)
	if cond == nil || !cond.IsTrue() {
		t.Errorf("expected Ready=True for valid spec, got %+v", cond)
	}
	if got.Status.Hash == "" {
		t.Errorf("expected resolved hash to be stored on status")
	}
	if got.Status.Hash != nc.Hash() {
		t.Errorf("expected stored hash %q to match computed hash %q", got.Status.Hash, nc.Hash())
	}
}

func TestReconcileMissingZoneNotReady(t *testing.T) {
	nc := &apiv1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid", Finalizers: []string{Finalizer}},
		Spec:       apiv1.UpCloudNodeClassSpec{Plan: "2xCPU-4GB"},
	}
	got := reconcileAndGet(t, nc)
	cond := got.StatusConditions().Get(status.ConditionReady)
	if cond == nil || !cond.IsFalse() {
		t.Errorf("expected Ready=False for missing zone, got %+v", cond)
	}
}

func TestReconcileMissingPlanNotReady(t *testing.T) {
	nc := &apiv1.UpCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid2", Finalizers: []string{Finalizer}},
		Spec:       apiv1.UpCloudNodeClassSpec{Zone: "de-fra1"},
	}
	got := reconcileAndGet(t, nc)
	cond := got.StatusConditions().Get(status.ConditionReady)
	if cond == nil || !cond.IsFalse() {
		t.Errorf("expected Ready=False for missing plan, got %+v", cond)
	}
}
