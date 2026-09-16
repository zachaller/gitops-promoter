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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	acv1alpha1 "github.com/argoproj-labs/gitops-promoter/applyconfiguration/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/git"
	"github.com/argoproj-labs/gitops-promoter/internal/gitauth"
	"github.com/argoproj-labs/gitops-promoter/internal/settings"
	promoterConditions "github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

// DryShaURLTemplateData is the data passed to DryShaSuccessfulCommitStatus.spec.url.template.
type DryShaURLTemplateData struct {
	DryShaSuccessfulCommitStatus promoterv1alpha1.DryShaSuccessfulCommitStatus
	PromotionStrategy            *promoterv1alpha1.PromotionStrategy
	Environment                  string
	// DependsOnQuery is DependsOn encoded as repeated env= query parameters
	// (e.g. "env=e2e&env=perf"), ready to append after "?". Empty when DependsOn is empty.
	DependsOnQuery string
	// DependsOn is the current environment's immediate upstream branches (one edge away),
	// from the resolved dependency graph on the PromotionStrategy (explicit dependsOn or
	// the linear chain inferred from spec.environments order when no environment declares dependsOn).
	DependsOn []string
}

// DryShaSuccessfulCommitStatusReconciler reconciles a DryShaSuccessfulCommitStatus object
type DryShaSuccessfulCommitStatusReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    events.EventRecorder
	SettingsMgr *settings.Manager
}

// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=dryshasuccessfulcommitstatuses,verbs=get;list;watch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=dryshasuccessfulcommitstatuses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=dryshasuccessfulcommitstatuses/finalizers,verbs=update
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=commitstatuses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies,verbs=get;list;watch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=gitrepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=scmproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=clusterscmproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile reads the referenced PromotionStrategy and, using the dependency graph declared on it,
// determines which environments are eligible for promotion and reports that as a per-environment commit
// status.
//
// Unlike DependentsSuccessfulCommitStatus, which requires each upstream to be sitting on the target dry
// commit and healthy right now, this gate asks whether the target dry commit has ALREADY been successful
// in each upstream. That record is rebuilt from the promotion-history git notes on each upstream's active
// branch and cached on status, so an upstream that has since promoted past the target still satisfies it.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile
//
//nolint:dupl // Gate controllers share the same reconciliation skeleton by design.
func (r *DryShaSuccessfulCommitStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := logf.FromContext(ctx)
	logger.Info("Reconciling DryShaSuccessfulCommitStatus")
	startTime := time.Now()

	var dscs promoterv1alpha1.DryShaSuccessfulCommitStatus
	// This applies the resource status via Server-Side Apply at the end of reconciliation. Don't write status manually.
	var previousReady *metav1.Condition
	defer utils.HandleReconciliationResult(ctx, startTime, &dscs, r.Client, r.Recorder, constants.DryShaSuccessfulCommitStatusControllerFieldOwner, &result, &err, &previousReady)

	// 1. Fetch the DryShaSuccessfulCommitStatus instance.
	if err = r.Get(ctx, req.NamespacedName, &dscs); err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Info("DryShaSuccessfulCommitStatus not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get DryShaSuccessfulCommitStatus %q: %w", req.Name, err)
	}

	// Start fresh on the Ready condition each reconcile.
	previousReady = utils.RemoveReadyCondition(&dscs)

	if err = ensureControllerInstanceIDStable(ctx, r.SettingsMgr); err != nil {
		return ctrl.Result{}, err
	}

	// 2. Fetch the referenced PromotionStrategy.
	var ps promoterv1alpha1.PromotionStrategy
	psKey := client.ObjectKey{Namespace: dscs.Namespace, Name: dscs.Spec.PromotionStrategyRef.Name}
	if err = r.Get(ctx, psKey, &ps); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("referenced PromotionStrategy %q not found: %w", dscs.Spec.PromotionStrategyRef.Name, err)
		}
		return ctrl.Result{}, fmt.Errorf("failed to get PromotionStrategy %q: %w", dscs.Spec.PromotionStrategyRef.Name, err)
	}

	// 3. Rebuild the dry SHA record where stale, evaluate the graph, and write statuses.
	if err = r.updateDryShaSuccessfulCommitStatus(ctx, &dscs, &ps); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update dry SHA commit statuses: %w", err)
	}

	// 4. Requeue using the configured requeue duration.
	requeueDuration, err := settings.GetRequeueDuration[promoterv1alpha1.DryShaSuccessfulCommitStatusConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get requeue duration for DryShaSuccessfulCommitStatus %q: %w", dscs.Name, err)
	}

	return ctrl.Result{RequeueAfter: requeueDuration}, nil
}

