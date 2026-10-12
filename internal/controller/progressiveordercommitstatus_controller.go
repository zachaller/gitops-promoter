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
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	acmetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	acv1alpha1 "github.com/argoproj-labs/gitops-promoter/applyconfiguration/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/settings"
	promoterConditions "github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

// ProgressiveOrderURLTemplateData is the data passed to ProgressiveOrderCommitStatus.spec.url.template.
type ProgressiveOrderURLTemplateData struct {
	ProgressiveOrderCommitStatus promoterv1alpha1.ProgressiveOrderCommitStatus
	PromotionStrategy            *promoterv1alpha1.PromotionStrategy
	Environment                  string
	// DependsOnQuery is DependsOn encoded as repeated env= query parameters
	// (e.g. "env=e2e&env=perf"), ready to append after "?". Empty when DependsOn is empty.
	DependsOnQuery string
	// DependsOn is the current environment's immediate upstream branches (one edge away),
	// from the resolved dependency graph on the PromotionStrategy.
	DependsOn []string
}

// ProgressiveOrderCommitStatusReconciler reconciles a ProgressiveOrderCommitStatus object
type ProgressiveOrderCommitStatusReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    events.EventRecorder
	SettingsMgr *settings.Manager
}

// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=progressiveordercommitstatuses,verbs=get;list;watch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=progressiveordercommitstatuses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=progressiveordercommitstatuses/finalizers,verbs=update
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=commitstatuses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies,verbs=get;list;watch

// Reconcile reads the referenced PromotionStrategy and, using the dependency graph declared on it,
// reports per environment whether every dependsOn upstream has made healthy progress beyond what the
// environment is running.
//
// Unlike DependentsSuccessfulCommitStatus, which requires upstreams to be healthy on the exact dry SHA
// an environment is promoting, this gate keeps a persisted progress watermark per environment (the newest
// dry commit it was observed healthy on). An environment may promote once each upstream's watermark is newer
// than the environment's active dry commit and the upstream is not currently failing. In high-churn
// repositories upstreams rarely settle healthy on the latest commit before the next one arrives; the
// watermark lets higher environments keep advancing instead of waiting for a quiet period.
func (r *ProgressiveOrderCommitStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := logf.FromContext(ctx)
	logger.Info("Reconciling ProgressiveOrderCommitStatus")
	startTime := time.Now()

	var pocs promoterv1alpha1.ProgressiveOrderCommitStatus
	// This applies the resource status via Server-Side Apply at the end of reconciliation. Don't write status manually.
	var previousReady *metav1.Condition
	defer utils.HandleReconciliationResult(ctx, startTime, &pocs, r.Client, r.Recorder, constants.ProgressiveOrderCommitStatusControllerFieldOwner, &result, &err, &previousReady)

	if err = r.Get(ctx, req.NamespacedName, &pocs); err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Info("ProgressiveOrderCommitStatus not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get ProgressiveOrderCommitStatus %q: %w", req.Name, err)
	}

	// Start fresh on the Ready condition each reconcile.
	previousReady = utils.RemoveReadyCondition(&pocs)

	var ps promoterv1alpha1.PromotionStrategy
	psKey := client.ObjectKey{Namespace: pocs.Namespace, Name: pocs.Spec.PromotionStrategyRef.Name}
	if err = r.Get(ctx, psKey, &ps); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("referenced PromotionStrategy %q not found: %w", pocs.Spec.PromotionStrategyRef.Name, err)
		}
		return ctrl.Result{}, fmt.Errorf("failed to get PromotionStrategy %q: %w", pocs.Spec.PromotionStrategyRef.Name, err)
	}

	if err = r.updateProgressiveOrderCommitStatus(ctx, &pocs, &ps, metav1.Now()); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update progressive order commit statuses: %w", err)
	}

	requeueDuration, err := settings.GetRequeueDuration[promoterv1alpha1.ProgressiveOrderCommitStatusConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get requeue duration for ProgressiveOrderCommitStatus %q: %w", pocs.Name, err)
	}

	return ctrl.Result{RequeueAfter: requeueDuration}, nil
}

