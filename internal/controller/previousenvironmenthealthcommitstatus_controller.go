/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"gopkg.in/yaml.v3"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/settings"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// PreviousEnvironmentHealthCommitStatusReconciler reconciles PromotionStrategy objects
// to create CommitStatus resources that reflect the health of the previous environment.
// This allows gating promotions based on the health of the prior environment in a
// promotion sequence.
type PreviousEnvironmentHealthCommitStatusReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    record.EventRecorder
	SettingsMgr *settings.Manager
}

//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies,verbs=get;list;watch
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=changetransferpolicies,verbs=get;list;watch
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=commitstatuses,verbs=get;list;watch;create;update;patch;delete

// Reconcile looks at each PromotionStrategy and for each non-first environment,
// evaluates whether the previous environment is healthy. It then creates or updates
// a CommitStatus resource on the current environment's proposed commit reflecting
// the previous environment's health status.
func (r *PreviousEnvironmentHealthCommitStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling PreviousEnvironmentHealthCommitStatus")

	var ps promoterv1alpha1.PromotionStrategy
	err := r.Get(ctx, req.NamespacedName, &ps, &client.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Info("PromotionStrategy not found")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get PromotionStrategy")
		return ctrl.Result{}, fmt.Errorf("failed to get PromotionStrategy %q: %w", req.Name, err)
	}

	// List CTPs owned by this PromotionStrategy, ordered by environment
	ctps, err := r.getOrderedCTPs(ctx, &ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get ordered CTPs: %w", err)
	}

	err = r.updatePreviousEnvironmentCommitStatus(ctx, &ps, ctps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update previous environment commit status: %w", err)
	}

	requeueDuration, err := settings.GetRequeueDuration[promoterv1alpha1.PromotionStrategyConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get requeue duration: %w", err)
	}

	return ctrl.Result{
		Requeue:      true,
		RequeueAfter: requeueDuration,
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PreviousEnvironmentHealthCommitStatusReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	// Use Direct methods to read configuration from the API server without cache during setup.
	rateLimiter, err := settings.GetRateLimiterDirect[promoterv1alpha1.PromotionStrategyConfiguration, ctrl.Request](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get PreviousEnvironmentHealthCommitStatus rate limiter: %w", err)
	}

	maxConcurrentReconciles, err := settings.GetMaxConcurrentReconcilesDirect[promoterv1alpha1.PromotionStrategyConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get PreviousEnvironmentHealthCommitStatus max concurrent reconciles: %w", err)
	}

	err = ctrl.NewControllerManagedBy(mgr).
		Named("previousenvironmenthealthcommitstatus").
		Watches(&promoterv1alpha1.PromotionStrategy{}, &handler.EnqueueRequestForObject{}).
		Watches(&promoterv1alpha1.ChangeTransferPolicy{}, handler.EnqueueRequestsFromMapFunc(r.enqueuePromotionStrategyForCTP())).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles, RateLimiter: rateLimiter}).
		Complete(r)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}
	return nil
}

// enqueuePromotionStrategyForCTP returns a MapFunc that finds the owning PromotionStrategy
// of a ChangeTransferPolicy and enqueues it for reconciliation.
func (r *PreviousEnvironmentHealthCommitStatusReconciler) enqueuePromotionStrategyForCTP() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []ctrl.Request {
		ctp, ok := obj.(*promoterv1alpha1.ChangeTransferPolicy)
		if !ok {
			return nil
		}

		// Find the owning PromotionStrategy from the owner references
		for _, ref := range ctp.GetOwnerReferences() {
			if ref.Kind == "PromotionStrategy" {
				return []ctrl.Request{
					{
						NamespacedName: client.ObjectKey{
							Namespace: ctp.Namespace,
							Name:      ref.Name,
						},
					},
				}
			}
		}

		return nil
	}
}