// updateDryShaSuccessfulCommitStatus builds the dependency graph from the PromotionStrategy, validates it,
// refreshes each relevant environment's dry SHA record from git, evaluates every environment against its
// upstreams, and writes a per-environment CommitStatus: success once all of an environment's dependsOn
// upstreams have been successful for the dry commit being promoted, pending otherwise.
func (r *DryShaSuccessfulCommitStatusReconciler) updateDryShaSuccessfulCommitStatus(ctx context.Context, dscs *promoterv1alpha1.DryShaSuccessfulCommitStatus, ps *promoterv1alpha1.PromotionStrategy) error {
	logger := logf.FromContext(ctx)

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

	// Index PromotionStrategy environment status by branch so each node can look up its own and its
	// upstreams' state in O(1).
	statusByBranch := make(map[string]promoterv1alpha1.EnvironmentStatus, len(ps.Status.Environments))
	for _, envStatus := range ps.Status.Environments {
		statusByBranch[envStatus.Branch] = envStatus
	}

	// Carry the previously cached records forward so an unchanged branch tip costs no git work.
	recordsByBranch := make(map[string][]promoterv1alpha1.DryShaRecord, len(graph.branches))
	rebuiltFromByBranch := make(map[string]string, len(graph.branches))
	rebuiltAtByBranch := make(map[string]metav1.Time, len(graph.branches))
	for _, envStatus := range dscs.Status.Environments {
		recordsByBranch[envStatus.Branch] = envStatus.DryShaHistory
		rebuiltFromByBranch[envStatus.Branch] = envStatus.RebuiltFromSha
		rebuiltAtByBranch[envStatus.Branch] = envStatus.RebuiltAt
	}

	if err := r.refreshDryShaRecords(ctx, dscs, ps, graph, statusByBranch, recordsByBranch, rebuiltFromByBranch, rebuiltAtByBranch); err != nil {
		return err
	}

	commitStatuses := make([]*promoterv1alpha1.CommitStatus, 0, len(graph.branches))
	dscs.Status.Environments = make([]promoterv1alpha1.DryShaSuccessfulCommitStatusEnvironmentStatus, 0, len(graph.branches))

	for _, branch := range graph.branches {
		envStatus := statusByBranch[branch]
		// The gate is keyed to the dry SHA this environment is promoting. Upstreams are checked against
		// that SAME dry commit, which is what stops a downstream from merging a change its upstreams have
		// never run.
		targetDrySha := getEffectiveHydratedDrySha(envStatus)
		snapshots, isPending, reason := r.evaluateDryShaUpstreams(dscs, graph, branch, targetDrySha, statusByBranch, recordsByBranch)

		entry := promoterv1alpha1.DryShaSuccessfulCommitStatusEnvironmentStatus{
			Branch:         branch,
			RebuiltFromSha: rebuiltFromByBranch[branch],
			RebuiltAt:      rebuiltAtByBranch[branch],
			DryShaHistory:  recordsByBranch[branch],
			Upstreams:      snapshots,
		}

		// Skip when there is no proposed change (active and proposed dry SHAs match): there is no in-flight
		// PR to gate, so updating a CommitStatus would only cause unnecessary updates. This also avoids
		// writing a CommitStatus with an empty proposed hydrated SHA.
		//
		// Keep any existing CommitStatus in the valid set so orphan cleanup leaves the last evaluated gate
		// status alone until a new proposed change appears, and mirror that child onto status.environments[]
		// so operators still see the last report after promotion completes.
		if envStatus.Active.Dry.Sha == envStatus.Proposed.Dry.Sha {
			logger.V(4).Info("Skipping environment with no proposed change", "branch", branch)
			existing := &promoterv1alpha1.CommitStatus{}
			name := utils.CommitStatusResourceName(ctx, dscs, branch)
			if err := r.Get(ctx, client.ObjectKey{Namespace: dscs.Namespace, Name: name}, existing); err != nil {
				if !k8serrors.IsNotFound(err) {
					return fmt.Errorf("failed to get existing dry SHA CommitStatus for branch %q: %w", branch, err)
				}
			} else {
				commitStatuses = append(commitStatuses, existing)
				entry.GateEnvironmentCommitStatus = utils.GateEnvironmentCommitStatusFromCommitStatus(existing)
			}
			dscs.Status.Environments = append(dscs.Status.Environments, entry)
			continue
		}

		phase := promoterv1alpha1.CommitPhaseSuccess
		if isPending {
			phase = promoterv1alpha1.CommitPhasePending
		}

		logger.V(4).Info("Evaluated dry SHA gate for environment",
			"branch", branch,
			"dependsOn", graph.dependsOn[branch],
			"targetDrySha", targetDrySha,
			"pending", isPending,
			"reason", reason,
			"phase", phase)

		// Bind the CommitStatus to the proposed branch's hydrated SHA: that is the commit the
		// ChangeTransferPolicy inspects when gating the promotion PR. Binding to the dry SHA instead
		// leaves the gate undetectable, so the promotion never advances.
		proposedHydratedSha := envStatus.Proposed.Hydrated.Sha
		cs, gate, err := r.createOrUpdateDryShaSuccessfulCommitStatus(ctx, dscs, ps, branch, graph.dependsOn[branch], proposedHydratedSha, phase, reason)
		if err != nil {
			return fmt.Errorf("failed to set dry SHA commit status for branch %q: %w", branch, err)
		}
		commitStatuses = append(commitStatuses, cs)
		entry.GateEnvironmentCommitStatus = gate
		dscs.Status.Environments = append(dscs.Status.Environments, entry)
	}

	if err := utils.CleanupOrphanedCommitStatuses(ctx, r.Client, r.Recorder, dscs, commitStatuses); err != nil {
		return fmt.Errorf("failed to cleanup orphaned CommitStatus resources: %w", err)
	}

	utils.InheritNotReadyConditionFromObjects(dscs, promoterConditions.CommitStatusesNotReady, commitStatuses...)

	return nil
}