// updateProgressiveOrderCommitStatus builds and validates the dependency graph from the PromotionStrategy,
// advances each environment's progress watermark, and writes a per-environment CommitStatus: success once
// every dependsOn upstream has progressed past the environment's active dry commit, pending otherwise.
func (r *ProgressiveOrderCommitStatusReconciler) updateProgressiveOrderCommitStatus(ctx context.Context, pocs *promoterv1alpha1.ProgressiveOrderCommitStatus, ps *promoterv1alpha1.PromotionStrategy, now metav1.Time) error {
	environments, err := resolveDependentEnvironments(ps)
	if err != nil {
		return err
	}
	graph, err := buildDAG(environments)
	if err != nil {
		return fmt.Errorf("failed to build dependency graph: %w", err)
	}
	if err := graph.validateDAG(); err != nil {
		return fmt.Errorf("invalid dependency graph: %w", err)
	}

	statusByBranch := make(map[string]promoterv1alpha1.EnvironmentStatus, len(ps.Status.Environments))
	for _, envStatus := range ps.Status.Environments {
		statusByBranch[envStatus.Branch] = envStatus
	}

	previousWatermarks := make(map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark, len(pocs.Status.Environments))
	for _, envStatus := range pocs.Status.Environments {
		previousWatermarks[envStatus.Branch] = envStatus.LastHealthyDry
	}

	// Advance every watermark before evaluating any gate so each environment is judged against its
	// upstreams' latest observed progress.
	watermarks := make(map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark, len(graph.branches))
	for _, branch := range graph.branches {
		envStatus, ok := statusByBranch[branch]
		if !ok {
			watermarks[branch] = previousWatermarks[branch]
			continue
		}
		watermarks[branch] = advanceProgressWatermark(previousWatermarks[branch], envStatus, now)
	}

	logger := logf.FromContext(ctx)
	commitStatuses := make([]*promoterv1alpha1.CommitStatus, 0, len(graph.branches))
	pocs.Status.Environments = make([]promoterv1alpha1.ProgressiveOrderCommitStatusEnvironmentStatus, 0, len(graph.branches))

	for _, branch := range graph.branches {
		envStatus := statusByBranch[branch]
		upstreams, isPending, reason := evaluateProgressUpstreams(graph, branch, envStatus.Active.Dry, statusByBranch, watermarks)

		entry := promoterv1alpha1.ProgressiveOrderCommitStatusEnvironmentStatus{
			Branch:               branch,
			ActiveCommitStatuses: envStatus.Active.CommitStatuses,
			LastHealthyDry:       watermarks[branch],
			Upstreams:            upstreams,
		}

		// Skip when there is no proposed change (active and proposed dry SHAs match): there is no
		// in-flight PR to gate. Keep any existing CommitStatus in the valid set so orphan cleanup leaves
		// the last evaluated gate status alone, and mirror it onto status.environments[].
		if envStatus.Active.Dry.Sha == envStatus.Proposed.Dry.Sha {
			logger.V(4).Info("Skipping environment with no proposed change", "branch", branch)
			existing := &promoterv1alpha1.CommitStatus{}
			name := utils.CommitStatusResourceName(ctx, pocs, branch)
			if err := r.Get(ctx, client.ObjectKey{Namespace: pocs.Namespace, Name: name}, existing); err != nil {
				if !k8serrors.IsNotFound(err) {
					return fmt.Errorf("failed to get existing progressive order CommitStatus for branch %q: %w", branch, err)
				}
			} else {
				commitStatuses = append(commitStatuses, existing)
				entry.GateEnvironmentCommitStatus = utils.GateEnvironmentCommitStatusFromCommitStatus(existing)
			}
			pocs.Status.Environments = append(pocs.Status.Environments, entry)
			continue
		}

		phase := promoterv1alpha1.CommitPhaseSuccess
		if isPending {
			phase = promoterv1alpha1.CommitPhasePending
		}

		logger.V(4).Info("Evaluated progressive order gate for environment",
			"branch", branch,
			"dependsOn", graph.dependsOn[branch],
			"activeDrySha", envStatus.Active.Dry.Sha,
			"proposedDrySha", envStatus.Proposed.Dry.Sha,
			"pending", isPending,
			"reason", reason,
			"phase", phase)

		// Bind the CommitStatus to the proposed branch's hydrated SHA: that is the commit the
		// ChangeTransferPolicy inspects when gating the promotion PR.
		cs, gate, err := r.createOrUpdateProgressiveOrderCommitStatus(ctx, pocs, ps, branch, graph.dependsOn[branch], envStatus.Proposed.Hydrated.Sha, phase, reason)
		if err != nil {
			return fmt.Errorf("failed to set progressive order commit status for branch %q: %w", branch, err)
		}
		commitStatuses = append(commitStatuses, cs)
		entry.GateEnvironmentCommitStatus = gate
		pocs.Status.Environments = append(pocs.Status.Environments, entry)
	}

	if err := utils.CleanupOrphanedCommitStatuses(ctx, r.Client, r.Recorder, pocs, commitStatuses); err != nil {
		return fmt.Errorf("failed to cleanup orphaned CommitStatus resources: %w", err)
	}

	utils.InheritNotReadyConditionFromObjects(pocs, promoterConditions.CommitStatusesNotReady, commitStatuses...)

	return nil
}