// getOrderedCTPs returns the CTPs for a PromotionStrategy, ordered to match ps.Spec.Environments.
func (r *PreviousEnvironmentHealthCommitStatusReconciler) getOrderedCTPs(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy) ([]*promoterv1alpha1.ChangeTransferPolicy, error) {
	var ctpList promoterv1alpha1.ChangeTransferPolicyList
	err := r.List(ctx, &ctpList, client.InNamespace(ps.Namespace), client.MatchingLabels{
		promoterv1alpha1.PromotionStrategyLabel: utils.KubeSafeLabel(ps.Name),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list ChangeTransferPolicies: %w", err)
	}

	// Build a map from expected CTP name to CTP for quick lookup
	ctpMap := make(map[string]*promoterv1alpha1.ChangeTransferPolicy, len(ctpList.Items))
	for i := range ctpList.Items {
		ctpMap[ctpList.Items[i].Name] = &ctpList.Items[i]
	}

	// Order CTPs to match environments
	ctps := make([]*promoterv1alpha1.ChangeTransferPolicy, len(ps.Spec.Environments))
	for i, env := range ps.Spec.Environments {
		expectedName := utils.KubeSafeUniqueName(ctx, utils.GetChangeTransferPolicyName(ps.Name, env.Branch))
		ctp, found := ctpMap[expectedName]
		if !found {
			// CTP not yet created, skip
			continue
		}
		ctps[i] = ctp
	}

	return ctps, nil
}

// ensurePreviousEnvironmentSelector ensures that the CTP has the PreviousEnvironmentCommitStatusKey
// in its ProposedCommitStatuses. This is needed so the CTP knows to wait for the previous environment
// health check before merging. The selector is added via patch so it doesn't conflict with the
// PromotionStrategy controller's CreateOrUpdate (which preserves externally-managed selectors).
func (r *PreviousEnvironmentHealthCommitStatusReconciler) ensurePreviousEnvironmentSelector(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy) error {
	selector := promoterv1alpha1.CommitStatusSelector{
		Key: promoterv1alpha1.PreviousEnvironmentCommitStatusKey,
	}

	if slices.Contains(ctp.Spec.ProposedCommitStatuses, selector) {
		return nil
	}

	patch := client.MergeFrom(ctp.DeepCopy())
	ctp.Spec.ProposedCommitStatuses = append(ctp.Spec.ProposedCommitStatuses, selector)
	return r.Patch(ctx, ctp, patch)
}

// updatePreviousEnvironmentCommitStatus checks each non-first environment and creates/updates
// a CommitStatus resource on the current environment's proposed commit based on the previous
// environment's health.
func (r *PreviousEnvironmentHealthCommitStatusReconciler) updatePreviousEnvironmentCommitStatus(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy, ctps []*promoterv1alpha1.ChangeTransferPolicy) error {
	logger := log.FromContext(ctx)

	for i, ctp := range ctps {
		if i == 0 {
			// Skip, there's no previous environment.
			continue
		}

		if ctp == nil || ctps[i-1] == nil {
			// CTP not yet created, skip
			continue
		}

		if len(ps.Spec.ActiveCommitStatuses) == 0 && len(ps.Spec.Environments[i-1].ActiveCommitStatuses) == 0 {
			// Skip, there aren't any active commit statuses configured for the PromotionStrategy or the previous environment.
			continue
		}

		// Ensure the CTP has the proposed commit status selector so it waits for
		// the previous environment health check before merging.
		if err := r.ensurePreviousEnvironmentSelector(ctx, ctp); err != nil {
			return fmt.Errorf("failed to ensure previous environment selector on CTP %s: %w", ctp.Name, err)
		}

		previousEnvironmentStatus := getEnvironmentStatusFromCTP(ctps[i-1])
		currentEnvironmentStatus := getEnvironmentStatusFromCTP(ctp)

		// Skip if there's no proposed change in the current environment (i.e., active and proposed are the same).
		// In this case, there's no PR to put a commit status on, so we shouldn't create/update one.
		// This prevents updating commit status on already-merged PRs when the previous environment state changes.
		if ctp.Status.Active.Dry.Sha == ctp.Status.Proposed.Dry.Sha {
			logger.V(4).Info("Skipping previous environment commit status update - no proposed change in current environment",
				"activeBranch", ctp.Spec.ActiveBranch,
				"activeDrySha", ctp.Status.Active.Dry.Sha,
				"proposedDrySha", ctp.Status.Proposed.Dry.Sha,
				"previousEnvironmentActiveDrySha", previousEnvironmentStatus.Active.Dry.Sha,
				"currentEnvironmentActiveDrySha", ctp.Status.Proposed.Dry.Sha,
			)
			continue
		}

		isPending, pendingReason := isPreviousEnvironmentPending(previousEnvironmentStatus, currentEnvironmentStatus)

		commitStatusPhase := promoterv1alpha1.CommitPhaseSuccess
		if isPending {
			commitStatusPhase = promoterv1alpha1.CommitPhasePending
		}

		logger.V(4).Info("Setting previous environment CommitStatus phase",
			"phase", commitStatusPhase,
			"pendingReason", pendingReason,
			"activeBranch", ctp.Spec.ActiveBranch,
			"proposedDrySha", ctp.Status.Proposed.Dry.Sha,
			"proposedHydratedSha", ctp.Status.Proposed.Hydrated.Sha,
			"previousEnvironmentActiveDrySha", previousEnvironmentStatus.Active.Dry.Sha,
			"previousEnvironmentActiveHydratedSha", previousEnvironmentStatus.Active.Hydrated.Sha,
			"previousEnvironmentProposedDrySha", previousEnvironmentStatus.Proposed.Dry.Sha,
			"previousEnvironmentProposedNoteSha", getNoteDrySha(previousEnvironmentStatus.Proposed.Note),
			"previousEnvironmentActiveBranch", previousEnvironmentStatus.Branch)

		_, err := r.createOrUpdatePreviousEnvironmentCommitStatus(ctx, ctp, commitStatusPhase, pendingReason, previousEnvironmentStatus.Branch, ctps[i-1].Status.Active.CommitStatuses)
		if err != nil {
			return fmt.Errorf("failed to create or update previous environment commit status for branch %s: %w", ctp.Spec.ActiveBranch, err)
		}
	}

	return nil
}

// getEnvironmentStatusFromCTP constructs an EnvironmentStatus from a CTP's status.
func getEnvironmentStatusFromCTP(ctp *promoterv1alpha1.ChangeTransferPolicy) promoterv1alpha1.EnvironmentStatus {
	return promoterv1alpha1.EnvironmentStatus{
		Branch:   ctp.Spec.ActiveBranch,
		Active:   ctp.Status.Active,
		Proposed: ctp.Status.Proposed,
	}
}

func (r *PreviousEnvironmentHealthCommitStatusReconciler) createOrUpdatePreviousEnvironmentCommitStatus(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy, phase promoterv1alpha1.CommitStatusPhase, pendingReason string, previousEnvironmentBranch string, previousCRPCSPhases []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase) (*promoterv1alpha1.CommitStatus, error) {
	logger := log.FromContext(ctx)

	csName := utils.KubeSafeUniqueName(ctx, promoterv1alpha1.PreviousEnvProposedCommitPrefixNameLabel+ctp.Name)
	proposedCSObjectKey := client.ObjectKey{Namespace: ctp.Namespace, Name: csName}

	kind := reflect.TypeOf(promoterv1alpha1.ChangeTransferPolicy{}).Name()
	gvk := promoterv1alpha1.GroupVersion.WithKind(kind)
	controllerRef := metav1.NewControllerRef(ctp, gvk)

	// If there is only one commit status, use the URL from that commit status.
	var url string
	if len(previousCRPCSPhases) == 1 {
		url = previousCRPCSPhases[0].Url
	}

	statusMap := make(map[string]string)
	for _, status := range previousCRPCSPhases {
		statusMap[status.Key] = status.Phase
	}
	yamlStatusMap, err := yaml.Marshal(statusMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal previous environment commit statuses: %w", err)
	}

	description := previousEnvironmentBranch + " - synced and healthy"
	if phase == promoterv1alpha1.CommitPhasePending && pendingReason != "" {
		description = pendingReason
	}

	commitStatus := &promoterv1alpha1.CommitStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      proposedCSObjectKey.Name,
			Namespace: proposedCSObjectKey.Namespace,
		},
	}

	res, err := controllerutil.CreateOrUpdate(ctx, r.Client, commitStatus, func() error {
		commitStatus.Labels = map[string]string{
			promoterv1alpha1.CommitStatusLabel: promoterv1alpha1.PreviousEnvironmentCommitStatusKey,
		}
		commitStatus.Annotations = map[string]string{
			promoterv1alpha1.CommitStatusPreviousEnvironmentStatusesAnnotation: string(yamlStatusMap),
		}
		commitStatus.OwnerReferences = []metav1.OwnerReference{*controllerRef}
		commitStatus.Spec.RepositoryReference = ctp.Spec.RepositoryReference
		commitStatus.Spec.Sha = ctp.Status.Proposed.Hydrated.Sha
		commitStatus.Spec.Name = previousEnvironmentBranch + " - synced and healthy"
		commitStatus.Spec.Description = description
		commitStatus.Spec.Phase = phase
		commitStatus.Spec.Url = url
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create or update previous environments CommitStatus: %w", err)
	}
	logger.V(4).Info("CreateOrUpdate previous environment CommitStatus result", "result", res)

	return commitStatus, nil
}