// SetupWithManager sets up the controller with the Manager.
//
//nolint:dupl // Gate controllers share the same controller-builder wiring by design.
func (r *DryShaSuccessfulCommitStatusReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	// Use Direct methods to read configuration from the API server without cache during setup.
	// The cache is not started during SetupWithManager, so we must use the non-cached API reader.
	rateLimiter, err := settings.GetRateLimiterDirect[promoterv1alpha1.DryShaSuccessfulCommitStatusConfiguration, ctrl.Request](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get DryShaSuccessfulCommitStatus rate limiter: %w", err)
	}

	maxConcurrentReconciles, err := settings.GetMaxConcurrentReconcilesDirect[promoterv1alpha1.DryShaSuccessfulCommitStatusConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get DryShaSuccessfulCommitStatus max concurrent reconciles: %w", err)
	}

	err = ctrl.NewControllerManagedBy(mgr).
		For(&promoterv1alpha1.DryShaSuccessfulCommitStatus{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&promoterv1alpha1.PromotionStrategy{}, r.enqueueDryShaSuccessfulCommitStatusForPromotionStrategy()).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles, RateLimiter: rateLimiter}).
		Named("dryshasuccessfulcommitstatus").
		Complete(r)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}
	return nil
}

// enqueueDryShaSuccessfulCommitStatusForPromotionStrategy returns a handler that enqueues all
// DryShaSuccessfulCommitStatus resources that reference a PromotionStrategy when that PromotionStrategy changes.
func (r *DryShaSuccessfulCommitStatusReconciler) enqueueDryShaSuccessfulCommitStatusForPromotionStrategy() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
		ps, ok := obj.(*promoterv1alpha1.PromotionStrategy)
		if !ok {
			return nil
		}

		var dscsList promoterv1alpha1.DryShaSuccessfulCommitStatusList
		if err := r.List(ctx, &dscsList,
			client.InNamespace(ps.Namespace),
			client.MatchingFields{PromotionStrategyRefField: ps.Name},
		); err != nil {
			logf.FromContext(ctx).Error(err, "failed to list DryShaSuccessfulCommitStatus resources")
			return nil
		}

		requests := make([]ctrl.Request, 0, len(dscsList.Items))
		for i := range dscsList.Items {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKeyFromObject(&dscsList.Items[i]),
			})
		}

		return requests
	})
}