// advanceProgressWatermark returns the environment's progress watermark after observing envStatus.
//
//   - When the environment is healthy (all active commit statuses passing) on a dry commit newer than the
//     previous watermark, the watermark moves forward to that commit.
//   - When the active dry commit is older than the previous watermark (for example after a rollback), the
//     newer watermark is no longer trustworthy: it is replaced by the current commit if healthy, or cleared.
//   - Otherwise the previous watermark is kept, so an environment busy rolling out a newer commit still
//     advertises the progress it has already proven.
func advanceProgressWatermark(
	previous *promoterv1alpha1.ProgressiveOrderCommitStatusWatermark,
	envStatus promoterv1alpha1.EnvironmentStatus,
	now metav1.Time,
) *promoterv1alpha1.ProgressiveOrderCommitStatusWatermark {
	active := envStatus.Active.Dry
	if active.Sha == "" {
		return previous
	}
	healthy := utils.AreCommitStatusesPassing(envStatus.Active.CommitStatuses)

	current := func() *promoterv1alpha1.ProgressiveOrderCommitStatusWatermark {
		if !healthy {
			return nil
		}
		return &promoterv1alpha1.ProgressiveOrderCommitStatusWatermark{
			Sha:        active.Sha,
			CommitTime: active.CommitTime,
			ObservedAt: &now,
		}
	}

	if previous == nil {
		return current()
	}
	if previous.Sha == active.Sha {
		return previous
	}
	if active.CommitTime.Before(&previous.CommitTime) {
		return current()
	}
	if healthy && active.CommitTime.After(previous.CommitTime.Time) {
		return current()
	}
	return previous
}

// evaluateProgressUpstreams evaluates each direct dependsOn upstream of branch and reports whether any
// is unsatisfied, along with the first blocking reason.
func evaluateProgressUpstreams(
	g *dag,
	branch string,
	activeDry promoterv1alpha1.CommitShaState,
	statusByBranch map[string]promoterv1alpha1.EnvironmentStatus,
	watermarks map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark,
) ([]promoterv1alpha1.ProgressiveOrderCommitStatusUpstreamStatus, bool, string) {
	upstreams := make([]promoterv1alpha1.ProgressiveOrderCommitStatusUpstreamStatus, 0, len(g.dependsOn[branch]))
	isPending := false
	firstReason := ""
	for _, upstream := range g.dependsOn[branch] {
		pending, reason := isUpstreamProgressPending(g, upstream, activeDry, statusByBranch, watermarks)
		upstreams = append(upstreams, promoterv1alpha1.ProgressiveOrderCommitStatusUpstreamStatus{
			Branch:    upstream,
			Satisfied: !pending,
			Reason:    reason,
		})
		if pending && !isPending {
			isPending = true
			firstReason = reason
		}
	}
	return upstreams, isPending, firstReason
}

