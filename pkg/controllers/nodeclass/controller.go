package nodeclass

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/awslabs/operatorpkg/status"
	apiv1 "github.com/upcloud-tools/karpenter-provider-upcloud/apis/v1alpha2"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	nodeclaimutils "sigs.k8s.io/karpenter/pkg/utils/nodeclaim"
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

	if !slices.Contains(nodeClass.Finalizers, apiv1.NodeClassFinalizer) {
		nodeClass.Finalizers = append(nodeClass.Finalizers, apiv1.NodeClassFinalizer)
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
	nodeClass.Status.Hash = nodeClass.Hash()

	if !equality.Semantic.DeepEqual(stored, nodeClass) {
		if err := r.Status().Patch(ctx, nodeClass, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}
			return reconcile.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("failed to patch nodeclass status: %w", err)
		}
	}

	if err := r.rebaselineClaimHashes(ctx, nodeClass); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: 5 * time.Minute}, nil
}

// rebaselineClaimHashes brings every launched NodeClaim's hash annotations up to date:
//   - Claims stamped by a superseded hash algorithm get the live hash and current version re-stamped. Without this, a change
//     to how the hash is computed would look like drift on every node at once and recycle the whole fleet over a change
//     internal to Karpenter.
//   - Claims that never carried a hash (launched before drift detection shipped) are adopted the same way, so no node stays
//     permanently outside drift detection.
//
// Claims evaluated as Drifted keep their stored hash so genuine drift survives the pass. Claims that have not launched yet
// (no providerID) are skipped: Create() stamps their hash as part of provisioning, and writing it earlier would race with that.
func (r *Controller) rebaselineClaimHashes(ctx context.Context, nodeClass *apiv1.UpCloudNodeClass) error {
	nodeClaims := &karpv1.NodeClaimList{}
	if err := r.List(ctx, nodeClaims, nodeclaimutils.ForNodeClass(nodeClass)); err != nil {
		return fmt.Errorf("listing nodeclaims for hash re-baseline: %w", err)
	}

	for i := range nodeClaims.Items {
		claim := &nodeClaims.Items[i]
		_, hasHash := claim.Annotations[apiv1.NodeClassHashAnnotationKey]
		if !hasHash && claim.Status.ProviderID == "" {
			continue
		}
		if hasHash && claim.Annotations[apiv1.NodeClassHashVersionAnnotationKey] == apiv1.NodeClassHashVersion {
			continue
		}
		if drifted := claim.StatusConditions().Get(karpv1.ConditionTypeDrifted); drifted != nil && drifted.IsTrue() {
			continue
		}

		stored := claim.DeepCopy()
		if claim.Annotations == nil {
			claim.Annotations = map[string]string{}
		}
		claim.Annotations[apiv1.NodeClassHashAnnotationKey] = nodeClass.Hash()
		claim.Annotations[apiv1.NodeClassHashVersionAnnotationKey] = apiv1.NodeClassHashVersion
		if err := r.Patch(ctx, claim, client.MergeFrom(stored)); err != nil {
			return fmt.Errorf("re-baselining nodeclaim hash %s: %w", claim.Name, err)
		}
	}
	return nil
}

func (r *Controller) handleDeletion(ctx context.Context, nodeClass *apiv1.UpCloudNodeClass) (reconcile.Result, error) {
	if !slices.Contains(nodeClass.Finalizers, apiv1.NodeClassFinalizer) {
		return reconcile.Result{}, nil
	}

	log.FromContext(ctx).Info("handling UpCloudNodeClass deletion")

	nodeClass.Finalizers = slices.DeleteFunc(nodeClass.Finalizers, func(s string) bool {
		return s == apiv1.NodeClassFinalizer
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
