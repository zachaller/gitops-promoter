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
	"sort"
	"time"

	"gopkg.in/yaml.v3"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	acmetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	acv1alpha1 "github.com/argoproj-labs/gitops-promoter/applyconfiguration/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/settings"
	promoterConditions "github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

// DagCommitStatusReconciler reconciles a DagCommitStatus object.
type DagCommitStatusReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    events.EventRecorder
	SettingsMgr *settings.Manager
}

// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=dagcommitstatuses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=dagcommitstatuses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=dagcommitstatuses/finalizers,verbs=update

// Reconcile builds the dependency graph from the DagCommitStatus spec, validates it,
// and produces one CommitStatus on each non-root environment's proposed hydrated SHA
// whose phase aggregates the parent (dependsOn) environments' active commit statuses.
func (r *DagCommitStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling DagCommitStatus")
	startTime := time.Now()

	var dag promoterv1alpha1.DagCommitStatus
	defer utils.HandleReconciliationResult(ctx, startTime, &dag, r.Client, r.Recorder, constants.DagCommitStatusControllerFieldOwner, &result, &err)

	err = r.Get(ctx, req.NamespacedName, &dag, &client.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Info("DagCommitStatus not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get DagCommitStatus %q: %w", req.Name, err)
	}

	if !dag.DeletionTimestamp.IsZero() {
		logger.V(4).Info("DagCommitStatus is being deleted, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Reset transient status fields on each reconcile so we don't carry stale entries.
	meta.RemoveStatusCondition(dag.GetConditions(), string(promoterConditions.Ready))
	dag.Status.Environments = nil
	dag.Status.UnreferencedEnvironments = nil

	// Resolve the referenced PromotionStrategy.
	var ps promoterv1alpha1.PromotionStrategy
	psKey := client.ObjectKey{Namespace: dag.Namespace, Name: dag.Spec.PromotionStrategyRef.Name}
	if getErr := r.Get(ctx, psKey, &ps); getErr != nil {
		if k8serrors.IsNotFound(getErr) {
			r.setNotReady(&dag, promoterConditions.PromotionStrategyNotFound,
				fmt.Sprintf("referenced PromotionStrategy %q not found", dag.Spec.PromotionStrategyRef.Name))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get PromotionStrategy %q: %w", dag.Spec.PromotionStrategyRef.Name, getErr)
	}

	// Build and validate the graph against the PromotionStrategy's environments.
	psBranches := make(map[string]struct{}, len(ps.Spec.Environments))
	for _, e := range ps.Spec.Environments {
		psBranches[e.Branch] = struct{}{}
	}
	g, buildErr := buildDagGraph(dag.Spec.Environments, psBranches)
	if buildErr != nil {
		r.setNotReady(&dag, promoterConditions.InvalidDependencyGraph, buildErr.Error())
		return ctrl.Result{}, nil
	}

	// Build a status map keyed by branch from the PS status.
	statusByBranch := make(map[string]promoterv1alpha1.EnvironmentStatus, len(ps.Status.Environments))
	for _, es := range ps.Status.Environments {
		statusByBranch[es.Branch] = es
	}

	// Build a CTP map keyed by branch via labels on CTPs.
	var ctpList promoterv1alpha1.ChangeTransferPolicyList
	if listErr := r.List(ctx, &ctpList,
		client.InNamespace(ps.Namespace),
		client.MatchingLabels{promoterv1alpha1.PromotionStrategyLabel: utils.KubeSafeLabel(ps.Name)},
	); listErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list ChangeTransferPolicies for %q: %w", ps.Name, listErr)
	}
	ctpByBranch := make(map[string]*promoterv1alpha1.ChangeTransferPolicy, len(ctpList.Items))
	for i := range ctpList.Items {
		ctpByBranch[ctpList.Items[i].Spec.ActiveBranch] = &ctpList.Items[i]
	}

	key := dag.Spec.Key
	if key == "" {
		key = promoterv1alpha1.PreviousEnvironmentCommitStatusKey
	}

	produced := make([]*promoterv1alpha1.CommitStatus, 0)
	envStatuses := make([]promoterv1alpha1.DagEnvironmentStatus, 0)

	for _, env := range dag.Spec.Environments {
		if len(env.DependsOn) == 0 {
			continue // root: nothing to gate
		}

		ctp, ok := ctpByBranch[env.Branch]
		if !ok {
			logger.V(4).Info("Skipping env, no matching CTP yet", "branch", env.Branch)
			continue
		}

		// Skip if there's no proposed change in the gated environment (active == proposed).
		// Mirrors the existing PromotionStrategy controller behavior.
		if ctp.Status.Active.Dry.Sha == ctp.Status.Proposed.Dry.Sha {
			logger.V(4).Info("Skipping gate update — no proposed change in gated environment",
				"branch", env.Branch,
				"activeDrySha", ctp.Status.Active.Dry.Sha,
				"proposedDrySha", ctp.Status.Proposed.Dry.Sha,
			)
			continue
		}

		gatedStatus, ok := statusByBranch[env.Branch]
		if !ok {
			logger.V(4).Info("Skipping env, no PromotionStrategy status yet", "branch", env.Branch)
			continue
		}

		targetDrySha := getEffectiveHydratedDrySha(gatedStatus)
		currentActiveCommitTime := gatedStatus.Active.Dry.CommitTime

		// Walk every parent and collect per-parent results.
		parentResults := make([]promoterv1alpha1.DagParentStatus, 0, len(env.DependsOn))
		aggregatePending := false
		var aggregateReason string
		for _, parentBranch := range env.DependsOn {
			res := evaluateParentChain(g, statusByBranch, parentBranch, targetDrySha, currentActiveCommitTime)
			parentResults = append(parentResults, promoterv1alpha1.DagParentStatus{
				Branch:         parentBranch,
				Phase:          res.Phase,
				CommitStatuses: res.CommitStatuses,
			})
			if res.Pending && !aggregatePending {
				aggregatePending = true
				aggregateReason = res.Reason
			}
		}

		aggregatePhase := promoterv1alpha1.CommitPhaseSuccess
		if aggregatePending {
			aggregatePhase = promoterv1alpha1.CommitPhasePending
		}

		envStatuses = append(envStatuses, promoterv1alpha1.DagEnvironmentStatus{
			Branch:         env.Branch,
			AggregatePhase: aggregatePhase,
			DrySha:         targetDrySha,
			Parents:        parentResults,
		})

		cs, applyErr := r.upsertGateCommitStatus(ctx, &dag, ctp, key, aggregatePhase, aggregateReason, parentResults)
		if applyErr != nil {
			return ctrl.Result{}, fmt.Errorf("failed to upsert gate CommitStatus for branch %q: %w", env.Branch, applyErr)
		}
		produced = append(produced, cs)
	}

	dag.Status.Environments = envStatuses

	// Garbage-collect CommitStatuses owned by this DagCommitStatus whose target branch is no
	// longer a non-root environment in the graph. Note: we GC by *target branch*, not by
	// "did we produce a new revision this iteration", because we deliberately skip writing a
	// new revision for envs whose ctp has active.dry == proposed.dry (no proposed change).
	// Skipping the write must not destroy a previously-written gate.
	validBranches := make(map[string]struct{}, len(dag.Spec.Environments))
	for _, env := range dag.Spec.Environments {
		if len(env.DependsOn) == 0 {
			continue
		}
		validBranches[env.Branch] = struct{}{}
	}
	if cleanupErr := r.cleanupOrphanedCommitStatuses(ctx, &dag, validBranches); cleanupErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to cleanup orphaned CommitStatus resources: %w", cleanupErr)
	}

	// Compute the unreferenced envs (informational, no condition).
	unreferenced := make([]string, 0)
	for _, env := range dag.Spec.Environments {
		if len(env.DependsOn) == 0 {
			continue
		}
		if !hasReferencedKey(&ps, env.Branch, key) {
			unreferenced = append(unreferenced, env.Branch)
		}
	}
	sort.Strings(unreferenced)
	if len(unreferenced) > 0 {
		dag.Status.UnreferencedEnvironments = unreferenced
	}

	// Inherit not-ready conditions from any produced CommitStatus children that aren't ready.
	utils.InheritNotReadyConditionFromObjects(&dag, promoterConditions.PreviousEnvironmentCommitStatusNotReady, produced...)

	requeueDuration, durErr := settings.GetRequeueDuration[promoterv1alpha1.DagCommitStatusConfiguration](ctx, r.SettingsMgr)
	if durErr != nil {
		// Fall back to a sensible default rather than failing reconciliation outright.
		requeueDuration = 5 * time.Minute
	}

	return ctrl.Result{Requeue: true, RequeueAfter: requeueDuration}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DagCommitStatusReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	rateLimiter, err := settings.GetRateLimiterDirect[promoterv1alpha1.DagCommitStatusConfiguration, ctrl.Request](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get DagCommitStatus rate limiter: %w", err)
	}

	maxConcurrentReconciles, err := settings.GetMaxConcurrentReconcilesDirect[promoterv1alpha1.DagCommitStatusConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get DagCommitStatus max concurrent reconciles: %w", err)
	}

	err = ctrl.NewControllerManagedBy(mgr).
		For(&promoterv1alpha1.DagCommitStatus{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&promoterv1alpha1.PromotionStrategy{}, r.enqueueDagCommitStatusForPromotionStrategy()).
		Watches(&promoterv1alpha1.ChangeTransferPolicy{}, r.enqueueDagCommitStatusForCTP()).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles, RateLimiter: rateLimiter}).
		Complete(r)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}
	return nil
}

