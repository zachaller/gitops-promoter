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
	"time"

	promoterConditions "github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/settings"
)

// TimedCommitStatusReconciler reconciles a TimedCommitStatus object
type TimedCommitStatusReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    record.EventRecorder
	SettingsMgr *settings.Manager
}

// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=timedcommitstatuses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=timedcommitstatuses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=timedcommitstatuses/finalizers,verbs=update
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=commitstatuses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *TimedCommitStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling TimedCommitStatus")
	startTime := time.Now()

	var tcs promoterv1alpha1.TimedCommitStatus
	defer utils.HandleReconciliationResult(ctx, startTime, &tcs, r.Client, r.Recorder, &err)

	err = r.Get(ctx, req.NamespacedName, &tcs, &client.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("TimedCommitStatus not found")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get TimedCommitStatus")
		return ctrl.Result{}, fmt.Errorf("failed to get TimedCommitStatus %q: %w", req.Name, err)
	}

	// Remove any existing Ready condition. We want to start fresh.
	meta.RemoveStatusCondition(tcs.GetConditions(), string(promoterConditions.Ready))

	// Get the referenced PromotionStrategy
	var ps promoterv1alpha1.PromotionStrategy
	err = r.Get(ctx, client.ObjectKey{
		Namespace: tcs.Namespace,
		Name:      tcs.Spec.PromotionStrategyRef.Name,
	}, &ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get PromotionStrategy %q: %w", tcs.Spec.PromotionStrategyRef.Name, err)
	}

	// Process each environment and create/update CommitStatus resources
	for _, envConfig := range tcs.Spec.Environment {
		err = r.reconcileEnvironmentCommitStatus(ctx, &tcs, &ps, envConfig)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to reconcile environment %q: %w", envConfig.Branch, err)
		}
	}

	// Requeue to check status periodically
	requeueDuration, err := settings.GetRequeueDuration[promoterv1alpha1.TimedCommitStatusConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get requeue duration for TimedCommitStatus %q: %w", tcs.Name, err)
	}

	return ctrl.Result{
		RequeueAfter: requeueDuration,
	}, nil
}