// createOrUpdateDryShaSuccessfulCommitStatus upserts, via Server-Side Apply, the CommitStatus that reports
// whether the given environment's upstreams have already been successful for the dry commit it is promoting.
func (r *DryShaSuccessfulCommitStatusReconciler) createOrUpdateDryShaSuccessfulCommitStatus(
	ctx context.Context,
	dscs *promoterv1alpha1.DryShaSuccessfulCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
	branch string,
	dependsOn []string,
	reportedSha string,
	phase promoterv1alpha1.CommitStatusPhase,
	pendingReason string,
) (*promoterv1alpha1.CommitStatus, promoterv1alpha1.GateEnvironmentCommitStatus, error) {
	key := dscs.Spec.Key
	commitStatusName := utils.CommitStatusResourceName(ctx, dscs, branch)

	kind := reflect.TypeFor[promoterv1alpha1.DryShaSuccessfulCommitStatus]().Name()
	gvk := promoterv1alpha1.GroupVersion.WithKind(kind)

	labels := utils.CommitStatusStandardLabels(dscs, branch, key)
	description := utils.GateEnvironmentCommitStatusDescription(branch, phase, pendingReason)

	urlData := DryShaURLTemplateData{
		Environment:                  branch,
		DryShaSuccessfulCommitStatus: *dscs,
		PromotionStrategy:            ps,
		DependsOn:                    dependsOn,
		DependsOnQuery:               buildDependsOnQuery(dependsOn),
	}
	renderedURL, err := renderGateCommitStatusURL(ctx, dscs.Spec.URL, urlData, branch, commitStatusName, dscs.Namespace)
	if err != nil {
		return nil, promoterv1alpha1.GateEnvironmentCommitStatus{}, err
	}

	// Use the stable gate key as the SCM commit status context (spec.Name) so users can reference a single
	// predictable name in branch protection rules, regardless of which environment or phase produced the
	// status. The human-readable, per-environment detail goes in the description instead.
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

	commitStatusApply := acv1alpha1.CommitStatus(commitStatusName, dscs.Namespace).
		WithLabels(labels).
		WithOwnerReferences(acmetav1.OwnerReference().
			WithAPIVersion(gvk.GroupVersion().String()).
			WithKind(gvk.Kind).
			WithName(dscs.Name).
			WithUID(dscs.UID).
			WithController(true).
			WithBlockOwnerDeletion(true)).
		WithSpec(commitStatusSpec)

	// Read the previously persisted phase from the child CommitStatus (NotFound means this branch is being
	// gated for the first time) so the phase-change event below stays transition-only.
	previousPhase := ""
	existingCommitStatus := &promoterv1alpha1.CommitStatus{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: dscs.Namespace, Name: commitStatusName}, existingCommitStatus); err == nil {
		previousPhase = string(existingCommitStatus.Spec.Phase)
	} else if !k8serrors.IsNotFound(err) {
		return nil, promoterv1alpha1.GateEnvironmentCommitStatus{}, fmt.Errorf("failed to get existing dry SHA CommitStatus %q: %w", commitStatusName, err)
	}

	commitStatus := &promoterv1alpha1.CommitStatus{}
	commitStatus.Name = commitStatusName
	commitStatus.Namespace = dscs.Namespace
	if err := r.Patch(ctx, commitStatus, utils.ApplyPatch{ApplyConfig: commitStatusApply}, client.FieldOwner(constants.DryShaSuccessfulCommitStatusControllerFieldOwner), client.ForceOwnership); err != nil {
		return nil, promoterv1alpha1.GateEnvironmentCommitStatus{}, fmt.Errorf("failed to apply dry SHA CommitStatus: %w", err)
	}

	emitCommitStatusPhaseChangedEvent(r.Recorder, dscs, key, branch, previousPhase, string(phase))

	return commitStatus, promoterv1alpha1.GateEnvironmentCommitStatus{
		Phase:       phase,
		Description: description,
		Url:         renderedURL,
		ReportedSha: reportedSha,
	}, nil
}