// isUpstreamProgressPending reports whether upstream has NOT yet made healthy progress beyond
// downstreamActive (the dry commit the downstream environment is currently running).
//
// The upstream is satisfied when it is not currently failing and its watermark is newer than
// downstreamActive. A clean, healthy no-op upstream (its hydrator advanced the git note without producing
// a new commit, so its active dry SHA never moves) is looked through: it is satisfied when all of its own
// upstreams are, or immediately when it is a root.
func isUpstreamProgressPending(
	g *dag,
	upstream string,
	downstreamActive promoterv1alpha1.CommitShaState,
	statusByBranch map[string]promoterv1alpha1.EnvironmentStatus,
	watermarks map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark,
) (isPending bool, reason string) {
	envStatus, ok := statusByBranch[upstream]
	if !ok {
		return true, fmt.Sprintf("Waiting for %q environment status to be reported", upstream)
	}

	if key, failing := firstFailingCommitStatus(envStatus.Active.CommitStatuses); failing {
		return true, fmt.Sprintf("%q environment's %q commit status is failing", upstream, key)
	}

	if watermarkIsAhead(watermarks[upstream], downstreamActive) {
		return false, ""
	}

	envProposedDrySha := envStatus.Proposed.Dry.Sha
	envIsNoOp := getEffectiveHydratedDrySha(envStatus) != envProposedDrySha
	envHasPendingChanges := envStatus.Active.Dry.Sha != envProposedDrySha
	if envIsNoOp && !envHasPendingChanges {
		if isPend, r := checkCommitStatusesPassing(envStatus.Active.CommitStatuses, upstream); isPend {
			return isPend, r
		}
		for _, next := range g.dependsOn[upstream] {
			if pending, r := isUpstreamProgressPending(g, next, downstreamActive, statusByBranch, watermarks); pending {
				return true, r
			}
		}
		return false, ""
	}

	if watermarks[upstream] == nil {
		return true, fmt.Sprintf("Waiting for %q to be healthy on any commit", upstream)
	}
	return true, fmt.Sprintf("Waiting for %q to be healthy on a commit newer than %s (last healthy on %s)",
		upstream, shortShaOrNone(downstreamActive.Sha), utils.TruncateString(watermarks[upstream].Sha, 7))
}

// watermarkIsAhead reports whether the upstream watermark represents progress beyond downstreamActive.
// A downstream with no active dry commit is behind any watermark.
func watermarkIsAhead(watermark *promoterv1alpha1.ProgressiveOrderCommitStatusWatermark, downstreamActive promoterv1alpha1.CommitShaState) bool {
	if watermark == nil {
		return false
	}
	if downstreamActive.Sha == "" {
		return true
	}
	if watermark.Sha == downstreamActive.Sha {
		return false
	}
	return watermark.CommitTime.After(downstreamActive.CommitTime.Time)
}

// firstFailingCommitStatus returns the key of the first commit status in the failure phase.
// Pending statuses are not failing: an upstream rolling out a newer commit is expected to be pending.
func firstFailingCommitStatus(commitStatuses []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase) (string, bool) {
	for _, cs := range commitStatuses {
		if cs.Phase == string(promoterv1alpha1.CommitPhaseFailure) {
			return cs.Key, true
		}
	}
	return "", false
}

// progressiveOrderDescription returns the child CommitStatus description for the gate phase.
func progressiveOrderDescription(branch string, phase promoterv1alpha1.CommitStatusPhase, pendingReason string) string {
	if phase == promoterv1alpha1.CommitPhasePending {
		if pendingReason != "" {
			return pendingReason
		}
		return branch + " - waiting for upstream environments to progress"
	}
	return branch + " - all dependent environments have progressed and are healthy"
}

func shortShaOrNone(sha string) string {
	if sha == "" {
		return "none"
	}
	return utils.TruncateString(sha, 7)
}