// reconcileEnvironmentCommitStatus reconciles a CommitStatus resource for a specific environment
func (r *TimedCommitStatusReconciler) reconcileEnvironmentCommitStatus(
	ctx context.Context,
	tcs *promoterv1alpha1.TimedCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
	envConfig promoterv1alpha1.EnvironmentTimeCommitStatus,
) error {
	logger := log.FromContext(ctx)

	// Find the environment in the PromotionStrategy status
	var envStatus *promoterv1alpha1.EnvironmentStatus
	var prevEnvStatus *promoterv1alpha1.EnvironmentStatus

	for i, env := range ps.Status.Environments {
		if env.Branch == envConfig.Branch {
			envStatus = &ps.Status.Environments[i]
			// Get the previous environment (the one before this one in the promotion sequence)
			if i > 0 {
				prevEnvStatus = &ps.Status.Environments[i-1]
			}
			break
		}
	}

	if envStatus == nil {
		return fmt.Errorf("environment %q not found in PromotionStrategy status", envConfig.Branch)
	}

	// Get the dry SHA to track which logical commit we're promoting
	proposedDrySha := envStatus.Proposed.Dry.Sha
	if proposedDrySha == "" {
		logger.Info("No proposed dry SHA found for environment, skipping", "environment", envConfig.Branch)
		return nil
	}

	// Get the active hydrated SHA - this is what we report the commit status on
	// since it's what's actually deployed in this environment
	activeHydratedSha := envStatus.Active.Hydrated.Sha
	if activeHydratedSha == "" {
		logger.Info("No active hydrated SHA found for environment, skipping", "environment", envConfig.Branch)
		return nil
	}

	// Determine the commit status phase based on the time elapsed
	phase := r.determineCommitStatusPhase(proposedDrySha, prevEnvStatus, envConfig.Duration.Duration)

	// Create or update the CommitStatus resource
	commitStatusName := fmt.Sprintf("%s-%s", tcs.Name, envConfig.Branch)
	commitStatus := &promoterv1alpha1.CommitStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      commitStatusName,
			Namespace: tcs.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, commitStatus, func() error {
		// Set owner reference
		if err := controllerutil.SetControllerReference(tcs, commitStatus, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference: %w", err)
		}

		// Update the spec - report on the active hydrated SHA (what's actually deployed)
		commitStatus.Spec = promoterv1alpha1.CommitStatusSpec{
			RepositoryReference: ps.Spec.RepositoryReference,
			Sha:                 activeHydratedSha,
			Name:                fmt.Sprintf("promoter/timed/%s", envConfig.Branch),
			Description:         r.getCommitStatusDescription(phase, envConfig.Duration.Duration, prevEnvStatus),
			Phase:               phase,
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update CommitStatus for environment %q: %w", envConfig.Branch, err)
	}

	logger.Info("Reconciled CommitStatus for environment",
		"environment", envConfig.Branch,
		"proposedDrySha", proposedDrySha,
		"activeHydratedSha", activeHydratedSha,
		"phase", phase,
		"commitStatus", commitStatusName)

	return nil
}

// determineCommitStatusPhase determines the phase of the commit status based on the time elapsed
// since the previous environment's PR was merged.
// It uses the dry SHA to track the logical commit through environments.
func (r *TimedCommitStatusReconciler) determineCommitStatusPhase(
	proposedDrySha string,
	prevEnvStatus *promoterv1alpha1.EnvironmentStatus,
	duration time.Duration,
) promoterv1alpha1.CommitStatusPhase {
	// If this is the first environment (no previous environment), allow promotion immediately
	if prevEnvStatus == nil {
		return promoterv1alpha1.CommitPhaseSuccess
	}

	// Check if the previous environment has a merged PR
	if prevEnvStatus.PullRequest == nil || prevEnvStatus.PullRequest.State != promoterv1alpha1.PullRequestMerged {
		// Previous environment hasn't been merged yet, so this environment should wait
		return promoterv1alpha1.CommitPhasePending
	}

	// Find the merge time from the previous environment using the dry SHA
	// The dry SHA is what identifies the logical commit across environments
	var mergeTime *metav1.Time

	// First, check if we can find the SHA in the last healthy dry SHAs
	//for _, healthySha := range prevEnvStatus.LastHealthyDryShas {
	//	if healthySha.Sha == proposedDrySha {
	//		mergeTime = &healthySha.Time
	//		break
	//	}
	//}

	// If not found in LastHealthyDryShas, check the history for matching dry SHA
	if len(prevEnvStatus.History) > 0 {
		for _, hist := range prevEnvStatus.History {
			if hist.Active.Dry.Sha == proposedDrySha && hist.PullRequest != nil {
				mergeTime = &hist.PullRequest.PRMergeTime
				break
			}
		}
	}

	// If we can't find when this dry SHA was merged in the previous environment,
	// it means it hasn't been promoted there yet
	if mergeTime == nil {
		return promoterv1alpha1.CommitPhasePending
	}

	// Calculate time elapsed since the merge
	timeElapsed := time.Since(mergeTime.Time)

	// If the configured duration has passed, allow promotion
	if timeElapsed >= duration {
		return promoterv1alpha1.CommitPhaseSuccess
	}

	// Still waiting for the time gate
	return promoterv1alpha1.CommitPhasePending
}

// getCommitStatusDescription generates a human-readable description for the commit status
func (r *TimedCommitStatusReconciler) getCommitStatusDescription(
	phase promoterv1alpha1.CommitStatusPhase,
	duration time.Duration,
	prevEnvStatus *promoterv1alpha1.EnvironmentStatus,
) string {
	if prevEnvStatus == nil {
		return "First environment - promotion allowed immediately"
	}

	switch phase {
	case promoterv1alpha1.CommitPhaseSuccess:
		return fmt.Sprintf("Time gate passed - %s elapsed since previous environment merge", duration)
	case promoterv1alpha1.CommitPhasePending:
		if prevEnvStatus.PullRequest == nil || prevEnvStatus.PullRequest.State != promoterv1alpha1.PullRequestMerged {
			return fmt.Sprintf("Waiting for previous environment to be merged (requires %s wait time)", duration)
		}
		return fmt.Sprintf("Waiting for %s to elapse since previous environment merge", duration)
	default:
		return fmt.Sprintf("Unknown phase: %s", phase)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *TimedCommitStatusReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	// Use Direct methods to read configuration from the API server without cache during setup.
	// The cache is not started during SetupWithManager, so we must use the non-cached API reader.
	rateLimiter, err := settings.GetRateLimiterDirect[promoterv1alpha1.TimedCommitStatusConfiguration, ctrl.Request](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get TimedCommitStatus rate limiter: %w", err)
	}

	maxConcurrentReconciles, err := settings.GetMaxConcurrentReconcilesDirect[promoterv1alpha1.TimedCommitStatusConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get TimedCommitStatus max concurrent reconciles: %w", err)
	}

	err = ctrl.NewControllerManagedBy(mgr).
		For(&promoterv1alpha1.TimedCommitStatus{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles, RateLimiter: rateLimiter}).
		Complete(r)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}
	return nil
}