// newGitOperations builds the git client used to rebuild dry SHA records. The clone identity is the gate
// CR's namespace/name, so the process-wide clone registry reuses one clone across reconciles instead of
// re-cloning each pass.
func (r *DryShaSuccessfulCommitStatusReconciler) newGitOperations(
	ctx context.Context,
	dscs *promoterv1alpha1.DryShaSuccessfulCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
) (*git.EnvironmentOperations, error) {
	scmProvider, secret, err := utils.GetScmProviderAndSecretFromRepositoryReference(ctx, r.Client, r.SettingsMgr.GetControllerNamespace(), ps.Spec.RepositoryReference, ps)
	if err != nil {
		return nil, fmt.Errorf("failed to get ScmProvider and secret for repo %q: %w", ps.Spec.RepositoryReference.Name, err)
	}

	repoKey := client.ObjectKey{Namespace: dscs.Namespace, Name: ps.Spec.RepositoryReference.Name}
	gitAuthProvider, err := gitauth.CreateGitOperationsProvider(ctx, r.Client, scmProvider, secret, repoKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create git auth provider for ScmProvider %q: %w", scmProvider.GetName(), err)
	}

	gitRepo, err := utils.GetGitRepositoryFromObjectKey(ctx, r.Client, repoKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitRepository %q: %w", ps.Spec.RepositoryReference.Name, err)
	}

	gitOperations := git.NewEnvironmentOperations(gitRepo, gitAuthProvider, dscs.Namespace+"/"+dscs.Name)
	if err := gitOperations.CloneRepo(ctx); err != nil {
		return nil, fmt.Errorf("failed to clone repo %q: %w", ps.Spec.RepositoryReference.Name, err)
	}
	if err := gitOperations.FetchNotes(ctx); err != nil {
		return nil, fmt.Errorf("failed to fetch git notes: %w", err)
	}

	return gitOperations, nil
}

// environmentActivePath returns the active path in effect for a branch: the environment's own override
// when set, otherwise the strategy-level path.
func environmentActivePath(ps *promoterv1alpha1.PromotionStrategy, branch string) string {
	for _, env := range ps.Spec.Environments {
		if env.Branch == branch && env.ActivePath != "" {
			return env.ActivePath
		}
	}
	return ps.Spec.ActivePath
}

// branchesNeedingRecords returns the upstream branches whose dry SHA record is actually consulted this
// reconcile: the transitive ancestors of every environment with an in-flight change. A leaf environment
// nobody depends on is never walked.
func branchesNeedingRecords(g *dag, statusByBranch map[string]promoterv1alpha1.EnvironmentStatus) []string {
	needed := make(map[string]bool)
	for _, branch := range g.branches {
		envStatus := statusByBranch[branch]
		if envStatus.Active.Dry.Sha == envStatus.Proposed.Dry.Sha {
			// Nothing in flight for this environment, so its upstreams are not consulted.
			continue
		}
		for _, ancestor := range collectAncestorBranches(g, branch) {
			needed[ancestor] = true
		}
	}

	// Preserve graph order so the walk, and any resulting log output, is deterministic.
	ordered := make([]string, 0, len(needed))
	for _, branch := range g.branches {
		if needed[branch] {
			ordered = append(ordered, branch)
		}
	}
	return ordered
}

// refreshDryShaRecords rebuilds the cached dry SHA record for every branch whose active branch tip has
// moved since the cache was built, and refreshes the live head entry for all of them.
//
// Git is only touched when at least one branch is actually stale, so the common case — a reconcile
// triggered by an unrelated PromotionStrategy status change — does no clone, fetch, or walk.
func (r *DryShaSuccessfulCommitStatusReconciler) refreshDryShaRecords(
	ctx context.Context,
	dscs *promoterv1alpha1.DryShaSuccessfulCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
	graph *dag,
	statusByBranch map[string]promoterv1alpha1.EnvironmentStatus,
	recordsByBranch map[string][]promoterv1alpha1.DryShaRecord,
	rebuiltFromByBranch map[string]string,
	rebuiltAtByBranch map[string]metav1.Time,
) error {
	logger := logf.FromContext(ctx)

	needed := branchesNeedingRecords(graph, statusByBranch)

	stale := make([]string, 0, len(needed))
	for _, branch := range needed {
		activeHydratedSha := statusByBranch[branch].Active.Hydrated.Sha
		if activeHydratedSha == "" {
			// The environment has not reported an active branch state yet; there is nothing to walk from.
			continue
		}
		if rebuiltFromByBranch[branch] != activeHydratedSha {
			stale = append(stale, branch)
		}
	}

	if len(stale) > 0 {
		gitOperations, err := r.newGitOperations(ctx, dscs, ps)
		if err != nil {
			return err
		}

		for _, branch := range stale {
			records, tip, err := r.walkBranchForDryShaRecords(ctx, dscs, ps, branch, rebuiltFromByBranch[branch], gitOperations)
			if err != nil {
				return fmt.Errorf("failed to rebuild dry SHA record for branch %q: %w", branch, err)
			}
			logger.V(4).Info("Rebuilt dry SHA record from git",
				"branch", branch, "tip", tip, "entries", len(records))
			recordsByBranch[branch] = records
			rebuiltFromByBranch[branch] = tip
			rebuiltAtByBranch[branch] = metav1.NewTime(time.Now())
		}
	}

	// The dry commit currently running in an environment has no promotion-history note yet — one is only
	// written when the NEXT promotion merges — so it would be invisible to the gate until superseded.
	// Recompute it from live status every reconcile, including when the walk was skipped, because its
	// success flag tracks health that changes without the branch tip moving.
	for _, branch := range graph.branches {
		recordsByBranch[branch] = withLiveHeadRecord(recordsByBranch[branch], statusByBranch[branch])
	}

	return nil
}

