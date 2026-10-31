/*
Copyright 2026.

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
	"maps"
	"slices"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/resourcetemplate"
	"github.com/argoproj-labs/gitops-promoter/internal/settings"
	promoterConditions "github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

// defaultKubernetesResourcePollInterval is used when spec.pollInterval is zero (the CRD default is 30s).
const defaultKubernetesResourcePollInterval = 30 * time.Second

// KubernetesResourceCommitStatusReconciler reconciles KubernetesResourceCommitStatus resources: for each
// applicable environment it renders the referenced wrapped template, creates the real Kubernetes resource for
// the reported SHA, evaluates the phase expression against that resource's live state, and upserts a
// CommitStatus so the gate reflects it.
type KubernetesResourceCommitStatusReconciler struct {
	client.Client
	// APIReader reads the created resources without the manager cache, so no informer is started for
	// arbitrary kinds (a cluster-wide Job informer, for example).
	APIReader   client.Reader
	Scheme      *runtime.Scheme
	Recorder    events.EventRecorder
	SettingsMgr *settings.Manager
	EnqueueCTP  CTPEnqueueFunc
	// RESTConfig and RESTMapper are used at setup to generate the template CRDs before the cache starts. When
	// RESTConfig is nil, setup skips that step (the ControllerConfiguration controller still syncs on change).
	RESTConfig *rest.Config
	RESTMapper meta.RESTMapper

	evaluator *resourcetemplate.PhaseEvaluator
}

// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=kubernetesresourcecommitstatuses,verbs=get;list;watch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=kubernetesresourcecommitstatuses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=kubernetesresourcecommitstatuses/finalizers,verbs=update
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=commitstatuses,verbs=get;list;watch;patch;create;delete
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies,verbs=get;list;watch
// +kubebuilder:rbac:groups=templates.promoter.argoproj.io,resources=*,verbs=get;list;watch
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update

// Reconcile fetches the KubernetesResourceCommitStatus, its PromotionStrategy, and the referenced template,
// then for every applicable environment ensures the resource for the reported SHA exists, evaluates its phase,
// and upserts the CommitStatus. Resources of superseded SHAs and orphaned CommitStatuses are cleaned up, and
// ChangeTransferPolicies are enqueued when an environment transitions to success.
//
//nolint:gocyclo // Linear gate reconcile; the steps are documented inline.
func (r *KubernetesResourceCommitStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling KubernetesResourceCommitStatus")
	startTime := time.Now()

	var krcs promoterv1alpha1.KubernetesResourceCommitStatus
	var previousReady *metav1.Condition
	// This function applies the resource status via Server-Side Apply at the end of the reconciliation. Don't write status manually.
	defer func() {
		utils.HandleReconciliationResult(ctx, startTime, &krcs, r.Client, r.Recorder, constants.KubernetesResourceCommitStatusControllerFieldOwner, &result, &err, &previousReady)
	}()

	// 1. Fetch the KubernetesResourceCommitStatus instance
	if err := r.Get(ctx, req.NamespacedName, &krcs); err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Info("KubernetesResourceCommitStatus not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get KubernetesResourceCommitStatus %q: %w", req.Name, err)
	}

	// Remove any existing Ready condition. We want to start fresh.
	previousReady = utils.RemoveReadyCondition(&krcs)

	if err := ensureControllerInstanceIDStable(ctx, r.SettingsMgr); err != nil {
		return ctrl.Result{}, err
	}

	// 2. Fetch the referenced PromotionStrategy
	var ps promoterv1alpha1.PromotionStrategy
	if err := r.Get(ctx, client.ObjectKey{Namespace: krcs.Namespace, Name: krcs.Spec.PromotionStrategyRef.Name}, &ps); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("referenced PromotionStrategy %q not found: %w", krcs.Spec.PromotionStrategyRef.Name, err)
		}
		return ctrl.Result{}, fmt.Errorf("failed to get PromotionStrategy %q: %w", krcs.Spec.PromotionStrategyRef.Name, err)
	}

	// 3. Resolve the configured template source and fetch the wrapped template
	source, err := r.resolveTemplateSource(ctx, &krcs)
	if err != nil {
		return ctrl.Result{}, err
	}
	tmpl := &unstructured.Unstructured{}
	tmpl.SetGroupVersionKind(resourcetemplate.TemplateGVK(source))
	if err := r.Get(ctx, client.ObjectKey{Namespace: krcs.Namespace, Name: krcs.Spec.TemplateRef.Name}, tmpl); err != nil {
		if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return ctrl.Result{}, fmt.Errorf("referenced template %s %q not found in namespace %q: %w",
				krcs.Spec.TemplateRef.Kind, krcs.Spec.TemplateRef.Name, krcs.Namespace, err)
		}
		return ctrl.Result{}, fmt.Errorf("failed to get template %s %q: %w", krcs.Spec.TemplateRef.Kind, krcs.Spec.TemplateRef.Name, err)
	}

	// 4. Process each applicable environment
	outcome, err := r.processEnvironments(ctx, &krcs, &ps, source, tmpl)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to process environments: %w", err)
	}

	// 5. Delete resources created for superseded SHAs (subject to spec.historyLimit)
	if err := r.cleanupSupersededResources(ctx, &krcs, source, outcome.currentShaByBranch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to clean up superseded resources: %w", err)
	}

	// 6. Clean up orphaned CommitStatus resources that are no longer in the environment list
	if err := utils.CleanupOrphanedCommitStatuses(ctx, r.Client, r.Recorder, &krcs, outcome.commitStatuses); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to cleanup orphaned CommitStatus resources: %w", err)
	}

	// 7. Inherit conditions from CommitStatus objects
	utils.InheritNotReadyConditionFromObjects(&krcs, promoterConditions.CommitStatusesNotReady, outcome.commitStatuses...)

	// 8. If any environment transitioned to success, enqueue the corresponding ChangeTransferPolicies
	utils.EnqueueChangeTransferPolicies(ctx, r.EnqueueCTP, &ps, outcome.transitioned, "kubernetes resource gate transition")

	return ctrl.Result{RequeueAfter: r.requeueAfter(ctx, &krcs)}, nil
}

// SetupWithManager generates the template CRDs configured in ControllerConfiguration (when RESTConfig is set)
// and registers the controller: it watches KubernetesResourceCommitStatus and PromotionStrategy so reconciles
// run when a strategy's environment SHAs change. Created resources are polled (spec.pollInterval), not watched.
func (r *KubernetesResourceCommitStatusReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	r.evaluator = resourcetemplate.NewPhaseEvaluator()
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}

	if r.RESTConfig != nil {
		if err := r.syncTemplateCRDs(ctx, mgr); err != nil {
			// A bad entry must not keep every other controller from starting; the ControllerConfiguration
			// controller reports the error on its Ready condition and retries on the next change.
			log.FromContext(ctx).Error(err, "failed to generate KubernetesResourceCommitStatus template CRDs at startup")
		}
	}

	// Use Direct methods to read configuration from the API server without cache during setup.
	// The cache is not started during SetupWithManager, so we must use the non-cached API reader.
	rateLimiter, err := settings.GetRateLimiterDirect[promoterv1alpha1.KubernetesResourceCommitStatusConfiguration, ctrl.Request](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get KubernetesResourceCommitStatus rate limiter: %w", err)
	}

	maxConcurrentReconciles, err := settings.GetMaxConcurrentReconcilesDirect[promoterv1alpha1.KubernetesResourceCommitStatusConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get KubernetesResourceCommitStatus max concurrent reconciles: %w", err)
	}

	err = ctrl.NewControllerManagedBy(mgr).
		For(&promoterv1alpha1.KubernetesResourceCommitStatus{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&promoterv1alpha1.PromotionStrategy{}, CommitStatusGatePromotionStrategyWatchHandler[promoterv1alpha1.KubernetesResourceCommitStatusList](r.Client)).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles, RateLimiter: rateLimiter}).
		Named("kubernetesresourcecommitstatus").
		Complete(r)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}
	return nil
}

// syncTemplateCRDs generates the wrapped template CRDs with a direct (uncached) client before the cache starts.
func (r *KubernetesResourceCommitStatusReconciler) syncTemplateCRDs(ctx context.Context, mgr ctrl.Manager) error {
	sources, err := r.SettingsMgr.GetKubernetesResourceTemplatesDirect(ctx)
	if err != nil {
		return fmt.Errorf("read templates from ControllerConfiguration: %w", err)
	}
	if len(sources) == 0 {
		return nil
	}
	directClient, err := client.New(r.RESTConfig, client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return fmt.Errorf("create direct client: %w", err)
	}
	mapper := r.RESTMapper
	if mapper == nil {
		mapper = mgr.GetRESTMapper()
	}
	syncer, err := resourcetemplate.NewSyncer(r.RESTConfig, directClient, mapper)
	if err != nil {
		return fmt.Errorf("create template CRD syncer: %w", err)
	}
	if err := syncer.Sync(ctx, sources); err != nil {
		return fmt.Errorf("sync template CRDs: %w", err)
	}
	return nil
}

// resolveTemplateSource finds the configured source whose generated kind matches spec.templateRef.kind.
func (r *KubernetesResourceCommitStatusReconciler) resolveTemplateSource(ctx context.Context, krcs *promoterv1alpha1.KubernetesResourceCommitStatus) (resourcetemplate.Source, error) {
	sources, err := r.SettingsMgr.GetKubernetesResourceTemplates(ctx)
	if err != nil {
		return resourcetemplate.Source{}, fmt.Errorf("failed to read templates from ControllerConfiguration: %w", err)
	}
	source, ok := resourcetemplate.FindSource(sources, krcs.Spec.TemplateRef.Kind)
	if !ok {
		return resourcetemplate.Source{}, fmt.Errorf("template kind %q is not configured in ControllerConfiguration.spec.kubernetesResourceCommitStatus.templates",
			krcs.Spec.TemplateRef.Kind)
	}
	return source, nil
}

// environmentsOutcome is what processEnvironments returns to Reconcile.
type environmentsOutcome struct {
	currentShaByBranch map[string]string
	commitStatuses     []*promoterv1alpha1.CommitStatus
	transitioned       []string
}

// processEnvironments ensures the resource for each applicable environment's reported SHA exists, evaluates its
// phase, upserts the CommitStatus, and rewrites krcs.Status.Environments.
func (r *KubernetesResourceCommitStatusReconciler) processEnvironments(
	ctx context.Context,
	krcs *promoterv1alpha1.KubernetesResourceCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
	source resourcetemplate.Source,
	tmpl *unstructured.Unstructured,
) (environmentsOutcome, error) {
	logger := log.FromContext(ctx)

	previousByBranch := make(map[string]promoterv1alpha1.KubernetesResourceCommitStatusEnvironmentStatus, len(krcs.Status.Environments))
	for _, env := range krcs.Status.Environments {
		previousByBranch[env.Branch] = env
	}
	psEnvByBranch := make(map[string]*promoterv1alpha1.EnvironmentStatus, len(ps.Status.Environments))
	for i := range ps.Status.Environments {
		psEnvByBranch[ps.Status.Environments[i].Branch] = &ps.Status.Environments[i]
	}

	applicableEnvs := utils.GetApplicableEnvironments(ps, krcs.Spec.Key, krcs.Spec.ReportOn)
	outcome := environmentsOutcome{
		currentShaByBranch: make(map[string]string, len(applicableEnvs)),
		commitStatuses:     make([]*promoterv1alpha1.CommitStatus, 0, len(applicableEnvs)),
	}
	krcs.Status.Environments = make([]promoterv1alpha1.KubernetesResourceCommitStatusEnvironmentStatus, 0, len(applicableEnvs))

	for _, env := range applicableEnvs {
		envStatus, found := psEnvByBranch[env.Branch]
		if !found {
			return outcome, fmt.Errorf("environment %q not found in PromotionStrategy status", env.Branch)
		}
		sha := reportedSha(envStatus, krcs.Spec.ReportOn)
		previous := previousByBranch[env.Branch]
		if sha == "" {
			// Nothing to report on yet. Keep the last report so a transient empty status does not flap.
			logger.Info("No SHA available for environment yet", "branch", env.Branch, "reportOn", krcs.Spec.ReportOn)
			if previous.Branch != "" {
				krcs.Status.Environments = append(krcs.Status.Environments, previous)
			}
			continue
		}
		outcome.currentShaByBranch[env.Branch] = sha

		envResult, err := r.processEnvironment(ctx, krcs, ps, source, tmpl, envStatus, sha, previous)
		if err != nil {
			return outcome, fmt.Errorf("environment %q: %w", env.Branch, err)
		}
		krcs.Status.Environments = append(krcs.Status.Environments, envResult.status)
		outcome.commitStatuses = append(outcome.commitStatuses, envResult.commitStatus)

		previousPhase := string(previous.Phase)
		if previousPhase != string(promoterv1alpha1.CommitPhaseSuccess) && envResult.status.Phase == promoterv1alpha1.CommitPhaseSuccess {
			outcome.transitioned = append(outcome.transitioned, env.Branch)
		}
		// Emit only after the upsert succeeded so the event always describes persisted state.
		emitCommitStatusPhaseChangedEvent(r.Recorder, krcs, krcs.Spec.Key, env.Branch, previousPhase, string(envResult.status.Phase))
	}

	return outcome, nil
}

type environmentResult struct {
	commitStatus *promoterv1alpha1.CommitStatus
	status       promoterv1alpha1.KubernetesResourceCommitStatusEnvironmentStatus
}

// processEnvironment handles one environment: ensure the resource exists, evaluate the phase, upsert the CommitStatus.
func (r *KubernetesResourceCommitStatusReconciler) processEnvironment(
	ctx context.Context,
	krcs *promoterv1alpha1.KubernetesResourceCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
	source resourcetemplate.Source,
	tmpl *unstructured.Unstructured,
	envStatus *promoterv1alpha1.EnvironmentStatus,
	sha string,
	previous promoterv1alpha1.KubernetesResourceCommitStatusEnvironmentStatus,
) (environmentResult, error) {
	gvk := resourcetemplate.SourceGVK(source)
	resourceName := kubernetesResourceName(krcs, envStatus.Branch, sha)
	td := resourcetemplate.NewTemplateData(krcs, ps, envStatus, sha, resourceName)

	// Success is sticky for a SHA: once reported, a missing resource (for example a Job removed by
	// ttlSecondsAfterFinished) must not re-run the check and close a gate that was already open. Pending and
	// failed resources are recreated when missing, so deleting a failed Job re-runs it.
	succeeded := previous.ReportedSha == sha && previous.Phase == promoterv1alpha1.CommitPhaseSuccess
	live, err := r.ensureResource(ctx, krcs, gvk, tmpl, td, !succeeded)
	if err != nil {
		return environmentResult{}, err
	}

	var (
		phase            promoterv1alpha1.CommitStatusPhase
		description, url string
		evaluationTime   = previous.LastEvaluationTime
	)
	if live == nil {
		// Only reachable when the resource of a succeeded SHA is gone: keep the last report as is.
		phase, description, url = previous.Phase, previous.Description, previous.Url
	} else {
		phase, description, url, err = r.evaluate(krcs, ps, envStatus, live, td, previous)
		if err != nil {
			return environmentResult{}, err
		}
		evaluationTime = ptr.To(metav1.Now())
	}

	cs, err := utils.UpsertCommitStatus(ctx, r.Client, utils.UpsertCommitStatusParams{
		Parent:      krcs,
		RepoRefName: ps.Spec.RepositoryReference.Name,
		Branch:      envStatus.Branch,
		Sha:         sha,
		Key:         krcs.Spec.Key,
		Description: description,
		Url:         url,
		Phase:       phase,
		PublishTo:   krcs.Spec.PublishTo,
		FieldOwner:  constants.KubernetesResourceCommitStatusControllerFieldOwner,
	})
	if err != nil {
		return environmentResult{}, fmt.Errorf("failed to upsert CommitStatus: %w", err)
	}

	return environmentResult{
		commitStatus: cs,
		status: promoterv1alpha1.KubernetesResourceCommitStatusEnvironmentStatus{
			Branch:                      envStatus.Branch,
			GateEnvironmentCommitStatus: utils.GateEnvironmentCommitStatusFromCommitStatus(cs),
			Resource: &promoterv1alpha1.KubernetesResourceReference{
				APIVersion: gvk.GroupVersion().String(),
				Kind:       gvk.Kind,
				Namespace:  krcs.Namespace,
				Name:       resourceName,
			},
			ResourceExists:     live != nil,
			LastEvaluationTime: evaluationTime,
		},
	}, nil
}

// evaluate runs the phase expression against the live resource and renders the description and URL templates.
func (r *KubernetesResourceCommitStatusReconciler) evaluate(
	krcs *promoterv1alpha1.KubernetesResourceCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
	envStatus *promoterv1alpha1.EnvironmentStatus,
	live *unstructured.Unstructured,
	td resourcetemplate.TemplateData,
	previous promoterv1alpha1.KubernetesResourceCommitStatusEnvironmentStatus,
) (promoterv1alpha1.CommitStatusPhase, string, string, error) {
	phase, err := r.evaluator.EvaluatePhase(krcs.Spec.Phase.Expression, resourcetemplate.PhaseInput{
		Resource:                       live.Object,
		Environment:                    envStatus,
		PromotionStrategy:              ps,
		KubernetesResourceCommitStatus: krcs,
		Branch:                         envStatus.Branch,
		Sha:                            td.Sha,
		Phase:                          string(previous.Phase),
	})
	if err != nil {
		return "", "", "", fmt.Errorf("failed to evaluate phase expression: %w", err)
	}

	td.Phase = string(phase)
	td.Resource = live.Object
	description, err := resourcetemplate.RenderString(krcs.Spec.DescriptionTemplate, td)
	if err != nil {
		return "", "", "", fmt.Errorf("description template: %w", err)
	}
	if description == "" {
		description = defaultKubernetesResourceDescription(live.GetKind(), td.ResourceName, phase)
	}
	url, err := resourcetemplate.RenderString(krcs.Spec.UrlTemplate, td)
	if err != nil {
		return "", "", "", fmt.Errorf("url template: %w", err)
	}
	return phase, description, url, nil
}

// ensureResource returns the live resource for td. When it does not exist and create is true, it is created
// from the template; when create is false, (nil, nil) is returned so the caller keeps the previous report.
func (r *KubernetesResourceCommitStatusReconciler) ensureResource(
	ctx context.Context,
	krcs *promoterv1alpha1.KubernetesResourceCommitStatus,
	gvk schema.GroupVersionKind,
	tmpl *unstructured.Unstructured,
	td resourcetemplate.TemplateData,
	create bool,
) (*unstructured.Unstructured, error) {
	logger := log.FromContext(ctx)

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(gvk)
	err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: td.Namespace, Name: td.ResourceName}, live)
	if err == nil {
		return live, nil
	}
	if !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get %s %q: %w", gvk.Kind, td.ResourceName, err)
	}
	if !create {
		logger.Info("Resource of a succeeded SHA no longer exists; keeping the last report", "kind", gvk.Kind, "name", td.ResourceName, "branch", td.Branch)
		return nil, nil
	}

	desired, err := resourcetemplate.RenderTemplate(tmpl, gvk, td)
	if err != nil {
		return nil, fmt.Errorf("failed to render template: %w", err)
	}
	labels := desired.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, utils.CommitStatusStandardLabels(krcs, td.Branch, krcs.Spec.Key))
	labels[promoterv1alpha1.ReportedShaLabel] = td.Sha
	desired.SetLabels(labels)
	desired.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(krcs, promoterv1alpha1.GroupVersion.WithKind("KubernetesResourceCommitStatus"))})

	if err := r.Create(ctx, desired); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create %s %q: %w", gvk.Kind, td.ResourceName, err)
		}
		// Created by a concurrent reconcile between our Get and Create; read it back.
		if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: td.Namespace, Name: td.ResourceName}, live); err != nil {
			return nil, fmt.Errorf("failed to get %s %q after create conflict: %w", gvk.Kind, td.ResourceName, err)
		}
		return live, nil
	}
	logger.Info("Created resource from template", "kind", gvk.Kind, "name", td.ResourceName, "branch", td.Branch, "sha", td.Sha)
	r.Recorder.Eventf(krcs, nil, "Normal", constants.KubernetesResourceCreatedReason, "CreatingResource",
		constants.KubernetesResourceCreatedMessage, gvk.Kind, td.ResourceName, krcs.Spec.Key, td.Branch, td.Sha)
	return desired, nil
}

// cleanupSupersededResources deletes resources this gate created for SHAs that are no longer reported, keeping
// the spec.historyLimit most recent per environment. Resources of environments that no longer apply are deleted
// entirely. Only resources controlled by krcs are considered.
func (r *KubernetesResourceCommitStatusReconciler) cleanupSupersededResources(
	ctx context.Context,
	krcs *promoterv1alpha1.KubernetesResourceCommitStatus,
	source resourcetemplate.Source,
	currentShaByBranch map[string]string,
) error {
	logger := log.FromContext(ctx)
	gvk := resourcetemplate.SourceGVK(source)

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	err := r.APIReader.List(ctx, list, client.InNamespace(krcs.Namespace), client.MatchingLabels{
		utils.CommitStatusGateLabelKeyForParent(krcs): utils.KubeSafeLabel(krcs.Name),
	})
	if err != nil {
		return fmt.Errorf("failed to list %s resources: %w", gvk.Kind, err)
	}

	currentShaByEnvLabel := make(map[string]string, len(currentShaByBranch))
	for branch, sha := range currentShaByBranch {
		currentShaByEnvLabel[utils.KubeSafeLabel(branch)] = sha
	}

	superseded := make(map[string][]unstructured.Unstructured)
	for i := range list.Items {
		item := list.Items[i]
		if !metav1.IsControlledBy(&item, krcs) {
			continue
		}
		envLabel := item.GetLabels()[promoterv1alpha1.EnvironmentLabel]
		if currentSha, ok := currentShaByEnvLabel[envLabel]; ok && item.GetLabels()[promoterv1alpha1.ReportedShaLabel] == currentSha {
			continue
		}
		superseded[envLabel] = append(superseded[envLabel], item)
	}

	historyLimit := int(ptr.Deref(krcs.Spec.HistoryLimit, 0))
	for envLabel, items := range superseded {
		keep := 0
		if _, applicable := currentShaByEnvLabel[envLabel]; applicable {
			keep = historyLimit
		}
		// Newest first; the first `keep` entries survive.
		slices.SortFunc(items, func(a, b unstructured.Unstructured) int {
			return b.GetCreationTimestamp().Compare(a.GetCreationTimestamp().Time)
		})
		for i := keep; i < len(items); i++ {
			item := items[i]
			if err := r.Delete(ctx, &item); err != nil {
				if k8serrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("failed to delete superseded %s %q: %w", gvk.Kind, item.GetName(), err)
			}
			logger.Info("Deleted resource of superseded SHA", "kind", gvk.Kind, "name", item.GetName(), "environment", envLabel)
			r.Recorder.Eventf(krcs, nil, "Normal", constants.KubernetesResourceDeletedReason, "CleaningSupersededResources",
				constants.KubernetesResourceDeletedMessage, gvk.Kind, item.GetName(), item.GetLabels()[promoterv1alpha1.ReportedShaLabel], envLabel)
		}
	}
	return nil
}

// requeueAfter polls at spec.pollInterval while any environment is pending, otherwise uses the configured requeue duration.
func (r *KubernetesResourceCommitStatusReconciler) requeueAfter(ctx context.Context, krcs *promoterv1alpha1.KubernetesResourceCommitStatus) time.Duration {
	logger := log.FromContext(ctx)
	for _, env := range krcs.Status.Environments {
		if env.Phase == promoterv1alpha1.CommitPhasePending {
			if krcs.Spec.PollInterval.Duration > 0 {
				return krcs.Spec.PollInterval.Duration
			}
			return defaultKubernetesResourcePollInterval
		}
	}
	requeue, err := settings.GetRequeueDuration[promoterv1alpha1.KubernetesResourceCommitStatusConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		logger.Error(err, "failed to get requeue duration, using 5 minutes")
		return 5 * time.Minute
	}
	return requeue
}

// reportedSha returns the hydrated SHA selected by spec.reportOn for an environment.
func reportedSha(envStatus *promoterv1alpha1.EnvironmentStatus, reportOn string) string {
	if reportOn == constants.CommitRefActive {
		return envStatus.Active.Hydrated.Sha
	}
	return envStatus.Proposed.Hydrated.Sha
}

// kubernetesResourceName returns the deterministic name of the resource created for one environment and SHA.
func kubernetesResourceName(krcs *promoterv1alpha1.KubernetesResourceCommitStatus, branch, sha string) string {
	return utils.KubeSafeUniqueName(krcs.Name + "-" + branch + "-" + utils.TruncateString(sha, 7))
}

// defaultKubernetesResourceDescription is the CommitStatus description when spec.descriptionTemplate is empty.
func defaultKubernetesResourceDescription(kind, name string, phase promoterv1alpha1.CommitStatusPhase) string {
	switch phase {
	case promoterv1alpha1.CommitPhaseSuccess:
		return fmt.Sprintf("%s %s is successful", kind, name)
	case promoterv1alpha1.CommitPhaseFailure:
		return fmt.Sprintf("%s %s is failing", kind, name)
	default:
		return fmt.Sprintf("Waiting for %s %s", kind, name)
	}
}