func (r *DagCommitStatusReconciler) setNotReady(dag *promoterv1alpha1.DagCommitStatus, reason promoterConditions.CommonReason, message string) {
	meta.SetStatusCondition(dag.GetConditions(), metav1.Condition{
		Type:               string(promoterConditions.Ready),
		Status:             metav1.ConditionFalse,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: dag.Generation,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func (r *DagCommitStatusReconciler) upsertGateCommitStatus(
	ctx context.Context,
	dag *promoterv1alpha1.DagCommitStatus,
	ctp *promoterv1alpha1.ChangeTransferPolicy,
	key string,
	phase promoterv1alpha1.CommitStatusPhase,
	pendingReason string,
	parents []promoterv1alpha1.DagParentStatus,
) (*promoterv1alpha1.CommitStatus, error) {
	logger := log.FromContext(ctx)

	csName := utils.KubeSafeUniqueName(ctx, promoterv1alpha1.PreviousEnvProposedCommitPrefixNameLabel+ctp.Name)

	kind := reflect.TypeOf(promoterv1alpha1.DagCommitStatus{}).Name()
	gvk := promoterv1alpha1.GroupVersion.WithKind(kind)

	// Build description summarising contributing parents.
	parentBranches := make([]string, 0, len(parents))
	for _, p := range parents {
		parentBranches = append(parentBranches, p.Branch)
	}
	sort.Strings(parentBranches)
	parentList := joinComma(parentBranches)

	description := parentList + " - synced and healthy"
	if phase == promoterv1alpha1.CommitPhasePending && pendingReason != "" {
		description = pendingReason
	}
	displayName := parentList + " - synced and healthy"

	// For back-compat: write the flat per-key annotation. Multi-parent may collide;
	// the canonical place for the disaggregated view is DagCommitStatus.status.environments[].
	flat := flattenParentCommitStatuses(parents)
	yamlStatusMap, err := yaml.Marshal(flat)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal previous environment commit statuses: %w", err)
	}

	var url string
	// Preserve the single-status URL convenience: if exactly one parent contributed exactly one
	// commit status, surface its URL on the gate.
	if len(parents) == 1 && len(parents[0].CommitStatuses) == 1 {
		url = parents[0].CommitStatuses[0].Url
	}

	commitStatusApply := acv1alpha1.CommitStatus(csName, ctp.Namespace).
		WithLabels(map[string]string{
			promoterv1alpha1.CommitStatusLabel:    key,
			promoterv1alpha1.DagCommitStatusLabel: utils.KubeSafeLabel(dag.Name),
			promoterv1alpha1.EnvironmentLabel:     utils.KubeSafeLabel(ctp.Spec.ActiveBranch),
		}).
		WithAnnotations(map[string]string{
			promoterv1alpha1.CommitStatusPreviousEnvironmentStatusesAnnotation: string(yamlStatusMap),
		}).
		WithOwnerReferences(acmetav1.OwnerReference().
			WithAPIVersion(gvk.GroupVersion().String()).
			WithKind(gvk.Kind).
			WithName(dag.Name).
			WithUID(dag.UID).
			WithController(true).
			WithBlockOwnerDeletion(true)).
		WithSpec(acv1alpha1.CommitStatusSpec().
			WithRepositoryReference(acv1alpha1.ObjectReference().
				WithName(ctp.Spec.RepositoryReference.Name)).
			WithSha(ctp.Status.Proposed.Hydrated.Sha).
			WithName(displayName).
			WithDescription(description).
			WithPhase(phase).
			WithUrl(url))

	commitStatus := &promoterv1alpha1.CommitStatus{}
	commitStatus.Name = csName
	commitStatus.Namespace = ctp.Namespace
	if err := r.Patch(ctx, commitStatus, utils.ApplyPatch{ApplyConfig: commitStatusApply},
		client.FieldOwner(constants.DagCommitStatusControllerFieldOwner), client.ForceOwnership); err != nil {
		return nil, fmt.Errorf("failed to apply gate CommitStatus: %w", err)
	}
	logger.V(4).Info("Applied gate CommitStatus",
		"branch", ctp.Spec.ActiveBranch, "phase", phase, "sha", ctp.Status.Proposed.Hydrated.Sha)
	return commitStatus, nil
}

// cleanupOrphanedCommitStatuses deletes CommitStatuses owned by this DagCommitStatus
// whose target environment branch is no longer a non-root entry in the graph. Gates
// for branches that are still in the graph are preserved even when this iteration skipped
// writing a new revision (e.g. because the gated environment's CTP has active.dry == proposed.dry).
func (r *DagCommitStatusReconciler) cleanupOrphanedCommitStatuses(
	ctx context.Context,
	dag *promoterv1alpha1.DagCommitStatus,
	validBranches map[string]struct{},
) error {
	logger := log.FromContext(ctx)

	var csList promoterv1alpha1.CommitStatusList
	if err := r.List(ctx, &csList,
		client.InNamespace(dag.Namespace),
		client.MatchingLabels{promoterv1alpha1.DagCommitStatusLabel: utils.KubeSafeLabel(dag.Name)},
	); err != nil {
		return fmt.Errorf("failed to list CommitStatus resources for DagCommitStatus %q: %w", dag.Name, err)
	}

	for i := range csList.Items {
		cs := &csList.Items[i]
		if !metav1.IsControlledBy(cs, dag) {
			continue
		}
		// Determine the gate's target branch from the EnvironmentLabel we wrote at apply time.
		// If a gate has no such label, it predates this controller's labeling scheme — leave it alone.
		envLabel, hasLabel := cs.Labels[promoterv1alpha1.EnvironmentLabel]
		if !hasLabel {
			continue
		}
		// The label was applied via utils.KubeSafeLabel — look up the matching branch by
		// comparing the label-encoded form of every valid branch.
		stillValid := false
		for branch := range validBranches {
			if utils.KubeSafeLabel(branch) == envLabel {
				stillValid = true
				break
			}
		}
		if stillValid {
			continue
		}
		logger.Info("Deleting orphaned gate CommitStatus", "commitStatusName", cs.Name, "envLabel", envLabel)
		if err := r.Delete(ctx, cs); err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to delete orphaned CommitStatus %q: %w", cs.Name, err)
		}
		r.Recorder.Eventf(dag, nil, "Normal", constants.OrphanedCommitStatusDeletedReason,
			"CleaningOrphanedResources", constants.OrphanedCommitStatusDeletedMessage, cs.Name)
	}
	return nil
}

// enqueueDagCommitStatusForPromotionStrategy enqueues all DagCommitStatuses pointing
// at the given PromotionStrategy when it changes.
func (r *DagCommitStatusReconciler) enqueueDagCommitStatusForPromotionStrategy() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
		ps, ok := obj.(*promoterv1alpha1.PromotionStrategy)
		if !ok {
			return nil
		}
		var dagList promoterv1alpha1.DagCommitStatusList
		if err := r.List(ctx, &dagList, client.InNamespace(ps.Namespace)); err != nil {
			log.FromContext(ctx).Error(err, "failed to list DagCommitStatus resources")
			return nil
		}
		var requests []ctrl.Request
		for i := range dagList.Items {
			d := &dagList.Items[i]
			if d.Spec.PromotionStrategyRef.Name == ps.Name {
				requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)})
			}
		}
		return requests
	})
}

// enqueueDagCommitStatusForCTP enqueues all DagCommitStatuses whose referenced PromotionStrategy
// owns the given CTP. We use the CTP's PromotionStrategyLabel to find the PS name.
func (r *DagCommitStatusReconciler) enqueueDagCommitStatusForCTP() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
		ctp, ok := obj.(*promoterv1alpha1.ChangeTransferPolicy)
		if !ok {
			return nil
		}
		psNameLabel := ctp.Labels[promoterv1alpha1.PromotionStrategyLabel]
		if psNameLabel == "" {
			return nil
		}
		var dagList promoterv1alpha1.DagCommitStatusList
		if err := r.List(ctx, &dagList, client.InNamespace(ctp.Namespace)); err != nil {
			log.FromContext(ctx).Error(err, "failed to list DagCommitStatus resources")
			return nil
		}
		var requests []ctrl.Request
		for i := range dagList.Items {
			d := &dagList.Items[i]
			if utils.KubeSafeLabel(d.Spec.PromotionStrategyRef.Name) == psNameLabel {
				requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)})
			}
		}
		return requests
	})
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