// walkBranchForDryShaRecords rebuilds one environment's dry SHA record from the promotion-history git notes
// on its active branch, newest first.
func (r *DryShaSuccessfulCommitStatusReconciler) walkBranchForDryShaRecords(
	ctx context.Context,
	dscs *promoterv1alpha1.DryShaSuccessfulCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
	branch string,
	lastKnownHydratedSha string,
	gitOperations *git.EnvironmentOperations,
) ([]promoterv1alpha1.DryShaRecord, string, error) {
	logger := logf.FromContext(ctx)

	tip, err := gitOperations.GetBranchSha(ctx, branch, lastKnownHydratedSha)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get branch SHA for %q: %w", branch, err)
	}

	depth := dscs.Spec.EffectiveHistoryDepth()
	shas, err := gitOperations.GetRevListFirstParent(ctx, "origin/"+branch, depth)
	if err != nil {
		return nil, "", fmt.Errorf("failed to list commits for %q: %w", branch, err)
	}
	if len(shas) == 0 {
		return nil, tip, nil
	}

	// Pre-load the commit objects and hydrator metadata blobs in one pass rather than per commit.
	if err := gitOperations.LoadCommitAndMetadataBlobs(ctx, environmentActivePath(ps, branch), shas...); err != nil {
		logger.V(4).Info("failed to prefetch history commit objects", "branch", branch, "err", err)
	}

	records := make([]promoterv1alpha1.DryShaRecord, 0, len(shas))
	seen := make(map[string]bool, len(shas))
	for _, sha := range shas {
		record, ok := r.dryShaRecordForCommit(ctx, gitOperations, sha)
		if !ok {
			continue
		}
		// The walk runs newest first, so the first entry for a dry SHA is the most recent one. A dry SHA
		// can repeat when an environment is re-promoted onto the same dry commit.
		if seen[record.Sha] {
			continue
		}
		seen[record.Sha] = true
		records = append(records, record)
	}

	return records, tip, nil
}