// getNoteDrySha safely returns the DrySha from a HydratorMetadata pointer, or empty string if nil.
func getNoteDrySha(note *promoterv1alpha1.HydratorMetadata) string {
	if note == nil {
		return ""
	}
	return note.DrySha
}

// isPreviousEnvironmentPending returns whether the previous environment is pending and a reason string if it is pending.
func isPreviousEnvironmentPending(previousEnvironmentStatus, currentEnvironmentStatus promoterv1alpha1.EnvironmentStatus) (isPending bool, reason string) {
	previousEnvProposedNoteSha := getNoteDrySha(previousEnvironmentStatus.Proposed.Note)
	previousEnvProposedDrySha := previousEnvironmentStatus.Proposed.Dry.Sha

	// Determine which dry SHA each environment's hydrator has processed.
	// The Note.DrySha (from git note) is the authoritative source because when manifests don't change
	// between dry commits, the hydrator may only update the git note without creating a new commit.
	// In that case, hydrator.metadata (Proposed.Dry.Sha) still has the old SHA, but the git note
	// confirms hydration is complete for the new dry SHA.
	// For legacy hydrators that don't use git notes, fall back to Proposed.Dry.Sha.
	previousEnvHydratedForDrySha := previousEnvProposedNoteSha
	if previousEnvHydratedForDrySha == "" {
		previousEnvHydratedForDrySha = previousEnvProposedDrySha
	}
	currentEnvHydratedForDrySha := getNoteDrySha(currentEnvironmentStatus.Proposed.Note)
	if currentEnvHydratedForDrySha == "" {
		currentEnvHydratedForDrySha = currentEnvironmentStatus.Proposed.Dry.Sha
	}

	// Check if hydrator has processed the same dry SHA as the current environment.
	if previousEnvHydratedForDrySha != currentEnvHydratedForDrySha {
		return true, "Waiting for the hydrator to finish processing the proposed dry commit"
	}

	// Check if the previous environment has completed its promotion.
	// There are two ways promotion can be "complete":
	//
	// 1. prMerged: A PR was created and merged, so Active.Dry.Sha now matches the target.
	//
	// 2. noOpHydration: The hydrator determined the manifests were unchanged between the
	//    old and new dry commits, so it only updated the git note (Note.DrySha) without creating
	//    a new hydrated commit. We detect this by comparing:
	//    - previousEnvHydratedForDrySha: The dry SHA the hydrator has processed (from Note.DrySha)
	//    - previousEnvProposedDrySha: The dry SHA in hydrator.metadata (Proposed.Dry.Sha)
	//    When these differ, it means the git note was updated to a newer dry SHA, but
	//    hydrator.metadata still has the old value because no new commit was created.
	//    In this case, there's no PR to merge, so we shouldn't block waiting for one.
	//
	prMerged := previousEnvironmentStatus.Active.Dry.Sha == currentEnvHydratedForDrySha
	noOpHydration := previousEnvProposedDrySha != previousEnvHydratedForDrySha
	promotionComplete := prMerged || noOpHydration
	if !promotionComplete {
		return true, "Waiting for previous environment to be promoted"
	}

	// Only check commit times if the previous environment actually merged the exact SHA (not no-op).
	prWasMerged := previousEnvironmentStatus.Active.Dry.Sha == currentEnvHydratedForDrySha
	if prWasMerged {
		previousEnvironmentDryShaEqualOrNewer := previousEnvironmentStatus.Active.Dry.CommitTime.Equal(&metav1.Time{Time: currentEnvironmentStatus.Active.Dry.CommitTime.Time}) ||
			previousEnvironmentStatus.Active.Dry.CommitTime.After(currentEnvironmentStatus.Active.Dry.CommitTime.Time)
		if !previousEnvironmentDryShaEqualOrNewer {
			// This should basically never happen.
			return true, "Previous environment's commit is older than current environment's commit"
		}
	}

	// Finally, check that the previous environment's commit statuses are passing.
	previousEnvironmentPassing := utils.AreCommitStatusesPassing(previousEnvironmentStatus.Active.CommitStatuses)
	if !previousEnvironmentPassing {
		if len(previousEnvironmentStatus.Active.CommitStatuses) == 1 {
			return true, fmt.Sprintf("Waiting for previous environment's %q commit status to be successful", previousEnvironmentStatus.Active.CommitStatuses[0].Key)
		}
		return true, "Waiting for previous environment's commit statuses to be successful"
	}

	return false, ""
}