// createOrUpdateProgressiveOrderCommitStatus upserts, via Server-Side Apply, the CommitStatus that reports
// whether the given environment's upstreams have made sufficient progress.
func (r *ProgressiveOrderCommitStatusReconciler) createOrUpdateProgressiveOrderCommitStatus(
	ctx context.Context,
	pocs *promoterv1alpha1.ProgressiveOrderCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
	branch string,
	dependsOn []string,
	reportedSha string,
	phase promoterv1alpha1.CommitStatusPhase,
	pendingReason string,
) (*promoterv1alpha1.CommitStatus, promoterv1alpha1.GateEnvironmentCommitStatus, error) {
	key := pocs.Spec.Key
	commitStatusName := utils.CommitStatusResourceName(ctx, pocs, branch)

	kind := reflect.TypeFor[promoterv1alpha1.ProgressiveOrderCommitStatus]().Name()
	gvk := promoterv1alpha1.GroupVersion.WithKind(kind)

	labels := utils.CommitStatusStandardLabels(pocs, branch, key)
	description := progressiveOrderDescription(branch, phase, pendingReason)

	var renderedURL string
	if pocs.Spec.URL.Template != "" {
		data := ProgressiveOrderURLTemplateData{
			Environment:                  branch,
			ProgressiveOrderCommitStatus: *pocs,
			PromotionStrategy:            ps,
			DependsOn:                    dependsOn,
			DependsOnQuery:               buildDependsOnQuery(dependsOn),
		}
		var err error
		renderedURL, err = renderOrderingGateURL(pocs.Spec.URL, data)
		if err != nil {
			return nil, promoterv1alpha1.GateEnvironmentCommitStatus{}, err
		}
	}

	// Use the stable gate key as the SCM commit status context (spec.Name) so users can reference a
	// single predictable name in branch protection rules.
	commitStatusSpec := acv1alpha1.CommitStatusSpec().
		WithRepositoryReference(acv1alpha1.ObjectReference().
			WithName(ps.Spec.RepositoryReference.Name)).
		WithSha(reportedSha).
		WithName(key).
		WithDescription(description).
		WithPhase(phase)

	if renderedURL != "" {
		commitStatusSpec = commitStatusSpec.WithUrl(renderedURL)
	}

	commitStatusApply := acv1alpha1.CommitStatus(commitStatusName, pocs.Namespace).
		WithLabels(labels).
		WithOwnerReferences(acmetav1.OwnerReference().
			WithAPIVersion(gvk.GroupVersion().String()).
			WithKind(gvk.Kind).
			WithName(pocs.Name).
			WithUID(pocs.UID).
			WithController(true).
			WithBlockOwnerDeletion(true)).
		WithSpec(commitStatusSpec)

	// Read the previously persisted phase so the phase-change event below stays transition-only.
	previousPhase := ""
	existingCommitStatus := &promoterv1alpha1.CommitStatus{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: pocs.Namespace, Name: commitStatusName}, existingCommitStatus); err == nil {
		previousPhase = string(existingCommitStatus.Spec.Phase)
	} else if !k8serrors.IsNotFound(err) {
		return nil, promoterv1alpha1.GateEnvironmentCommitStatus{}, fmt.Errorf("failed to get existing progressive order CommitStatus %q: %w", commitStatusName, err)
	}

	commitStatus := &promoterv1alpha1.CommitStatus{}
	commitStatus.Name = commitStatusName
	commitStatus.Namespace = pocs.Namespace
	if err := r.Patch(ctx, commitStatus, utils.ApplyPatch{ApplyConfig: commitStatusApply}, client.FieldOwner(constants.ProgressiveOrderCommitStatusControllerFieldOwner), client.ForceOwnership); err != nil {
		return nil, promoterv1alpha1.GateEnvironmentCommitStatus{}, fmt.Errorf("failed to apply progressive order CommitStatus: %w", err)
	}

	emitCommitStatusPhaseChangedEvent(r.Recorder, pocs, key, branch, previousPhase, string(phase))

	return commitStatus, promoterv1alpha1.GateEnvironmentCommitStatus{
		Phase:       phase,
		Description: description,
		Url:         renderedURL,
		ReportedSha: reportedSha,
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
//
//nolint:dupl // Gate controllers share the same SetupWithManager skeleton by design.
func (r *ProgressiveOrderCommitStatusReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	// Use Direct methods to read configuration from the API server without cache during setup.
	// The cache is not started during SetupWithManager, so we must use the non-cached API reader.
	rateLimiter, err := settings.GetRateLimiterDirect[promoterv1alpha1.ProgressiveOrderCommitStatusConfiguration, ctrl.Request](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get ProgressiveOrderCommitStatus rate limiter: %w", err)
	}

	maxConcurrentReconciles, err := settings.GetMaxConcurrentReconcilesDirect[promoterv1alpha1.ProgressiveOrderCommitStatusConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get ProgressiveOrderCommitStatus max concurrent reconciles: %w", err)
	}

	err = ctrl.NewControllerManagedBy(mgr).
		For(&promoterv1alpha1.ProgressiveOrderCommitStatus{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&promoterv1alpha1.PromotionStrategy{}, CommitStatusGatePromotionStrategyWatchHandler[promoterv1alpha1.ProgressiveOrderCommitStatusList](r.Client)).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles, RateLimiter: rateLimiter}).
		Named("progressiveordercommitstatus").
		Complete(r)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}
	return nil
}