// dryShaRecordForCommit reads the promotion-history note on an active branch commit and turns it into a
// record entry. It reports ok=false when the commit carries no usable trailers.
//
// The note is the durable copy: it survives SCM-side merge message rewrites (squash merges, merges made
// directly on the SCM). Commits predating notes, or whose PR finalization never wrote one, fall back to the
// merge commit's own message trailers — the same fallback order the ChangeTransferPolicy history builder
// uses.
func (r *DryShaSuccessfulCommitStatusReconciler) dryShaRecordForCommit(
	ctx context.Context,
	gitOperations *git.EnvironmentOperations,
	sha string,
) (promoterv1alpha1.DryShaRecord, bool) {
	logger := logf.FromContext(ctx)

	source := promoterv1alpha1.DryShaRecordSourceNote
	trailers, err := gitOperations.GetHistoryNote(ctx, sha)
	if err != nil {
		logger.V(4).Info("failed to get history note, falling back to commit message trailers", "sha", sha, "err", err)
	}
	if len(trailers) == 0 {
		source = promoterv1alpha1.DryShaRecordSourceCommitMessage
		trailers, err = gitOperations.GetTrailers(ctx, sha)
		if err != nil {
			logger.V(4).Info("failed to get trailers", "sha", sha, "err", err)
			return promoterv1alpha1.DryShaRecord{}, false
		}
	}

	// Sha-dry-active records what the environment was running immediately before this promotion merged,
	// and the Commit-status-active-* trailers are that same dry commit's health, captured in the same
	// snapshot. Pairing them is what makes this a record of "dry SHA X was successful in this environment".
	drySha := getFirstTrailerValue(trailers, constants.TrailerShaDryActive)
	if drySha == "" {
		// A pull request merged before the promoter ever refreshed its commit message carries no trailers.
		logger.V(4).Info("commit has no "+constants.TrailerShaDryActive+" trailer, skipping", "sha", sha)
		return promoterv1alpha1.DryShaRecord{}, false
	}

	activeKeys, _ := getCommitStatusKeysFromTrailers(ctx, trailers)
	activeStatuses := commitStatusPhasesFromTrailers(ctx, trailers, constants.TrailerCommitStatusActivePrefix, activeKeys)

	record := promoterv1alpha1.DryShaRecord{
		Sha:        drySha,
		MergeSha:   sha,
		Source:     source,
		Successful: utils.AreCommitStatusesPassing(activeStatuses),
	}

	if mergeTime := getFirstTrailerValue(trailers, constants.TrailerPullRequestMergeTime); mergeTime != "" {
		if parsed, err := time.Parse(time.RFC3339, mergeTime); err != nil {
			logger.V(4).Info("failed to parse "+constants.TrailerPullRequestMergeTime, "time", mergeTime, "err", err)
		} else {
			record.MergedAt = metav1.NewTime(parsed)
		}
	}

	return record, true
}

// withLiveHeadRecord returns records with the environment's currently-running dry commit as the newest
// entry, replacing any stale live entry from a previous reconcile.
func withLiveHeadRecord(records []promoterv1alpha1.DryShaRecord, envStatus promoterv1alpha1.EnvironmentStatus) []promoterv1alpha1.DryShaRecord {
	// Drop any previous live entry: its success flag is recomputed below, and git-derived entries are
	// authoritative for everything behind the head.
	pruned := make([]promoterv1alpha1.DryShaRecord, 0, len(records)+1)
	for _, record := range records {
		if record.Source == promoterv1alpha1.DryShaRecordSourceLive {
			continue
		}
		pruned = append(pruned, record)
	}

	activeDrySha := envStatus.Active.Dry.Sha
	if activeDrySha == "" {
		return pruned
	}

	live := promoterv1alpha1.DryShaRecord{
		Sha:        activeDrySha,
		MergeSha:   envStatus.Active.Hydrated.Sha,
		Source:     promoterv1alpha1.DryShaRecordSourceLive,
		Successful: utils.AreCommitStatusesPassing(envStatus.Active.CommitStatuses),
	}

	// The walk records each note's Sha-dry-active, which is the dry commit running BEFORE that promotion,
	// so the currently running one never appears in it. If it somehow does (a re-promotion onto the same
	// dry commit), the live entry wins because it carries current health.
	deduped := slices.DeleteFunc(pruned, func(record promoterv1alpha1.DryShaRecord) bool {
		return record.Sha == activeDrySha
	})

	return append([]promoterv1alpha1.DryShaRecord{live}, deduped...)
}

// dryShaSatisfaction is the outcome of evaluating one upstream branch against a target dry commit.
type dryShaSatisfaction struct {
	// SatisfiedBySha is the dry commit whose recorded success satisfied the upstream. Empty when pending.
	SatisfiedBySha string
	// Reason explains why the upstream is not satisfied. Empty when satisfied.
	Reason string
	// Satisfied reports whether the upstream has been successful for the target dry commit.
	Satisfied bool
}

