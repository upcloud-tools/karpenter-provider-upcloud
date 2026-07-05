package nodeclass

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/awslabs/operatorpkg/status"
	apiv1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	Finalizer = "upcloud.com/nodeclass-finalizer"
)

type Controller struct {
	client.Client
}

func (r *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("nodeclass", req.NamespacedName))

	nodeClass := &apiv1.UpCloudNodeClass{}
	if err := r.Get(ctx, req.NamespacedName, nodeClass); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if !nodeClass.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, nodeClass)
	}

	stored := nodeClass.DeepCopy()

	if !slices.Contains(nodeClass.Finalizers, Finalizer) {
		nodeClass.Finalizers = append(nodeClass.Finalizers, Finalizer)
		if err := r.Update(ctx, nodeClass); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{RequeueAfter: time.Minute}, nil
	}

	if err := r.validateSpec(nodeClass); err != nil {
		log.FromContext(ctx).Error(err, "validation failed")
		nodeClass.StatusConditions().SetFalse(status.ConditionReady, "ValidationFailed", "Validation failed: "+err.Error())
		if err := r.Status().Patch(ctx, nodeClass, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	log.FromContext(ctx).Info("validation succeeded")
	nodeClass.StatusConditions().SetTrue(status.ConditionReady)

	if !equality.Semantic.DeepEqual(stored, nodeClass) {
		if err := r.Status().Patch(ctx, nodeClass, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}
			return reconcile.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("failed to patch nodeclass status: %w", err)
		}
	}

	return reconcile.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *Controller) handleDeletion(ctx context.Context, nodeClass *apiv1.UpCloudNodeClass) (reconcile.Result, error) {
	if !slices.Contains(nodeClass.Finalizers, Finalizer) {
		return reconcile.Result{}, nil
	}

	log.FromContext(ctx).Info("handling UpCloudNodeClass deletion")

	nodeClass.Finalizers = slices.DeleteFunc(nodeClass.Finalizers, func(s string) bool {
		return s == Finalizer
	})
	if err := r.Update(ctx, nodeClass); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *Controller) validateSpec(nodeClass *apiv1.UpCloudNodeClass) error {
	if nodeClass.Spec.Zone == "" {
		return fmt.Errorf("zone is required")
	}
	if nodeClass.Spec.Plan == "" {
		return fmt.Errorf("plan is required")
	}
	return nil
}

func (r *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.UpCloudNodeClass{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 5}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