// evaluateDryShaUpstreams builds snapshots for every transitive ancestor of branch and reports whether any
// DIRECT dependsOn upstream is unsatisfied. Transitive ancestors are reported for observability only; the
// gate decision rests on the direct upstreams, each of which is itself gated on its own upstreams.
func (r *DryShaSuccessfulCommitStatusReconciler) evaluateDryShaUpstreams(
	dscs *promoterv1alpha1.DryShaSuccessfulCommitStatus,
	g *dag,
	branch string,
	targetDrySha string,
	statusByBranch map[string]promoterv1alpha1.EnvironmentStatus,
	recordsByBranch map[string][]promoterv1alpha1.DryShaRecord,
) (snapshots []promoterv1alpha1.DryShaSuccessfulCommitStatusUpstreamStatus, isPending bool, reason string) {
	ancestors := collectAncestorBranches(g, branch)
	snapshots = make([]promoterv1alpha1.DryShaSuccessfulCommitStatusUpstreamStatus, 0, len(ancestors))
	byBranch := make(map[string]dryShaSatisfaction, len(ancestors))

	for _, upstream := range ancestors {
		result := evaluateDryShaUpstream(dscs, upstream, targetDrySha, statusByBranch[upstream], recordsByBranch[upstream])
		byBranch[upstream] = result
		snapshots = append(snapshots, promoterv1alpha1.DryShaSuccessfulCommitStatusUpstreamStatus{
			Branch:         upstream,
			Satisfied:      result.Satisfied,
			SatisfiedBySha: result.SatisfiedBySha,
			Reason:         result.Reason,
		})
	}

	for _, upstream := range g.dependsOn[branch] {
		if result := byBranch[upstream]; !result.Satisfied {
			return snapshots, true, result.Reason
		}
	}

	return snapshots, false, ""
}

// evaluateDryShaUpstream decides whether one upstream environment has been successful for targetDrySha.
//
// The record is a first-parent walk of the upstream's active branch, newest first, so index order IS
// promotion order on that branch. That makes the descendant rule sound rather than heuristic: an entry at a
// lower index is strictly a later promotion, so the upstream demonstrably ran the target's content and has
// since become healthy past it. No commit-time comparison is involved, so clock skew, second-granularity
// timestamp ties, and force-pushed timestamps cannot influence the decision.
func evaluateDryShaUpstream(
	dscs *promoterv1alpha1.DryShaSuccessfulCommitStatus,
	branch string,
	targetDrySha string,
	envStatus promoterv1alpha1.EnvironmentStatus,
	records []promoterv1alpha1.DryShaRecord,
) dryShaSatisfaction {
	if targetDrySha == "" {
		return dryShaSatisfaction{Reason: "Waiting for the hydrator to finish processing the proposed dry commit"}
	}

	targetIndex := -1
	firstSuccessIndex := -1
	for i, record := range records {
		if firstSuccessIndex == -1 && record.Successful {
			firstSuccessIndex = i
		}
		if record.Sha == targetDrySha {
			targetIndex = i
			break
		}
	}

	// 1. The target dry commit itself was successful in this environment.
	if targetIndex >= 0 && records[targetIndex].Successful {
		return dryShaSatisfaction{Satisfied: true, SatisfiedBySha: targetDrySha}
	}

	// 2. A later promotion on this branch — which therefore carries the target's content — was successful.
	if targetIndex >= 0 && firstSuccessIndex >= 0 && firstSuccessIndex < targetIndex && dscs.Spec.IsAllowNewerDrySha() {
		return dryShaSatisfaction{Satisfied: true, SatisfiedBySha: records[firstSuccessIndex].Sha}
	}

	// 3. The target reached this environment but was not successful, and either nothing newer succeeded or
	//    spec.allowNewerDrySha is off.
	if targetIndex >= 0 {
		return dryShaSatisfaction{
			Reason: fmt.Sprintf("Waiting for %q to become successful on the proposed dry commit", branch),
		}
	}

	// 4. The target is not in the record at all.
	//
	//    Note this already accounts for the dry commit the upstream is running right now: it has no
	//    promotion-history note yet (one is only written when the next promotion merges), so
	//    withLiveHeadRecord prepends it from live status before evaluation. Reaching here means the
	//    upstream has genuinely never run the target.
	if envStatus.Branch == "" {
		return dryShaSatisfaction{Reason: fmt.Sprintf("Waiting for %q environment status to be reported", branch)}
	}

	//    Either the upstream has not promoted the target yet, or the target is older than the configured
	//    walk depth. Distinguish the two so the fix is discoverable: a record that filled the whole walk
	//    means we simply did not look far enough back.
	if len(records) >= dscs.Spec.EffectiveHistoryDepth() {
		return dryShaSatisfaction{
			Reason: fmt.Sprintf("Proposed dry commit not found in the last %d promotions of %q; raise spec.historyDepth if it is older than that",
				dscs.Spec.EffectiveHistoryDepth(), branch),
		}
	}
	return dryShaSatisfaction{Reason: fmt.Sprintf("Waiting for %q to be promoted", branch)}
}
