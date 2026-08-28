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
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/argoproj-labs/gitops-promoter/internal/git"
	"github.com/argoproj-labs/gitops-promoter/internal/gitauth"
	"github.com/argoproj-labs/gitops-promoter/internal/settings"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	acmetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	acv1alpha1 "github.com/argoproj-labs/gitops-promoter/applyconfiguration/api/v1alpha1"
	prlabels "github.com/argoproj-labs/gitops-promoter/internal/labels"
	promoterConditions "github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
)

// CTPEnqueueFunc is a function type that can be used to enqueue CTP reconcile requests
// without modifying the CTP object. This is used by other controllers (like PromotionStrategy)
// to trigger CTP reconciliation without causing object conflicts.
type CTPEnqueueFunc func(namespace, name string)

// ChangeTransferPolicyReconciler reconciles a ChangeTransferPolicy object
type ChangeTransferPolicyReconciler struct {
	client.Client
	Recorder    events.EventRecorder
	Scheme      *runtime.Scheme
	SettingsMgr *settings.Manager

	// enqueueFunc is set during SetupWithManager and can be retrieved via GetEnqueueFunc.
	// It allows other controllers to enqueue CTP reconcile requests.
	enqueueFunc CTPEnqueueFunc

	// EnqueuePR wakes the PullRequest controller without patching the PR object.
	EnqueuePR PREnqueueFunc

	labelEvaluator prlabels.Evaluator
}

// GetEnqueueFunc returns a function that can be used to enqueue CTP reconcile requests.
// This should be called after SetupWithManager has been called.
func (r *ChangeTransferPolicyReconciler) GetEnqueueFunc() CTPEnqueueFunc {
	return r.enqueueFunc
}

//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=changetransferpolicies,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=changetransferpolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=changetransferpolicies/finalizers,verbs=update
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=pullrequests,verbs=get;list;watch;patch;create
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=pullrequests/finalizers,verbs=update
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=commitstatuses,verbs=get;list;watch
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies,verbs=get;list;watch
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=gitrepositories,verbs=get;list;watch
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=scmproviders,verbs=get;list;watch
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=clusterscmproviders,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ChangeTransferPolicy object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.2/pkg/reconcile
func (r *ChangeTransferPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling ChangeTransferPolicy")
	startTime := time.Now()

	var ctp promoterv1alpha1.ChangeTransferPolicy
	// This function applies the resource status via Server-Side Apply at the end of the reconciliation. Don't write status manually.
	var previousReady *metav1.Condition
	defer utils.HandleReconciliationResult(ctx, startTime, &ctp, r.Client, r.Recorder, constants.ChangeTransferPolicyControllerFieldOwner, &result, &err, &previousReady)

	err = r.Get(ctx, req.NamespacedName, &ctp, &client.GetOptions{})
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			logger.Info("ChangeTransferPolicy not found")
			return ctrl.Result{}, nil
		}

		logger.Error(err, "failed to get ChangeTransferPolicy")
		return ctrl.Result{}, fmt.Errorf("failed to get ChangeTransferPolicy: %w", err)
	}

	if deleted, err := r.handleFinalizer(ctx, &ctp); err != nil || deleted {
		return ctrl.Result{}, err
	}

	// Handle PR finalizer removal if PR is being deleted and CTP status is already synced
	err = r.handlePRFinalizerRemoval(ctx, &ctp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to handle PR finalizer removal: %w", err)
	}

	// Remove any existing Ready condition. We want to start fresh.
	previousReady = utils.RemoveReadyCondition(&ctp)

	if err := ensureControllerInstanceIDStable(ctx, r.SettingsMgr); err != nil {
		return ctrl.Result{}, err
	}

	scmProvider, secret, err := utils.GetScmProviderAndSecretFromRepositoryReference(ctx, r.Client, r.SettingsMgr.GetControllerNamespace(), ctp.Spec.RepositoryReference, &ctp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get ScmProvider and secret for repo %q: %w", ctp.Spec.RepositoryReference.Name, err)
	}

	gitAuthProvider, err := gitauth.CreateGitOperationsProvider(ctx, r.Client, scmProvider, secret, client.ObjectKey{Namespace: ctp.Namespace, Name: ctp.Spec.RepositoryReference.Name})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create git auth provider for ScmProvider %q: %w", scmProvider.GetName(), err)
	}
	gitRepo, err := utils.GetGitRepositoryFromObjectKey(ctx, r.Client, client.ObjectKey{Namespace: ctp.GetNamespace(), Name: ctp.Spec.RepositoryReference.Name})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get GitRepository: %w", err)
	}
	gitOperations := git.NewEnvironmentOperations(gitRepo, gitAuthProvider, ctp.Namespace+"/"+ctp.Name)

	// TODO: could probably short circuit the clone and use an ls-remote to compare the sha's of the current ctp status,
	// this would help with slamming the git provider with clone requests on controller restarts.

	err = gitOperations.CloneRepo(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to clone repo %q: %w", ctp.Spec.RepositoryReference.Name, err)
	}

	// Fetch git notes for hydrator metadata (used to track hydration completion)
	err = gitOperations.FetchNotes(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to fetch git notes: %w", err)
	}

	// Point the promotion branch at the change this policy should promote, before any status is
	// calculated from it. Policies that promote the proposed branch tip need no branch of their own.
	if ctp.Spec.SelectsCandidates() {
		var movedPromotionBranch bool
		movedPromotionBranch, err = r.selectPromotionCandidate(ctx, &ctp, gitOperations)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to select promotion candidate: %w", err)
		}
		if movedPromotionBranch {
			return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
		}
	} else {
		// Proposed already describes the newest change under this policy, so a separate candidate
		// record would only duplicate it. Clear any left behind by a previous policy.
		ctp.Status.Candidate = nil
	}

	// Snapshot the persisted status (from the Get above) before calculateStatus overwrites it,
	// so promotion lifecycle events can be emitted only on actual state transitions.
	prevStatus := ctp.Status.DeepCopy()
	if prevStatus == nil {
		prevStatus = &promoterv1alpha1.ChangeTransferPolicyStatus{}
	}
	err = r.calculateStatus(ctx, &ctp, gitOperations)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to calculate ChangeTransferPolicy status: %w", err)
	}
	r.emitPromotionLifecycleEvents(&ctp, prevStatus)

	// Bring the record of what this environment has vouched for up to date before the pull request is
	// written, since the pull request is what records the current change's verification into git.
	if err = r.refreshVerification(ctx, &ctp, gitOperations); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to refresh verification record: %w", err)
	}

	mergedProposedBranch, err := r.gitMergeStrategyOurs(ctx, gitOperations, &ctp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to git merge for conflict resolution: %w", err)
	}

	// gitMergeStrategyOurs just pushed a new commit to the proposed branch (origin/<proposedBranch>
	// is now ahead of ctp.Status.Proposed.Hydrated.Sha, which calculateStatus set before the merge
	// ran). Returning to the workqueue here lets the next reconcile re-derive Status.Proposed via
	// the normal calculateStatus path so PR.Spec.MergeSha matches origin/<proposedBranch> on the
	// very next attempt. Otherwise we would patch the PR with the pre-merge sha on this reconcile,
	// the PullRequest controller would fail to merge with a sha-mismatch error, and we would burn
	// at least one extra SCM merge call per conflict-resolved promotion before recovering.
	if mergedProposedBranch {
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}

	pr, err := r.createOrUpdatePullRequest(ctx, &ctp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set promotion state: %w", err)
	}

	if pr != nil {
		utils.InheritNotReadyConditionFromObjects(&ctp, promoterConditions.PullRequestNotReady, pr)
	}

	pr, err = r.mergePullRequests(ctx, &ctp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to merge pull requests: %w", err)
	}

	if pr != nil {
		utils.InheritNotReadyConditionFromObjects(&ctp, promoterConditions.PullRequestNotReady, pr)
	}

	// calculateHistory is done at a best effort so we do not return any errors here, we just log them instead.
	r.calculateHistory(ctx, &ctp, gitOperations)

	requeueDuration, err := settings.GetRequeueDuration[promoterv1alpha1.ChangeTransferPolicyConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get global promotion configuration: %w", err)
	}

	return ctrl.Result{
		RequeueAfter: requeueDuration,
	}, nil
}

// calculateHistory calculates the history by getting the first parents on the active branch and using the trailers to reconstruct the history.
// This function is best effort and will log errors but continue processing if it encounters issues with individual commits. This is because history is stored in git
// in order to get out of a bad state requires re-writing git history or pushing a bunch of commits greater than the max history limit.
func (r *ChangeTransferPolicyReconciler) calculateHistory(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy, gitOperations *git.EnvironmentOperations) {
	logger := log.FromContext(ctx)

	shaListActive, err := gitOperations.GetRevListFirstParent(ctx, "origin/"+ctp.Spec.ActiveBranch, 5)
	if err != nil {
		logger.V(4).Info("failed to get rev-list commit history for active branch", "branch", ctp.Spec.ActiveBranch, "err", err)
		return
	}
	logger.V(4).Info("Rev-list history for active branch", "shaList", shaListActive)

	history := make([]promoterv1alpha1.History, 0, len(shaListActive))
	for _, sha := range shaListActive {
		historyEntry, shouldInclude, err := r.buildHistoryEntry(ctx, sha, ctp.Spec.ActivePath, gitOperations)
		if err != nil {
			logger.V(4).Info("failed to build history entry", "sha", sha, "err", err)
			continue
		}

		if shouldInclude {
			history = append(history, historyEntry)
		}
	}

	ctp.Status.History = history
}

// buildHistoryEntry creates a single history entry for the given SHA
func (r *ChangeTransferPolicyReconciler) buildHistoryEntry(ctx context.Context, sha, activePath string, gitOperations *git.EnvironmentOperations) (promoterv1alpha1.History, bool, error) {
	activeTrailers, err := gitOperations.GetTrailers(ctx, sha)
	if err != nil {
		return promoterv1alpha1.History{}, false, fmt.Errorf("failed to get trailers for SHA %q: %w", sha, err)
	}

	historyEntry := promoterv1alpha1.History{
		Proposed:    promoterv1alpha1.CommitBranchStateHistoryProposed{},
		Active:      promoterv1alpha1.CommitBranchState{},
		PullRequest: &promoterv1alpha1.PullRequestCommonStatus{},
	}

	r.populateActiveMetadata(ctx, &historyEntry, sha, activePath, gitOperations)
	r.populateProposedMetadata(ctx, &historyEntry, activeTrailers, gitOperations)
	r.populatePullRequestMetadata(ctx, &historyEntry, activeTrailers)
	r.populateCommitStatuses(ctx, &historyEntry, activeTrailers)

	return historyEntry, true, nil
}

// getFirstTrailerValue returns the first value for a given trailer key, or an empty string if not found.
func getFirstTrailerValue(trailers map[string][]string, key string) string {
	if values, ok := trailers[key]; ok && len(values) > 0 {
		return values[0]
	}
	return ""
}

func encodeTrailerDescription(description string) (string, error) {
	encoded, err := json.Marshal(description)
	if err != nil {
		return "", fmt.Errorf("encode commit status description: %w", err)
	}
	return string(encoded), nil
}

func decodeTrailerDescription(ctx context.Context, encoded string) string {
	if encoded == "" {
		return ""
	}
	var description string
	if err := json.Unmarshal([]byte(encoded), &description); err != nil {
		log.FromContext(ctx).Error(err, "failed to decode commit status description trailer", "encoded", encoded)
		return ""
	}
	return description
}

func addCommitStatusTrailers(commitTrailers trailers, prefix string, statuses []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase) error {
	for _, status := range statuses {
		commitTrailers[prefix+status.Key+"-phase"] = status.Phase
		commitTrailers[prefix+status.Key+"-url"] = status.Url
		if status.Description == "" {
			continue
		}
		encoded, err := encodeTrailerDescription(status.Description)
		if err != nil {
			return err
		}
		commitTrailers[prefix+status.Key+"-description"] = encoded
	}
	return nil
}

// populateActiveMetadata populates the active metadata for a history entry
func (r *ChangeTransferPolicyReconciler) populateActiveMetadata(ctx context.Context, h *promoterv1alpha1.History, sha, activePath string, gitOperations *git.EnvironmentOperations) {
	logger := log.FromContext(ctx)
	activeHydrated, err := gitOperations.GetShaMetadataFromGit(ctx, sha)
	if err != nil {
		logger.V(4).Info("failed to get active historic metadata from git", "sha", sha, "error", err)
	}
	h.Active.Hydrated = activeHydrated
	h.Active.Hydrated.Body = removeKnownTrailers(h.Active.Hydrated.Body)

	activeDry, err := gitOperations.GetShaMetadataFromFile(ctx, sha, activePath)
	if err != nil {
		logger.V(4).Info("failed to get active historic metadata from file", "sha", sha, "error", err)
	}
	h.Active.Dry = activeDry
}

// populateProposedMetadata populates the proposed metadata for a history entry
func (r *ChangeTransferPolicyReconciler) populateProposedMetadata(ctx context.Context, h *promoterv1alpha1.History, activeTrailers map[string][]string, gitOperations *git.EnvironmentOperations) {
	logger := log.FromContext(ctx)

	proposedHydratedSha := getFirstTrailerValue(activeTrailers, constants.TrailerShaHydratedProposed)
	if proposedHydratedSha == "" {
		logger.V(4).Info("No " + constants.TrailerShaHydratedProposed + " trailer found")
		return
	}

	meta, err := gitOperations.GetShaMetadataFromGit(ctx, proposedHydratedSha)
	if err != nil {
		logger.V(4).Info("failed to get proposed historic metadata from git", "sha", proposedHydratedSha, "error", err)
	}
	h.Proposed.Hydrated = meta
}

// populatePullRequestMetadata populates the pull request metadata for a history entry
func (r *ChangeTransferPolicyReconciler) populatePullRequestMetadata(ctx context.Context, h *promoterv1alpha1.History, activeTrailers map[string][]string) {
	logger := log.FromContext(ctx)

	if pullRequestID := getFirstTrailerValue(activeTrailers, constants.TrailerPullRequestID); pullRequestID != "" {
		h.PullRequest.ID = pullRequestID
	} else {
		logger.V(4).Info("No " + constants.TrailerPullRequestID + " found in trailers")
	}

	if pullRequestUrl := getFirstTrailerValue(activeTrailers, constants.TrailerPullRequestUrl); pullRequestUrl != "" {
		if !strings.HasPrefix(pullRequestUrl, "http://") && !strings.HasPrefix(pullRequestUrl, "https://") {
			logger.V(4).Info("pull request URL does not start with http:// or https://", "url", pullRequestUrl)
		} else {
			h.PullRequest.Url = pullRequestUrl
		}
	} else {
		logger.V(4).Info("No " + constants.TrailerPullRequestUrl + " found in trailers")
	}

	if timeStr := getFirstTrailerValue(activeTrailers, constants.TrailerPullRequestCreationTime); timeStr != "" {
		if creationTime, err := time.Parse(time.RFC3339, timeStr); err != nil {
			logger.V(4).Info("failed to parse "+constants.TrailerPullRequestCreationTime, "time", timeStr, "err", err)
		} else {
			h.PullRequest.PRCreationTime = metav1.NewTime(creationTime)
		}
	} else {
		logger.V(4).Info("No " + constants.TrailerPullRequestCreationTime + " found in trailers")
	}

	if timeStr := getFirstTrailerValue(activeTrailers, constants.TrailerPullRequestMergeTime); timeStr != "" {
		if mergeTime, err := time.Parse(time.RFC3339, timeStr); err != nil {
			logger.V(4).Info("failed to parse "+constants.TrailerPullRequestMergeTime, "time", timeStr, "err", err)
		} else {
			h.PullRequest.PRMergeTime = metav1.NewTime(mergeTime)
		}
	} else {
		logger.V(4).Info("No " + constants.TrailerPullRequestMergeTime + " found in trailers")
	}
}

// populateCommitStatuses populates the commit statuses for a history entry
func (r *ChangeTransferPolicyReconciler) populateCommitStatuses(ctx context.Context, h *promoterv1alpha1.History, activeTrailers map[string][]string) {
	activeKeys, proposedKeys := getCommitStatusKeysFromTrailers(ctx, activeTrailers)

	h.Active.CommitStatuses = make([]promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase, 0, len(activeKeys))
	for _, key := range activeKeys {
		url := getFirstTrailerValue(activeTrailers, constants.TrailerCommitStatusActivePrefix+key+"-url")
		if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			log.FromContext(ctx).Error(errors.New("invalid URL"), "active commit status URL does not start with http:// or https://", "url", url, "key", key)
			url = ""
		}
		h.Active.CommitStatuses = append(h.Active.CommitStatuses, promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
			Key:         key,
			Phase:       getFirstTrailerValue(activeTrailers, constants.TrailerCommitStatusActivePrefix+key+"-phase"),
			Url:         url,
			Description: decodeTrailerDescription(ctx, getFirstTrailerValue(activeTrailers, constants.TrailerCommitStatusActivePrefix+key+"-description")),
		})
	}

	h.Proposed.CommitStatuses = make([]promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase, 0, len(proposedKeys))
	for _, key := range proposedKeys {
		url := getFirstTrailerValue(activeTrailers, constants.TrailerCommitStatusProposedPrefix+key+"-url")
		if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			log.FromContext(ctx).Error(errors.New("invalid URL"), "proposed commit status URL does not start with http:// or https://", "url", url, "key", key)
			url = ""
		}
		h.Proposed.CommitStatuses = append(h.Proposed.CommitStatuses, promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
			Key:         key,
			Phase:       getFirstTrailerValue(activeTrailers, constants.TrailerCommitStatusProposedPrefix+key+"-phase"),
			Url:         url,
			Description: decodeTrailerDescription(ctx, getFirstTrailerValue(activeTrailers, constants.TrailerCommitStatusProposedPrefix+key+"-description")),
		})
	}
}

// getCommitStatusKeysFromTrailers extracts the commit status keys from the trailers in the given context.
func getCommitStatusKeysFromTrailers(ctx context.Context, trailers map[string][]string) (activeKeys []string, proposedKeys []string) {
	logger := log.FromContext(ctx)

	// This function extracts commit status keys from trailers with the given prefix.
	// It looks for keys that start with the prefix, trims the prefix, splits by "-", and joins all but the last part to form the commit status key.
	// This is under the assumption that the last part is always "-phase", "-url", or "-description" and that it does not go over multiple "-" aka the ending can not be
	// -what-am-i-doing. This would return a bad key because it would contain -what-am-i.
	extractKeys := func(prefix string) []string {
		keys := []string{}
		for key := range trailers {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			key = strings.TrimPrefix(key, prefix)
			if key == "" {
				logger.V(4).Info("Skipping empty trailer key", "key", key)
				continue
			}
			parts := strings.Split(key, "-")
			if len(parts) < 2 {
				logger.V(4).Info("Skipping trailer with unexpected format", "key", key)
				continue
			}
			csKey := strings.Join(parts[:len(parts)-1], "-")
			// Append if it does not exist in keys
			if !slices.Contains(keys, csKey) {
				keys = append(keys, csKey)
			}
		}
		return keys
	}

	activeKeys = extractKeys(constants.TrailerCommitStatusActivePrefix)
	proposedKeys = extractKeys(constants.TrailerCommitStatusProposedPrefix)

	return activeKeys, proposedKeys
}

func removeKnownTrailers(input string) string {
	toRemove := []string{
		constants.TrailerPullRequestID,
		constants.TrailerPullRequestSourceBranch,
		constants.TrailerPullRequestTargetBranch,
		constants.TrailerPullRequestCreationTime,
		constants.TrailerPullRequestUrl,
		constants.TrailerCommitStatusActivePrefix,
		constants.TrailerCommitStatusProposedPrefix,
		constants.TrailerShaHydratedActive,
		constants.TrailerShaHydratedProposed,
		constants.TrailerShaDryActive,
		constants.TrailerShaDryProposed,
	}

	lines := strings.Split(input, "\n")
	filtered := make([]string, 0, len(lines))

	for _, line := range lines {
		shouldKeep := true
		for _, rm := range toRemove {
			if strings.HasPrefix(line, rm) {
				shouldKeep = false
				break
			}
		}
		if shouldKeep {
			filtered = append(filtered, line)
		}
	}

	result := strings.Join(filtered, "\n")
	result = strings.TrimSpace(result)
	return result
}

// SetupWithManager sets up the controller with the Manager.
func (r *ChangeTransferPolicyReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	// This index gets used by the CommitStatus controller and the webhook server to find the ChangeTransferPolicy to trigger reconcile
	if err := mgr.GetFieldIndexer().IndexField(ctx, &promoterv1alpha1.ChangeTransferPolicy{}, ".status.proposed.hydrated.sha", func(rawObj client.Object) []string {
		//nolint:forcetypeassert // type is guaranteed by the IndexField API
		ctp := rawObj.(*promoterv1alpha1.ChangeTransferPolicy)
		return []string{ctp.Status.Proposed.Hydrated.Sha}
	}); err != nil {
		return fmt.Errorf("failed to set field index for .status.proposed.hydrated.sha: %w", err)
	}

	// This gets used by the CommitStatus controller to find the ChangeTransferPolicy to trigger reconcile
	if err := mgr.GetFieldIndexer().IndexField(ctx, &promoterv1alpha1.ChangeTransferPolicy{}, ".status.active.hydrated.sha", func(rawObj client.Object) []string {
		//nolint:forcetypeassert // type is guaranteed by the IndexField API
		ctp := rawObj.(*promoterv1alpha1.ChangeTransferPolicy)
		return []string{ctp.Status.Active.Hydrated.Sha}
	}); err != nil {
		return fmt.Errorf("failed to set field index for .status.active.hydrated.sha: %w", err)
	}

	// Use Direct methods to read configuration from the API server without cache during setup.
	// The cache is not started during SetupWithManager, so we must use the non-cached API reader.
	rateLimiter, err := settings.GetRateLimiterDirect[promoterv1alpha1.ChangeTransferPolicyConfiguration, ctrl.Request](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get ChangeTransferPolicy rate limiter: %w", err)
	}

	maxConcurrentReconciles, err := settings.GetMaxConcurrentReconcilesDirect[promoterv1alpha1.ChangeTransferPolicyConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		return fmt.Errorf("failed to get ChangeTransferPolicy max concurrent reconciles: %w", err)
	}

	// Create a channel for external enqueue requests. This allows other controllers
	// to trigger CTP reconciliation without modifying the CTP object.
	// The channel uses GenericEvent with a minimal CTP object containing just the namespace/name.
	// We use a buffer of 1024 to match the default internal buffer size of source.Channel.
	// Sends will block if the buffer is full, providing natural backpressure to callers.
	externalEnqueueChan := make(chan event.GenericEvent, 1024)

	// Store the enqueue function so it can be retrieved by other controllers.
	// This is a blocking send - callers will wait if the channel buffer is full.
	r.enqueueFunc = func(namespace, name string) {
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.SetNamespace(namespace)
		ctp.SetName(name)

		select {
		case externalEnqueueChan <- event.GenericEvent{Object: ctp}:
			// Sent successfully
		default:
			// Channel is full, log a warning and block until space is available
			log.FromContext(ctx).Info("CTP enqueue channel is full, blocking until space is available",
				"namespace", namespace, "name", name)
			externalEnqueueChan <- event.GenericEvent{Object: ctp}
		}
	}

	err = ctrl.NewControllerManagedBy(mgr).
		For(&promoterv1alpha1.ChangeTransferPolicy{},
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				// Webhooks trigger reconciliations by bumping an annotation.
				// TODO: use a custom predicate to only trigger on the specific annotation change.
				predicate.AnnotationChangedPredicate{},
			))).
		// This controller intentionally doesn't have a .Owns for CommitStatuses. Every reconcile of a CommitStatus
		// checks whether it needs to update a related ChangeTransferPolicy by setting an annotation. Avoiding .Owns
		// here avoids duplicate reconciliations.
		Owns(&promoterv1alpha1.PullRequest{}, builder.WithPredicates(pullRequestUpdateEnqueuesChangeTransferPolicyPredicate())).
		// Watch for external enqueue requests from other controllers (e.g., PromotionStrategy).
		// The handler.EnqueueRequestForObject extracts the namespace/name from the GenericEvent.
		WatchesRawSource(source.Channel(externalEnqueueChan, &handler.EnqueueRequestForObject{})).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles, RateLimiter: rateLimiter}).
		Complete(r)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}
	return nil
}

// maxVerificationWalk bounds how many commits on the active branch are examined when rebuilding an
// environment's verification record from git. Skipped commits — direct pushes, squash merges that
// dropped the promoter's message — consume the budget without contributing evidence, so a repository
// with heavy manual activity gets a shorter effective record than the bound suggests. It is generous
// because the walk normally examines nothing at all: the record is only extended when the active
// branch moves, and then only over the commits it moved by.
const maxVerificationWalk = 200

// refreshVerification brings the environment's record of the changes it has promoted past up to date
// by reading the evidence each promotion wrote into its merge commit on the active branch.
//
// This is only half of what an environment has vouched for. It covers every change the environment
// has moved past; the change it is running right now has no merge commit yet, and is composed in
// from live health where the record is read (see environmentVerificationStatus in the PromotionStrategy
// controller). That live half is what stops a later environment from only ever seeing changes that
// are already one promotion stale.
//
// Must run after calculateStatus, which populates the active branch state this reads.
func (r *ChangeTransferPolicyReconciler) refreshVerification(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy, gitOperations *git.EnvironmentOperations) error {
	logger := log.FromContext(ctx)

	activeHydratedSha := ctp.Status.Active.Hydrated.Sha
	if activeHydratedSha == "" {
		// Nothing promoted yet, so there is no history to walk and nothing running to latch.
		return nil
	}

	if ctp.Status.Verification == nil {
		ctp.Status.Verification = &promoterv1alpha1.VerificationState{}
	}
	verification := ctp.Status.Verification

	if verification.ObservedActiveSha != activeHydratedSha {
		entries, reachedObserved, err := r.walkVerifiedDryShas(ctx, ctp, gitOperations, activeHydratedSha, verification.ObservedActiveSha)
		if err != nil {
			return err
		}

		if reachedObserved {
			verification.DryShas = append(entries, verification.DryShas...)
		} else {
			// The walk could not reach what was recorded last time — a first reconcile, a lost status,
			// or rewritten history. It has therefore just read the whole record git can offer, so
			// replace rather than prepend; keeping the old entries would preserve ones git no longer
			// supports.
			logger.V(4).Info("Rebuilt verification record from scratch",
				"activeBranch", ctp.Spec.ActiveBranch,
				"previousObservedActiveSha", verification.ObservedActiveSha,
				"entries", len(entries))
			verification.DryShas = entries
		}
		verification.ObservedActiveSha = activeHydratedSha
	}

	verification.DryShas = dedupeVerifiedDryShas(verification.DryShas)
	if len(verification.DryShas) > promoterv1alpha1.MaxLastHealthyDryShas {
		verification.DryShas = verification.DryShas[:promoterv1alpha1.MaxLastHealthyDryShas]
	}

	return nil
}

// walkVerifiedDryShas reads verification evidence off the active branch, newest commit first,
// stopping when it reaches stopAtSha or has collected a full record. It reports whether it reached
// stopAtSha: a walk that did not has read everything it is going to, so its result replaces the
// existing record rather than extending it.
//
// Everything read here lives in commit objects — the message for the trailers, and the commit time.
// The clone is blobless (--filter=blob:none), so reading file content instead would fetch a blob from
// the remote per commit; staying off the trees is what keeps this walk cheap.
func (r *ChangeTransferPolicyReconciler) walkVerifiedDryShas(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy, gitOperations *git.EnvironmentOperations, fromSha, stopAtSha string) ([]promoterv1alpha1.HealthyDryShas, bool, error) {
	logger := log.FromContext(ctx)

	shas, err := gitOperations.GetRevListFirstParent(ctx, fromSha, maxVerificationWalk)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list commits on branch %q: %w", ctp.Spec.ActiveBranch, err)
	}

	entries := make([]promoterv1alpha1.HealthyDryShas, 0, len(shas))
	for _, sha := range shas {
		if stopAtSha != "" && sha == stopAtSha {
			return entries, true, nil
		}

		if len(entries) >= promoterv1alpha1.MaxLastHealthyDryShas {
			// A full record already; anything older would be truncated away regardless. Reported as not
			// having reached stopAtSha so the caller replaces rather than extends, which is right: these
			// are the newest entries there are.
			break
		}

		trailers, err := gitOperations.GetTrailers(ctx, sha)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get trailers for commit %q: %w", sha, err)
		}

		drySha, ok := verifiedDryShaFromTrailers(ctx, trailers)
		if !ok {
			continue
		}

		commitTime, err := gitOperations.GetShaTime(ctx, sha)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get commit time for commit %q: %w", sha, err)
		}

		entries = append(entries, promoterv1alpha1.HealthyDryShas{Sha: drySha, Time: commitTime})
	}

	logger.V(4).Info("Walked active branch for verification evidence",
		"activeBranch", ctp.Spec.ActiveBranch, "walked", len(shas), "found", len(entries))

	return entries, false, nil
}

// verifiedDryShaFromTrailers reads a commit's evidence that the environment was healthy on the change
// it was running before that promotion, returning false when the commit is evidence of nothing.
//
// The phases recorded here are read at promotion time: the controller refreshes the pull request body
// and merges it in the same reconcile, off the same status, so they describe the environment's health
// at the moment it stopped running that change. That is the claim this record makes — healthy when we
// moved past it — not merely healthy at some point along the way.
//
// A commit carrying no promoter trailers at all — a direct push to the active branch, a squash merge
// that dropped the message, a hand-edited message — is unknown rather than negative. The caller skips
// it and keeps walking: stopping there would let a single manual push blind the whole record behind
// it, while treating it as a verification would invent one.
func verifiedDryShaFromTrailers(ctx context.Context, trailers map[string][]string) (string, bool) {
	drySha := getFirstTrailerValue(trailers, constants.TrailerShaDryActive)
	if drySha == "" {
		return "", false
	}

	activeKeys, _ := getCommitStatusKeysFromTrailers(ctx, trailers)
	if len(activeKeys) == 0 {
		// Every configured active commit status is written as a trailer, as pending when nothing has
		// reported yet, so no trailers means no gates were configured. An environment that gates on
		// nothing verifies whatever it runs.
		return drySha, true
	}

	for _, key := range activeKeys {
		if getFirstTrailerValue(trailers, constants.TrailerCommitStatusActivePrefix+key+"-phase") != string(promoterv1alpha1.CommitPhaseSuccess) {
			return "", false
		}
	}
	return drySha, true
}

// dedupeVerifiedDryShas keeps the first entry for each dry SHA, which is the newest. A change can be
// recorded twice when the live latch names one the walk also found, or when a change is promoted,
// reverted, and promoted again.
func dedupeVerifiedDryShas(entries []promoterv1alpha1.HealthyDryShas) []promoterv1alpha1.HealthyDryShas {
	seen := make(map[string]bool, len(entries))
	deduped := make([]promoterv1alpha1.HealthyDryShas, 0, len(entries))
	for _, entry := range entries {
		if seen[entry.Sha] {
			continue
		}
		seen[entry.Sha] = true
		deduped = append(deduped, entry)
	}
	return deduped
}

// maxPromotionCandidates bounds how far back along the proposed branch the controller will look for
// a promotable change. It is a safety valve, not a tuning knob: selection normally stops at the
// change the environment is already running, and only walks the full window when the environment has
// fallen very far behind. It is deliberately larger than MaxLastHealthyDryShas so the walk can always
// reach past every change an upstream environment could still be vouching for.
const maxPromotionCandidates = 200

// selectPromotionCandidate points the promotion branch at the change this policy should promote next.
//
// The proposed branch belongs to the hydrator: it always carries the newest dry commit, which is also
// the one upstream environments have had the least time to verify. Promoting it directly is what the
// Latest policy does, and under churn it can starve an environment indefinitely. The other policies
// instead treat the proposed branch as a stream of candidates — the hydrator writes one commit per
// dry commit — and pick one out of its history, which is what this function does. The chosen commit
// is force-pushed to the promotion branch, and the rest of the reconcile proceeds against that branch
// exactly as it would against the proposed branch.
//
// Called before calculateStatus so that the status, the pull request, and the conflict merge all
// describe the selected candidate. Returns true when the promotion branch was moved, in which case
// the caller must requeue rather than continue: everything after this point derives from the branch,
// and re-deriving it on a fresh reconcile is simpler than reasoning about which reads in this one
// would still see the pre-push tip.
func (r *ChangeTransferPolicyReconciler) selectPromotionCandidate(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy, gitOperations *git.EnvironmentOperations) (bool, error) {
	var lastKnownCandidateSha string
	if ctp.Status.Candidate != nil {
		lastKnownCandidateSha = ctp.Status.Candidate.HydratedSha
	}

	candidateShas, err := gitOperations.GetBranchShas(ctx, ctp.Spec.ProposedBranch, ctp.Spec.ActivePath, lastKnownCandidateSha)
	if err != nil {
		if strings.Contains(err.Error(), "couldn't find remote ref") {
			return false, fmt.Errorf("failed to get SHAs for proposed branch %q: %w (this branch may not exist yet - check if your hydrator is running and has processed this branch)", ctp.Spec.ProposedBranch, err)
		}
		return false, fmt.Errorf("failed to get SHAs for proposed branch %q: %w", ctp.Spec.ProposedBranch, err)
	}

	// The commit time is what the PromotionStrategy controller ranks environments by when deciding
	// which one has the newest view of the hydrator's output, so it has to be populated here.
	candidateCommitTime, err := gitOperations.GetShaTime(ctx, candidateShas.Hydrated)
	if err != nil {
		return false, fmt.Errorf("failed to get commit time for candidate SHA %q: %w", candidateShas.Hydrated, err)
	}

	// The hydrator's note lives on this branch, not on the promotion branch, and it is the only
	// signal that a hydration which changed no manifests has happened. The PromotionStrategy
	// controller reads it to spot environments whose view of the notes has gone stale.
	candidateNote, err := gitOperations.FindMatchingHydratorNote(ctx, candidateShas.Hydrated, candidateShas.Dry, git.MaxHydratorNoteFirstParentWalk)
	if err != nil {
		return false, fmt.Errorf("failed to get hydrator note for candidate SHA %q: %w", candidateShas.Hydrated, err)
	}

	// Record the newest change that exists regardless of whether it can be promoted, so the gap
	// between it and Proposed shows how far behind the environment is running.
	ctp.Status.Candidate = &promoterv1alpha1.PromotionCandidateState{
		DrySha:      candidateShas.Dry,
		HydratedSha: candidateShas.Hydrated,
		CommitTime:  candidateCommitTime,
	}
	if candidateNote != nil {
		ctp.Status.Candidate.NoteDrySha = candidateNote.DrySha
	}

	activeShas, err := gitOperations.GetBranchShas(ctx, ctp.Spec.ActiveBranch, ctp.Spec.ActivePath, ctp.Status.Active.Hydrated.Sha)
	if err != nil {
		return false, fmt.Errorf("failed to get SHAs for active branch %q: %w", ctp.Spec.ActiveBranch, err)
	}

	selected, err := r.findPromotionCandidate(ctx, ctp, gitOperations, candidateShas.Hydrated, activeShas.Dry)
	if err != nil {
		return false, err
	}

	return r.updatePromotionBranch(ctx, ctp, gitOperations, selected, activeShas)
}

// promotionCandidate is a commit on the proposed branch and the dry commit it hydrates.
type promotionCandidate struct {
	hydratedSha string
	drySha      string
}

// findPromotionCandidate walks the proposed branch from its tip back toward the change the
// environment is already running and returns the candidate the policy selects, or a zero value when
// nothing is promotable right now.
//
// The walk stops at activeDrySha because everything older is already promoted. It reads each commit's
// dry SHA from hydrator.metadata rather than from the git note: a note-only hydration means the
// manifests did not change, so there is no distinct commit to promote, and the ledger upstream
// environments write records the hydrator.metadata SHA too. Using the same source on both sides is
// what makes a candidate comparable with what upstream verified.
func (r *ChangeTransferPolicyReconciler) findPromotionCandidate(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy, gitOperations *git.EnvironmentOperations, candidateTipSha, activeDrySha string) (promotionCandidate, error) {
	logger := log.FromContext(ctx)

	shas, err := gitOperations.GetRevListFirstParent(ctx, candidateTipSha, maxPromotionCandidates)
	if err != nil {
		return promotionCandidate{}, fmt.Errorf("failed to list candidates on branch %q: %w", ctp.Spec.ProposedBranch, err)
	}

	policy := ctp.Spec.GetPromotionPolicy()

	// Newest first, so the first eligible commit is the newest one. Sequential wants the oldest
	// instead, so it keeps walking and takes the last one it found.
	var eligible []promotionCandidate
	for _, sha := range shas {
		metadata, err := gitOperations.GetShaMetadataFromFile(ctx, sha, ctp.Spec.ActivePath)
		if err != nil {
			return promotionCandidate{}, fmt.Errorf("failed to read hydrator metadata for commit %q: %w", sha, err)
		}

		drySha := metadata.Sha
		if drySha == "" {
			// Not a commit the hydrator identified, so there is no dry SHA to match against what
			// upstream verified. Conflict-resolving merge commits pushed by an earlier reconcile land
			// here before the hydrated commit they sit on top of.
			continue
		}

		if drySha == activeDrySha {
			// Reached what the environment already runs; everything from here back is promoted.
			break
		}

		if !isPromotionCandidateAllowed(ctp, drySha) {
			continue
		}

		eligible = append(eligible, promotionCandidate{hydratedSha: sha, drySha: drySha})
		if policy != promoterv1alpha1.PromotionPolicySequential {
			break
		}
	}

	if len(eligible) == 0 {
		logger.V(4).Info("No promotable candidate found",
			"policy", policy,
			"proposedBranch", ctp.Spec.ProposedBranch,
			"candidateTipSha", candidateTipSha,
			"activeDrySha", activeDrySha,
			"walked", len(shas))
		return promotionCandidate{}, nil
	}

	selected := eligible[0]
	if policy == promoterv1alpha1.PromotionPolicySequential {
		selected = eligible[len(eligible)-1]
	}

	// The walk stops at the active dry SHA, but that SHA is only found when the environment's current
	// change is still within the window and still on the branch's first-parent chain. Confirm the
	// selection really is ahead of the active branch so a rewritten or aged-out history cannot make
	// the promotion branch move backwards onto something already merged.
	alreadyPromoted, err := gitOperations.IsAncestor(ctx, selected.hydratedSha, "origin/"+ctp.Spec.ActiveBranch)
	if err != nil {
		return promotionCandidate{}, fmt.Errorf("failed to check whether candidate %q is already on branch %q: %w", selected.hydratedSha, ctp.Spec.ActiveBranch, err)
	}
	if alreadyPromoted {
		logger.V(4).Info("Skipping candidate already contained in the active branch",
			"candidateSha", selected.hydratedSha, "candidateDrySha", selected.drySha)
		return promotionCandidate{}, nil
	}

	logger.Info("Selected promotion candidate",
		"policy", policy,
		"candidateSha", selected.hydratedSha,
		"candidateDrySha", selected.drySha,
		"newestDrySha", ctp.Status.Candidate.DrySha,
		"activeDrySha", activeDrySha)

	return selected, nil
}

// isPromotionCandidateAllowed reports whether every preceding environment has verified the change.
// A nil Candidates means there is nothing upstream to wait for, which is the case for the first
// environment in a sequence.
func isPromotionCandidateAllowed(ctp *promoterv1alpha1.ChangeTransferPolicy, drySha string) bool {
	if ctp.Spec.Candidates == nil {
		return true
	}
	for _, allowed := range ctp.Spec.Candidates.DryShas {
		if allowed == drySha {
			return true
		}
	}
	return false
}

// updatePromotionBranch moves the promotion branch onto the selected candidate when it is not
// already carrying it.
//
// The comparison is by dry SHA rather than by commit SHA because the branch tip is not always the
// candidate commit itself: when the promotion conflicts with the active branch, gitMergeStrategyOurs
// pushes a merge commit on top that keeps the candidate's tree, and therefore its hydrator.metadata.
// Comparing commit SHAs would see a difference there, force the branch back onto the candidate, and
// leave the next reconcile to redo the merge forever.
func (r *ChangeTransferPolicyReconciler) updatePromotionBranch(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy, gitOperations *git.EnvironmentOperations, selected promotionCandidate, activeShas git.BranchShas) (bool, error) {
	logger := log.FromContext(ctx)

	promotionBranch := ctp.Spec.PromotionBranch

	// An empty head means the branch does not exist yet, which is the normal state before this
	// environment's first promotion.
	currentHead, err := gitOperations.LsRemoteBranchHead(ctx, promotionBranch)
	if err != nil {
		return false, fmt.Errorf("failed to look up promotion branch %q: %w", promotionBranch, err)
	}

	if selected.hydratedSha == "" {
		if currentHead != "" {
			// Nothing new to promote. Leaving the branch where it is keeps it equivalent to the active
			// branch, so the rest of the reconcile sees no pending promotion.
			return false, nil
		}

		// First reconcile for this environment. The branch has to exist before the rest of the
		// reconcile can read it, so start it at the active branch: same content, hence nothing to
		// promote until a candidate is selected.
		if activeShas.Hydrated == "" {
			return false, fmt.Errorf("cannot initialize promotion branch %q: active branch %q has no commit", promotionBranch, ctp.Spec.ActiveBranch)
		}
		logger.Info("Initializing promotion branch at the active branch", "promotionBranch", promotionBranch, "activeSha", activeShas.Hydrated)
		if err := gitOperations.PushShaToBranch(ctx, activeShas.Hydrated, promotionBranch); err != nil {
			return false, fmt.Errorf("failed to initialize promotion branch %q: %w", promotionBranch, err)
		}
		return true, nil
	}

	if currentHead != "" {
		currentShas, err := gitOperations.GetBranchShas(ctx, promotionBranch, ctp.Spec.ActivePath, ctp.Status.Proposed.Hydrated.Sha)
		if err != nil {
			return false, fmt.Errorf("failed to get SHAs for promotion branch %q: %w", promotionBranch, err)
		}
		if currentShas.Dry == selected.drySha {
			return false, nil
		}
	}

	logger.Info("Advancing promotion branch to selected candidate",
		"promotionBranch", promotionBranch,
		"fromSha", currentHead,
		"toSha", selected.hydratedSha,
		"drySha", selected.drySha)

	if err := gitOperations.PushShaToBranch(ctx, selected.hydratedSha, promotionBranch); err != nil {
		return false, fmt.Errorf("failed to advance promotion branch %q: %w", promotionBranch, err)
	}

	r.Recorder.Eventf(ctp, nil, "Normal", constants.PromotionCandidateSelectedReason, "SelectingCandidate",
		constants.PromotionCandidateSelectedMessage, selected.drySha, ctp.Spec.ActiveBranch, ctp.Status.Candidate.DrySha)

	return true, nil
}

func (r *ChangeTransferPolicyReconciler) calculateStatus(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy, gitOperations *git.EnvironmentOperations) error {
	logger := log.FromContext(ctx)

	// TODO: consider parallelizing parts of this function that are network-bound work. This requires the git library
	// to be made concurrency-safe for a single identity first; today its EnvironmentOperations methods share one
	// on-disk clone and must be called sequentially (see the internal/git package documentation).

	// GetBranchShas skips the network fetch for a branch when a live ls-remote confirms its remote
	// SHA still matches what we observed last reconcile - the commit is then guaranteed already
	// present in this identity's clone. In the common steady state (nothing changed since the last
	// reconcile) this avoids paying for a full fetch on either branch. A failed probe (for example a
	// branch not existing yet) just falls back to a real fetch, exactly as before.
	proposedShas, err := gitOperations.GetBranchShas(ctx, ctp.Spec.GetPromotionSourceBranch(), ctp.Spec.ActivePath, ctp.Status.Proposed.Hydrated.Sha)
	if err != nil {
		// If the proposed branch doesn't exist, it's likely because the hydrator hasn't run yet
		if strings.Contains(err.Error(), "couldn't find remote ref") {
			return fmt.Errorf("failed to get SHAs for proposed branch %q: %w (this branch may not exist yet - check if your hydrator is running and has processed this branch)", ctp.Spec.GetPromotionSourceBranch(), err)
		}
		return fmt.Errorf("failed to get SHAs for proposed branch %q: %w", ctp.Spec.GetPromotionSourceBranch(), err)
	}

	activeShas, err := gitOperations.GetBranchShas(ctx, ctp.Spec.ActiveBranch, ctp.Spec.ActivePath, ctp.Status.Active.Hydrated.Sha)
	if err != nil {
		return fmt.Errorf("failed to get SHAs for active branch %q: %w", ctp.Spec.ActiveBranch, err)
	}

	logger.Info("Branch SHAs", "branchShas", map[string]git.BranchShas{
		ctp.Spec.ActiveBranch:               activeShas,
		ctp.Spec.GetPromotionSourceBranch(): proposedShas,
	})

	err = r.setCommitMetadata(ctx, ctp, gitOperations, activeShas.Hydrated, proposedShas.Hydrated)
	if err != nil {
		return fmt.Errorf("failed to set commit metadata: %w", err)
	}

	if err := validateProposedDryMetadata(ctp); err != nil {
		metadataPath := hydratorMetadataPath(ctp.Spec.ActivePath)
		r.Recorder.Eventf(ctp, nil, "Warning", constants.MissingProposedHydratorMetadataReason, "EvaluatingPromotion",
			constants.MissingProposedHydratorMetadataMessage, ctp.Spec.GetPromotionSourceBranch(), ctp.Status.Proposed.Hydrated.Sha, metadataPath)
		return err
	}

	err = r.setCommitStatusState(ctx, &ctp.Status.Active, ctp.Spec.ActiveCommitStatuses)
	if err != nil {
		if _, ok := errors.AsType[*TooManyMatchingShaError](err); ok {
			r.Recorder.Eventf(ctp, nil, "Warning", constants.TooManyMatchingShaReason, "EvaluatingPromotion", constants.TooManyMatchingShaActiveMessage)
		}
		return fmt.Errorf("failed to set active commit status state: %w", err)
	}

	err = r.setCommitStatusState(ctx, &ctp.Status.Proposed, ctp.Spec.ProposedCommitStatuses)
	if err != nil {
		if _, ok := errors.AsType[*TooManyMatchingShaError](err); ok {
			r.Recorder.Eventf(ctp, nil, "Warning", constants.TooManyMatchingShaReason, "EvaluatingPromotion", constants.TooManyMatchingShaProposedMessage)
		}
		return fmt.Errorf("failed to set proposed commit status state: %w", err)
	}

	err = r.setPullRequestState(ctx, ctp)
	if err != nil {
		return fmt.Errorf("failed to set pull request status state: %w", err)
	}

	return nil
}

func hydratorMetadataPath(activePath string) string {
	if activePath == "" {
		return "hydrator.metadata"
	}
	return path.Join(activePath, "hydrator.metadata")
}

// validateProposedDryMetadata returns an error when the proposed branch has clearly moved ahead of
// active but promoter could not read a dry SHA from hydrator.metadata at activePath. An empty active
// dry SHA is normal before the first promotion; an empty proposed dry SHA is not once the proposed
// branch tip differs from active. A git note dry SHA that differs from proposed dry SHA is fine.
//
// This function assumes ctp.Status.Proposed.Hydrated.Sha is non-empty. That should already be
// confirmed by this point in the reconcile.
func validateProposedDryMetadata(ctp *promoterv1alpha1.ChangeTransferPolicy) error {
	if ctp.Status.Proposed.Dry.Sha != "" {
		return nil
	}

	proposedHydratedSha := ctp.Status.Proposed.Hydrated.Sha

	if proposedHydratedSha == ctp.Status.Active.Hydrated.Sha {
		return nil
	}

	metadataPath := hydratorMetadataPath(ctp.Spec.ActivePath)
	msg := fmt.Sprintf("proposed branch %q has hydrated commit %s but no dry SHA from %q on that commit",
		ctp.Spec.GetPromotionSourceBranch(), proposedHydratedSha, metadataPath)
	if ctp.Spec.ActivePath != "" {
		msg += fmt.Sprintf("; ensure the hydrator writes hydrator.metadata under activePath %q", ctp.Spec.ActivePath)
	}
	if noteDrySha := getNoteDrySha(ctp.Status.Proposed.Note); noteDrySha != "" {
		msg += fmt.Sprintf(" (git note reports dry SHA %s, confirming hydration ran)", noteDrySha)
	}

	return errors.New(msg)
}

// NewTooManyMatchingShaError creates a new TooManyMatchingShaError. This error indicates that there are too many
// commit status resources matching the given SHA and key.
func NewTooManyMatchingShaError(commitStatusKey string, commitStatuses []promoterv1alpha1.CommitStatus) error {
	return &TooManyMatchingShaError{
		commitStatusKey: commitStatusKey,
		commitStatuses:  commitStatuses,
	}
}

// emitPromotionLifecycleEvents compares the status persisted by the previous reconcile (prev,
// snapshotted before calculateStatus overwrote it) with the freshly calculated status and emits
// promotion lifecycle events. All events are transition-only: a state that persists across
// reconciles (e.g. still blocked on the same pending commit status) emits nothing.
func (r *ChangeTransferPolicyReconciler) emitPromotionLifecycleEvents(ctp *promoterv1alpha1.ChangeTransferPolicy, prev *promoterv1alpha1.ChangeTransferPolicyStatus) {
	cur := &ctp.Status
	pendingNow := cur.Proposed.Dry.Sha != "" && cur.Proposed.Dry.Sha != cur.Active.Dry.Sha
	pendingBefore := prev.Proposed.Dry.Sha != "" && prev.Proposed.Dry.Sha != prev.Active.Dry.Sha
	// A new attempt is a proposed dry sha we haven't seen before, either because nothing was
	// pending or because a newer change superseded the in-flight one.
	newAttempt := pendingNow && (!pendingBefore || prev.Proposed.Dry.Sha != cur.Proposed.Dry.Sha)

	if newAttempt {
		r.Recorder.Eventf(ctp, nil, "Normal", constants.PromotionStartedReason, "Promoting",
			constants.PromotionStartedMessage, cur.Proposed.Dry.Sha, ctp.Spec.ActiveBranch, cur.Active.Dry.Sha)
	}

	if pendingNow {
		prevPhases := make(map[string]string, len(prev.Proposed.CommitStatuses))
		for _, s := range prev.Proposed.CommitStatuses {
			prevPhases[s.Key] = s.Phase
		}
		for _, s := range cur.Proposed.CommitStatuses {
			if s.Phase == string(promoterv1alpha1.CommitPhaseSuccess) {
				continue
			}
			if !newAttempt && prevPhases[s.Key] == s.Phase {
				// Same gate, same phase as the last reconcile: already announced.
				continue
			}
			eventType := "Normal"
			if s.Phase == string(promoterv1alpha1.CommitPhaseFailure) {
				eventType = "Warning"
			}
			r.Recorder.Eventf(ctp, nil, eventType, constants.PromotionBlockedReason, "Promoting",
				constants.PromotionBlockedMessage, cur.Proposed.Dry.Sha, ctp.Spec.ActiveBranch, s.Key, s.Phase)
		}
	}

	// The first-ever status population is not a promotion, hence the prev guard.
	if prev.Active.Dry.Sha != "" && cur.Active.Dry.Sha != "" && prev.Active.Dry.Sha != cur.Active.Dry.Sha {
		r.Recorder.Eventf(ctp, nil, "Normal", constants.PromotionCompletedReason, "Promoting",
			constants.PromotionCompletedMessage, ctp.Spec.ActiveBranch, cur.Active.Dry.Sha, prev.Active.Dry.Sha)
	}
}

// TooManyMatchingShaError is an error type that indicates that there are too many matching SHAs for a commit status.
type TooManyMatchingShaError struct {
	commitStatusKey string
	commitStatuses  []promoterv1alpha1.CommitStatus
}

// Error implements the error interface for TooManyMatchingShaError.
func (e *TooManyMatchingShaError) Error() string {
	// Construct a message that includes the namespace/name of each commit status.
	// If there are more than two, finish the message with "and X more..."
	var msg strings.Builder
	msg.WriteString("there are too many matching SHAs for the '" + e.commitStatusKey + "' commit status: ")
	for i, cs := range e.commitStatuses {
		if i > 0 {
			msg.WriteString(", ")
		}
		if i >= 2 {
			fmt.Fprintf(&msg, "and %d more...", len(e.commitStatuses)-i)
			break
		}
		fmt.Fprintf(&msg, "%s/%s", cs.Namespace, cs.Name)
	}
	return msg.String()
}

func (r *ChangeTransferPolicyReconciler) setCommitMetadata(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy, gitOperations *git.EnvironmentOperations, activeHydratedSha, proposedHydratedSha string) error {
	logger := log.FromContext(ctx)

	activeCommitMetadata, err := gitOperations.GetShaMetadataFromFile(ctx, activeHydratedSha, ctp.Spec.ActivePath)
	if err != nil {
		return fmt.Errorf("failed to get commit metadata for hydrated SHA %q: %w", activeHydratedSha, err)
	}
	ctp.Status.Active.Dry = activeCommitMetadata

	proposedCommitMetadata, err := gitOperations.GetShaMetadataFromFile(ctx, proposedHydratedSha, ctp.Spec.ActivePath)
	if err != nil {
		return fmt.Errorf("failed to get commit metadata for hydrated SHA %q: %w", activeHydratedSha, err)
	}
	ctp.Status.Proposed.Dry = proposedCommitMetadata

	activeCommitMetadata, err = gitOperations.GetShaMetadataFromGit(ctx, activeHydratedSha)
	if err != nil {
		return fmt.Errorf("failed to get commit active metadata for hydrated SHA %q: %w", activeHydratedSha, err)
	}
	ctp.Status.Active.Hydrated = activeCommitMetadata
	ctp.Status.Active.Hydrated.Body = removeKnownTrailers(ctp.Status.Active.Hydrated.Body)
	proposedCommitMetadata, err = gitOperations.GetShaMetadataFromGit(ctx, proposedHydratedSha)
	if err != nil {
		return fmt.Errorf("failed to get commit proposed metadata for hydrated SHA %q: %w", proposedHydratedSha, err)
	}
	ctp.Status.Proposed.Hydrated = proposedCommitMetadata

	// Read the git note for the proposed hydrated commit to get the Note.DrySha.
	// This is used by downstream environments to verify that hydration is complete
	// for a given dry commit before allowing promotion. When conflict-resolution
	// ours-merge advances the branch tip past the hydrated commit (no note on the
	// merge commit), walk first-parent ancestors for a note whose drySha matches
	// hydrator.metadata on the tip.
	proposedNote, err := gitOperations.FindMatchingHydratorNote(ctx, proposedHydratedSha, ctp.Status.Proposed.Dry.Sha, git.MaxHydratorNoteFirstParentWalk)
	if err != nil {
		return fmt.Errorf("failed to get hydrator note for proposed hydrated SHA %q: %w", proposedHydratedSha, err)
	}
	var drySha string
	if proposedNote != nil {
		drySha = proposedNote.DrySha
		ctp.Status.Proposed.Note = &promoterv1alpha1.HydratorMetadata{
			DrySha: drySha,
		}
	} else {
		// No git note for this proposed hydrated commit. Clear any stale Note
		// from a previous reconcile (which referenced a different hydrated
		// commit), so downstream gates like getEffectiveHydratedDrySha don't
		// trust an old drySha as the current env's "effective" hydrated dry.
		// Leaving the old value in place causes
		// PromotionStrategy.updatePreviousEnvironmentCommitStatus to compute
		// targetDrySha from a stale note and incorrectly mark the previous-env
		// CommitStatus success against the wrong dry SHA.
		ctp.Status.Proposed.Note = nil
	}
	logger.V(4).Info("Set proposed Note.DrySha from git note",
		"proposedHydratedSha", proposedHydratedSha,
		"noteDrySha", drySha)

	return nil
}

// setCommitStatusState sets the hydrated and dry SHAs and commit times for the target commit branch state and sets the
// commit statuses.
func (r *ChangeTransferPolicyReconciler) setCommitStatusState(ctx context.Context, targetCommitBranchState *promoterv1alpha1.CommitBranchState, commitStatuses []promoterv1alpha1.CommitStatusSelector) error {
	logger := log.FromContext(ctx)

	commitStatusesState := []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{}
	var tooManyMatchingShaError error
	for _, status := range commitStatuses {
		var csList promoterv1alpha1.CommitStatusList
		// Find all the replicasets that match the commit status configured name and the sha of the hydrated commit
		err := r.List(ctx, &csList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(map[string]string{
				promoterv1alpha1.CommitStatusLabel: utils.KubeSafeLabel(status.Key),
			}),
			FieldSelector: fields.SelectorFromSet(map[string]string{
				".spec.sha": targetCommitBranchState.Hydrated.Sha,
			}),
		})
		if err != nil {
			return fmt.Errorf("failed to list CommitStatuses for key %q and SHA %q: %w", status.Key, targetCommitBranchState.Hydrated.Sha, err)
		}

		found := false
		phase := promoterv1alpha1.CommitPhasePending
		if len(csList.Items) == 1 {
			cs := csList.Items[0]

			// Read phase from spec instead of status. Since we query by spec.sha, reading from
			// spec.phase ensures we get the phase that corresponds to the SHA we're checking.
			// This prevents reading stale status.phase values when spec was just updated.
			csPhase := cs.Spec.Phase
			if csPhase == "" {
				csPhase = promoterv1alpha1.CommitPhasePending
			}
			commitStatusesState = append(commitStatusesState, promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				Key:         status.Key,
				Phase:       string(csPhase),
				Url:         cs.Spec.Url,
				Description: cs.Spec.Description,
			})
			found = true
			phase = csPhase
		} else if len(csList.Items) > 1 {
			// TODO: Decide how to bubble up errors. In the cases of too many CommitStatuses or none found, today we
			//       build a "synthetic" CommitStatus. But this can be confusing, because the commitStatuses field we're
			//       populating generally contains copies of the contents of actual CommitStatus resources. We should
			//       consider whether the API should have a dedicated field for reporting errors.
			commitStatusesState = append(commitStatusesState, promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				Key:   status.Key,
				Phase: string(promoterv1alpha1.CommitPhasePending),
			})
			tooManyMatchingShaError = NewTooManyMatchingShaError(status.Key, csList.Items)
			phase = promoterv1alpha1.CommitPhasePending
		} else if len(csList.Items) == 0 {
			commitStatusesState = append(commitStatusesState, promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				Key:         status.Key,
				Phase:       string(promoterv1alpha1.CommitPhasePending),
				Description: "Waiting for status to be reported",
			})
			found = false
			phase = promoterv1alpha1.CommitPhasePending
			// We might not want to event here because of the potential for a lot of events, when say Argo CD is slow at updating the status
		}
		logger.Info("CommitStatus State",
			"key", status.Key,
			"sha", targetCommitBranchState.Hydrated.Sha,
			"phase", phase,
			"found", found,
			"tooManyMatchingSha", tooManyMatchingShaError != nil,
			"foundCount", len(csList.Items))
	}

	// Keep the URL from previous reconciliation where the phase was a success, if the commit status was not found, likely due to a sha mismatch.
	// This is to ensure that the URL is not lost when the commit status is not found in the current reconciliation.
	// We do not want to solve this with the code below please do no uncomment it. A better solution would be to come up with
	// a standard that CommitStatus managers can use to informer the CTPs the URLs for the commit statuses for each environment.
	// for _, ctpStatusState := range targetCommitBranchState.CommitStatuses { // nolint:gocritic
	//	for i, calculatedCSState := range commitStatusesState {
	//		if calculatedCSState.Key == ctpStatusState.Key && ctpStatusState.Url != "" {
	//			commitStatusesState[i].Url = ctpStatusState.Url
	//		}
	//	}
	//}
	targetCommitBranchState.CommitStatuses = commitStatusesState

	return tooManyMatchingShaError
}

func (r *ChangeTransferPolicyReconciler) setPullRequestState(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy) error {
	logger := log.FromContext(ctx)

	pr := &promoterv1alpha1.PullRequestList{}
	err := r.List(ctx, pr, ctpPullRequestListOptions(ctp))
	if err != nil {
		return fmt.Errorf("failed to list PullRequests for ChangeTransferPolicy %q status update: %w", ctp.Name, err)
	}
	if len(pr.Items) == 0 {
		// No PR resource found - keep existing status to preserve ExternallyMergedOrClosed and other metadata.
		// This allows the CTP to maintain a record of the last known PR state even after the PR resource
		// is deleted (e.g., after external merge/close). The status is only replaced when a new PR is created.
		logger.V(4).Info("No PR resource found, preserving existing PR status in CTP")
		return nil
	}

	if len(pr.Items) > 1 {
		return tooManyPRsError(pr)
	}

	if ctp.Status.PullRequest == nil {
		ctp.Status.PullRequest = &promoterv1alpha1.PullRequestCommonStatus{}
	}

	logger.V(4).Info("CTP copying PR status",
		"prName", pr.Items[0].Name,
		"prState", pr.Items[0].Status.State,
		"prID", pr.Items[0].Status.ID,
		"prDeletionTimestamp", pr.Items[0].DeletionTimestamp,
		"specState", pr.Items[0].Spec.State,
		"statusState", pr.Items[0].Status.State,
		"hasCTPFinalizer", controllerutil.ContainsFinalizer(&pr.Items[0], promoterv1alpha1.ChangeTransferPolicyPullRequestFinalizer))

	ctp.Status.PullRequest.ID = pr.Items[0].Status.ID
	ctp.Status.PullRequest.State = pr.Items[0].Status.State
	ctp.Status.PullRequest.PRCreationTime = pr.Items[0].Status.PRCreationTime
	ctp.Status.PullRequest.Url = pr.Items[0].Status.Url
	ctp.Status.PullRequest.ExternallyMergedOrClosed = pr.Items[0].Status.ExternallyMergedOrClosed

	// If PR is being deleted and has our finalizer, we need to ensure the CTP status is persisted.
	// The status will be persisted by the defer in Reconcile, and then on the next reconcile
	// (triggered by enqueue below) the finalizer will be removed by handlePRFinalizerRemoval.
	if !pr.Items[0].DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(&pr.Items[0], promoterv1alpha1.ChangeTransferPolicyPullRequestFinalizer) {
		logger.V(4).Info("PR being deleted with CTP finalizer, CTP status will be persisted and finalizer removed on next reconcile")
		// Enqueue a reconcile to trigger finalizer removal after status is persisted
		if r.enqueueFunc != nil {
			r.enqueueFunc(ctp.Namespace, ctp.Name)
		}
	}

	return nil
}

// handlePRFinalizerRemoval checks if there's a PR being deleted with our finalizer where the CTP status
// already matches the PR status. If so, it removes the finalizer to allow the PR to be deleted.
func (r *ChangeTransferPolicyReconciler) handlePRFinalizerRemoval(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy) error {
	logger := log.FromContext(ctx)

	// Find any PR resources for this CTP
	pr := &promoterv1alpha1.PullRequestList{}
	err := r.List(ctx, pr, ctpPullRequestListOptions(ctp))
	if err != nil {
		return fmt.Errorf("failed to list PullRequests for finalizer check: %w", err)
	}

	if len(pr.Items) == 0 {
		// No PR found, nothing to do
		return nil
	}

	if len(pr.Items) > 1 {
		// Multiple PRs found, return the error immediately
		return tooManyPRsError(pr)
	}

	prItem := &pr.Items[0]
	prKey := client.ObjectKeyFromObject(prItem)

	var livePR promoterv1alpha1.PullRequest
	if err := r.Get(ctx, prKey, &livePR); err != nil {
		return fmt.Errorf("failed to get PullRequest for finalizer removal: %w", err)
	}

	// Use a single live object for all checks (List can be slightly stale vs Get).
	if livePR.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(&livePR, promoterv1alpha1.ChangeTransferPolicyPullRequestFinalizer) {
		return nil
	}

	if ctp.Status.PullRequest == nil {
		logger.V(4).Info("PR being deleted but CTP has no PR status, cannot remove finalizer yet")
		return nil
	}

	statusMatches := ctp.Status.PullRequest.ID == livePR.Status.ID &&
		ctp.Status.PullRequest.State == livePR.Status.State &&
		boolPtrEqual(ctp.Status.PullRequest.ExternallyMergedOrClosed, livePR.Status.ExternallyMergedOrClosed)
	if !statusMatches {
		logger.V(4).Info("PR being deleted but CTP status doesn't match PR status, cannot remove finalizer yet",
			"ctpPRID", ctp.Status.PullRequest.ID,
			"prID", livePR.Status.ID,
			"ctpPRState", ctp.Status.PullRequest.State,
			"prState", livePR.Status.State,
			"ctpExternallyMergedOrClosed", ctp.Status.PullRequest.ExternallyMergedOrClosed,
			"prExternallyMergedOrClosed", livePR.Status.ExternallyMergedOrClosed)
		return nil
	}

	logger.Info("Removing CTP finalizer from PR - status already synced",
		"prName", livePR.Name,
		"prID", livePR.Status.ID,
		"prState", livePR.Status.State)

	prApply := pullRequestApplyOwnedByChangeTransferPolicy(&livePR, nil, false)
	prObj := &promoterv1alpha1.PullRequest{}
	prObj.Name = livePR.Name
	prObj.Namespace = livePR.Namespace
	if err := r.Patch(ctx, prObj, utils.ApplyPatch{ApplyConfig: prApply},
		client.FieldOwner(constants.ChangeTransferPolicyControllerFieldOwner), client.ForceOwnership); err != nil {
		return fmt.Errorf("failed to remove CTP finalizer from PullRequest: %w", err)
	}

	logger.V(4).Info("PR finalizer removed")
	return nil
}

// ctpPullRequestListOptions returns list options for PullRequests owned by this ChangeTransferPolicy.
func ctpPullRequestListOptions(ctp *promoterv1alpha1.ChangeTransferPolicy) *client.ListOptions {
	return &client.ListOptions{
		Namespace: ctp.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{
			promoterv1alpha1.PromotionStrategyLabel:    utils.KubeSafeLabel(ctp.Labels[promoterv1alpha1.PromotionStrategyLabel]),
			promoterv1alpha1.ChangeTransferPolicyLabel: utils.KubeSafeLabel(ctp.Name),
			promoterv1alpha1.EnvironmentLabel:          utils.KubeSafeLabel(ctp.Spec.ActiveBranch),
		}),
	}
}

// ownerReferenceToApply maps a live OwnerReference into an apply configuration fragment.
func ownerReferenceToApply(ref metav1.OwnerReference) *acmetav1.OwnerReferenceApplyConfiguration {
	return acmetav1.OwnerReference().
		WithAPIVersion(ref.APIVersion).
		WithKind(ref.Kind).
		WithName(ref.Name).
		WithUID(ref.UID).
		WithController(ptr.Deref(ref.Controller, false)).
		WithBlockOwnerDeletion(ptr.Deref(ref.BlockOwnerDeletion, false))
}

// pullRequestApplyOwnedByChangeTransferPolicy builds the Server-Side Apply configuration this reconciler uses for
// PullRequests it manages (labels, owner references, spec, and optionally the CTP PullRequest finalizer).
// When includeCTPFinalizer is false, the CTP finalizer is omitted so SSA withdraws this field manager's claim and
// the entry is removed from the live object.
func pullRequestApplyOwnedByChangeTransferPolicy(pr *promoterv1alpha1.PullRequest, specStateOverride *promoterv1alpha1.PullRequestState, includeCTPFinalizer bool) *acv1alpha1.PullRequestApplyConfiguration {
	specState := pr.Spec.State
	if specStateOverride != nil {
		specState = *specStateOverride
	}

	prSpec := acv1alpha1.PullRequestSpec().
		WithRepositoryReference(acv1alpha1.ObjectReference().WithName(pr.Spec.RepositoryReference.Name)).
		WithTitle(pr.Spec.Title).
		WithTargetBranch(pr.Spec.TargetBranch).
		WithSourceBranch(pr.Spec.SourceBranch).
		WithMergeSha(pr.Spec.MergeSha).
		WithState(specState)

	if pr.Spec.Description != "" {
		prSpec = prSpec.WithDescription(pr.Spec.Description)
	}
	if pr.Spec.Commit.Message != "" {
		prSpec = prSpec.WithCommit(acv1alpha1.CommitConfiguration().WithMessage(pr.Spec.Commit.Message))
	}
	if len(pr.Spec.Labels) > 0 {
		prSpec = prSpec.WithLabels(pr.Spec.Labels...)
	} else if pr.Spec.Labels != nil {
		prSpec.Labels = []string{}
	}

	prApply := acv1alpha1.PullRequest(pr.Name, pr.Namespace).
		WithLabels(pr.Labels)

	for i := range pr.OwnerReferences {
		prApply = prApply.WithOwnerReferences(ownerReferenceToApply(pr.OwnerReferences[i]))
	}

	if includeCTPFinalizer {
		prApply = prApply.WithFinalizers(promoterv1alpha1.ChangeTransferPolicyPullRequestFinalizer)
	}

	return prApply.WithSpec(prSpec)
}

// handleCTPCleanupOnDelete removes ChangeTransferPolicyPullRequestFinalizer from all PullRequests for this CTP.
// After that, the PullRequest controller and kube garbage collection (on a real cluster) complete removal; envtest
// does not run the garbage collector, so owned PullRequests are not cascade-deleted by the apiserver alone.
// Integration tests accept either PullRequest deletion or absence of this finalizer as proof the CTP no longer
// blocks PR cleanup.
func (r *ChangeTransferPolicyReconciler) handleCTPCleanupOnDelete(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy) error {
	logger := log.FromContext(ctx)

	prList := &promoterv1alpha1.PullRequestList{}
	err := r.List(ctx, prList, ctpPullRequestListOptions(ctp))
	if err != nil {
		return fmt.Errorf("failed to list PullRequests for ChangeTransferPolicy deletion cleanup: %w", err)
	}

	for i := range prList.Items {
		prKey := client.ObjectKeyFromObject(&prList.Items[i])

		var livePR promoterv1alpha1.PullRequest
		if err := r.Get(ctx, prKey, &livePR); err != nil {
			if k8s_errors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to get PullRequest %q: %w", prKey.Name, err)
		}
		if !controllerutil.ContainsFinalizer(&livePR, promoterv1alpha1.ChangeTransferPolicyPullRequestFinalizer) {
			continue
		}

		logger.Info("Removing ChangeTransferPolicy PullRequest finalizer during ChangeTransferPolicy deletion",
			"pullRequest", livePR.Name)

		prApply := pullRequestApplyOwnedByChangeTransferPolicy(&livePR, nil, false)
		prObj := &promoterv1alpha1.PullRequest{}
		prObj.Name = livePR.Name
		prObj.Namespace = livePR.Namespace
		if err := r.Patch(ctx, prObj, utils.ApplyPatch{ApplyConfig: prApply},
			client.FieldOwner(constants.ChangeTransferPolicyControllerFieldOwner), client.ForceOwnership); err != nil {
			return fmt.Errorf("failed to remove PullRequest finalizer for %q: %w", prKey.Name, err)
		}
	}

	return nil
}

// getPromotionStrategy fetches the PromotionStrategy for the CTP.
// It uses the controller owner reference.
// Returns nil, nil if no PS owner reference is present (PS is optional or not yet set).
// Returns an error only if the owner reference is present but the PS cannot be found.
func (r *ChangeTransferPolicyReconciler) getPromotionStrategy(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy) (*promoterv1alpha1.PromotionStrategy, error) {
	logger := log.FromContext(ctx)

	psKind := reflect.TypeFor[promoterv1alpha1.PromotionStrategy]().Name()
	for _, ref := range ctp.OwnerReferences {
		if ref.Kind == psKind && ptr.Deref(ref.Controller, false) {
			var ps promoterv1alpha1.PromotionStrategy
			if err := r.Get(ctx, client.ObjectKey{Namespace: ctp.Namespace, Name: ref.Name}, &ps); err != nil {
				return nil, fmt.Errorf("failed to get PromotionStrategy %q in namespace %q: %w", ref.Name, ctp.Namespace, err)
			}
			return &ps, nil
		}
	}

	logger.V(4).Info("ChangeTransferPolicy has no PromotionStrategy owner reference, skipping PromotionStrategy lookup")
	return nil, nil
}

// tooManyPRsError constructs an error indicating that there are too many open pull requests for the CTP.
func tooManyPRsError(pr *promoterv1alpha1.PullRequestList) error {
	prNames := make([]string, 0, len(pr.Items))
	for _, prItem := range pr.Items {
		prNames = append(prNames, prItem.Name)
	}
	// Only show the first 3 PR names and then indicate how many more there are
	summary := strings.Join(prNames, ", ")
	if len(prNames) > 3 {
		summary = strings.Join(prNames[:3], ", ") + fmt.Sprintf(" and %d more", len(prNames)-3)
	}
	return fmt.Errorf("found more than one open PullRequest: %s", summary)
}

func (r *ChangeTransferPolicyReconciler) createOrUpdatePullRequest(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy) (*promoterv1alpha1.PullRequest, error) {
	logger := log.FromContext(ctx)
	if ctp.Status.Proposed.Dry.Sha == ctp.Status.Active.Dry.Sha {
		// If the proposed dry sha is the same as the active dry sha, no need to create a pull request
		logger.V(4).Info("No promotion needed - active branch already has proposed changes",
			"activeDrySha", ctp.Status.Active.Dry.Sha,
			"proposedDrySha", ctp.Status.Proposed.Dry.Sha)
		// If there's a PullRequest resource, enqueue it so it quickly realizes it's already merged
		// (thus the matching active/proposed dry shas) and gets cleaned up.
		r.enqueuePullRequestsForCTP(ctx, ctp)
		return nil, nil
	}

	logger.V(4).Info("Proposed dry sha, does not match active", "proposedDrySha", ctp.Status.Proposed.Dry.Sha, "activeDrySha", ctp.Status.Active.Dry.Sha)
	gitRepo, err := utils.GetGitRepositoryFromObjectKey(ctx, r.Client, client.ObjectKey{Namespace: ctp.Namespace, Name: ctp.Spec.RepositoryReference.Name})
	if err != nil {
		return nil, fmt.Errorf("failed to get GitRepository %q: %w", ctp.Spec.RepositoryReference.Name, err)
	}

	var prName string
	switch {
	case gitRepo.Spec.GitHub != nil:
		prName = utils.GetPullRequestName(gitRepo.Spec.GitHub.Owner, gitRepo.Spec.GitHub.Name, ctp.Spec.ProposedBranch, ctp.Spec.ActiveBranch)
	case gitRepo.Spec.GitLab != nil:
		prName = utils.GetPullRequestName(gitRepo.Spec.GitLab.Namespace, gitRepo.Spec.GitLab.Name, ctp.Spec.ProposedBranch, ctp.Spec.ActiveBranch)
	case gitRepo.Spec.Forgejo != nil:
		prName = utils.GetPullRequestName(gitRepo.Spec.Forgejo.Owner, gitRepo.Spec.Forgejo.Name, ctp.Spec.ProposedBranch, ctp.Spec.ActiveBranch)
	case gitRepo.Spec.Gitea != nil:
		prName = utils.GetPullRequestName(gitRepo.Spec.Gitea.Owner, gitRepo.Spec.Gitea.Name, ctp.Spec.ProposedBranch, ctp.Spec.ActiveBranch)
	case gitRepo.Spec.Fake != nil:
		prName = utils.GetPullRequestName(gitRepo.Spec.Fake.Owner, gitRepo.Spec.Fake.Name, ctp.Spec.ProposedBranch, ctp.Spec.ActiveBranch)
	case gitRepo.Spec.BitbucketCloud != nil:
		prName = utils.GetPullRequestName(gitRepo.Spec.BitbucketCloud.Owner, gitRepo.Spec.BitbucketCloud.Name, ctp.Spec.ProposedBranch, ctp.Spec.ActiveBranch)
	case gitRepo.Spec.AzureDevOps != nil:
		prName = utils.GetPullRequestName(gitRepo.Spec.AzureDevOps.Project, gitRepo.Spec.AzureDevOps.Name, ctp.Spec.ProposedBranch, ctp.Spec.ActiveBranch)
	default:
		return nil, errors.New("unsupported git repository type")
	}

	prName = utils.KubeSafeUniqueName(prName)

	ps, err := r.getPromotionStrategy(ctx, ctp)
	if err != nil {
		return nil, fmt.Errorf("failed to get PromotionStrategy for template: %w", err)
	}

	templatePullRequestTemplate, err := r.SettingsMgr.GetPullRequestControllersTemplate(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pull request template from settings: %w", err)
	}

	// Template receives the current CTP and its PromotionStrategy.
	templateData := map[string]any{
		"ChangeTransferPolicy": ctp,
		"PromotionStrategy":    ps,
	}
	title, description, err := TemplatePullRequest(templatePullRequestTemplate, templateData)
	if err != nil {
		return nil, fmt.Errorf("failed to template pull request: %w", err)
	}

	// Check if the PR already exists to determine the commit message
	existingPR := &promoterv1alpha1.PullRequest{}
	prExists := true
	if err := r.Get(ctx, client.ObjectKey{Namespace: ctp.Namespace, Name: prName}, existingPR); err != nil {
		if !k8s_errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get existing PullRequest: %w", err)
		}
		prExists = false
	}

	// Build owner reference
	kind := reflect.TypeFor[promoterv1alpha1.ChangeTransferPolicy]().Name()
	gvk := promoterv1alpha1.GroupVersion.WithKind(kind)

	// Determine the commit message based on whether this is a new or existing PR
	var commitMessage string
	if !prExists {
		// New PR
		commitMessage = fmt.Sprintf("%s\n\n%s", title, description)
	} else {
		// Update existing PR - add trailers
		commitTrailers := trailers{}
		commitTrailers[constants.TrailerPullRequestID] = existingPR.Status.ID
		commitTrailers[constants.TrailerPullRequestSourceBranch] = ctp.Spec.GetPromotionSourceBranch()
		commitTrailers[constants.TrailerPullRequestTargetBranch] = ctp.Spec.ActiveBranch
		commitTrailers[constants.TrailerPullRequestCreationTime] = existingPR.Status.PRCreationTime.Format(time.RFC3339)
		commitTrailers[constants.TrailerPullRequestUrl] = existingPR.Status.Url

		if err := addCommitStatusTrailers(commitTrailers, constants.TrailerCommitStatusActivePrefix, ctp.Status.Active.CommitStatuses); err != nil {
			return nil, err
		}
		if err := addCommitStatusTrailers(commitTrailers, constants.TrailerCommitStatusProposedPrefix, ctp.Status.Proposed.CommitStatuses); err != nil {
			return nil, err
		}
		commitTrailers[constants.TrailerShaHydratedActive] = ctp.Status.Active.Hydrated.Sha
		commitTrailers[constants.TrailerShaHydratedProposed] = ctp.Status.Proposed.Hydrated.Sha
		commitTrailers[constants.TrailerShaDryActive] = ctp.Status.Active.Dry.Sha
		commitTrailers[constants.TrailerShaDryProposed] = ctp.Status.Proposed.Dry.Sha

		commitMessage = fmt.Sprintf("%s\n\n%s\n\n%s", title, description, commitTrailers)
	}

	// Determine the state: preserve existing state if PR exists, otherwise default to open
	prState := promoterv1alpha1.PullRequestOpen
	if prExists {
		prState = existingPR.Spec.State
	}

	desiredLabels, manageLabels, err := r.evaluatePullRequestLabels(ctp, ps)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate pull request labels: %w", err)
	}

	prLabels := utils.StampInstanceIDLabel(map[string]string{
		promoterv1alpha1.PromotionStrategyLabel:    utils.KubeSafeLabel(ctp.Labels[promoterv1alpha1.PromotionStrategyLabel]),
		promoterv1alpha1.ChangeTransferPolicyLabel: utils.KubeSafeLabel(ctp.Name),
		promoterv1alpha1.EnvironmentLabel:          utils.KubeSafeLabel(ctp.Spec.ActiveBranch),
	})

	draftPR := &promoterv1alpha1.PullRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prName,
			Namespace: ctp.Namespace,
			Labels:    prLabels,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         gvk.GroupVersion().String(),
					Kind:               kind,
					Name:               ctp.Name,
					UID:                ctp.UID,
					Controller:         new(true),
					BlockOwnerDeletion: new(true),
				},
			},
		},
		Spec: promoterv1alpha1.PullRequestSpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{Name: ctp.Spec.RepositoryReference.Name},
			Title:               title,
			TargetBranch:        ctp.Spec.ActiveBranch,
			SourceBranch:        ctp.Spec.GetPromotionSourceBranch(),
			Description:         description,
			Commit:              promoterv1alpha1.CommitConfiguration{Message: commitMessage},
			MergeSha:            ctp.Status.Proposed.Hydrated.Sha,
			State:               prState,
		},
	}
	if manageLabels {
		draftPR.Spec.Labels = desiredLabels
	} else if prExists {
		draftPR.Spec.Labels = existingPR.Spec.Labels
	}

	prApply := pullRequestApplyOwnedByChangeTransferPolicy(draftPR, nil, true)

	// Apply using Server-Side Apply with Patch to get the result directly
	pr := &promoterv1alpha1.PullRequest{}
	pr.Name = prName
	pr.Namespace = ctp.Namespace
	if err = r.Patch(ctx, pr, utils.ApplyPatch{ApplyConfig: prApply}, client.FieldOwner(constants.ChangeTransferPolicyControllerFieldOwner), client.ForceOwnership); err != nil {
		return nil, fmt.Errorf("failed to apply PullRequest %q: %w", prName, err)
	}

	// Log and emit events
	if !prExists {
		r.Recorder.Eventf(ctp, nil, "Normal", constants.PullRequestCreatedReason, "CreatingPullRequest", constants.PullRequestCreatedMessage, pr.Name)
		logger.V(4).Info("Created pull request", "pullRequest", pr.Name)
	} else {
		logger.V(4).Info("Applied pull request", "pullRequest", pr.Name)
	}

	return pr, nil
}

func ctpStatusShowsPullRequestExists(ctp *promoterv1alpha1.ChangeTransferPolicy) bool {
	pr := ctp.Status.PullRequest
	if pr == nil || pr.ID == "" {
		return false
	}
	if pr.ExternallyMergedOrClosed != nil && *pr.ExternallyMergedOrClosed {
		return false
	}
	return pr.State == promoterv1alpha1.PullRequestOpen
}

func (r *ChangeTransferPolicyReconciler) enqueuePullRequestsForCTP(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy) {
	if r.EnqueuePR == nil || !ctpStatusShowsPullRequestExists(ctp) {
		return
	}
	prList := &promoterv1alpha1.PullRequestList{}
	if err := r.List(ctx, prList, ctpPullRequestListOptions(ctp)); err != nil {
		log.FromContext(ctx).Error(err, "failed to list PullRequests to enqueue for SCM sync")
		return
	}
	for i := range prList.Items {
		pr := &prList.Items[i]
		if pr.DeletionTimestamp.IsZero() {
			r.EnqueuePR(pr.Namespace, pr.Name)
		}
	}
}

func (r *ChangeTransferPolicyReconciler) evaluatePullRequestLabels(ctp *promoterv1alpha1.ChangeTransferPolicy, ps *promoterv1alpha1.PromotionStrategy) ([]string, bool, error) {
	if ctp.Spec.PullRequest == nil || ctp.Spec.PullRequest.Labels == nil || ctp.Spec.PullRequest.Labels.Expression == "" {
		return nil, false, nil
	}

	desiredLabels, err := r.labelEvaluator.Evaluate(ctp.Spec.PullRequest.Labels.Expression, prlabels.ExpressionContext{
		Status:            ctp.Status,
		Spec:              ctp.Spec,
		PromotionStrategy: ps,
	})
	if err != nil {
		return nil, false, fmt.Errorf("failed to evaluate pull request labels expression: %w", err)
	}

	return desiredLabels, true, nil
}

// mergePullRequests tries to merge the pull request if all the checks have passed and the environment is set to auto merge.
func (r *ChangeTransferPolicyReconciler) mergePullRequests(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy) (*promoterv1alpha1.PullRequest, error) {
	logger := log.FromContext(ctx)

	for i, status := range ctp.Status.Proposed.CommitStatuses {
		if status.Phase != string(promoterv1alpha1.CommitPhaseSuccess) {
			logger.V(4).Info("Proposed commit status is not success", "key", ctp.Spec.ProposedCommitStatuses[i].Key, "sha", ctp.Status.Proposed.Hydrated.Sha, "phase", status.Phase)
			return nil, nil
		}
	}

	if !*ctp.Spec.AutoMerge {
		return nil, nil
	}

	prl := promoterv1alpha1.PullRequestList{}
	// Find the PRs that match the proposed commit and the environment. There should only be one.
	err := r.List(ctx, &prl, &client.ListOptions{
		Namespace: ctp.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{
			promoterv1alpha1.PromotionStrategyLabel:    utils.KubeSafeLabel(ctp.Labels[promoterv1alpha1.PromotionStrategyLabel]),
			promoterv1alpha1.ChangeTransferPolicyLabel: utils.KubeSafeLabel(ctp.Name),
			promoterv1alpha1.EnvironmentLabel:          utils.KubeSafeLabel(ctp.Spec.ActiveBranch),
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list PullRequests for ChangeTransferPolicy %s and Environment %s: %w", ctp.Name, ctp.Spec.ActiveBranch, err)
	}

	if len(prl.Items) > 1 {
		return nil, tooManyPRsError(&prl)
	}

	if len(prl.Items) != 1 {
		return nil, nil
	}

	// We found 1 pull request process it.
	pullRequest := prl.Items[0]
	if pullRequest.Status.State == promoterv1alpha1.PullRequestOpen {
		logger.Info("Commit status checks passed", "branch", ctp.Spec.ActiveBranch,
			"activeCommitStatuses", ctp.Status.Active.CommitStatuses,
			"proposedCommitStatuses", ctp.Status.Proposed.CommitStatuses,
			"activeDryCommitTime", ctp.Status.Active.Dry.CommitTime)
	}

	if pullRequest.Status.State != promoterv1alpha1.PullRequestOpen {
		// Nothing to do, the PR has to be open to be merged.
		return &pullRequest, nil
	}

	if pullRequest.Spec.State != promoterv1alpha1.PullRequestOpen {
		// This is for the case where the PR is set to merge in k8s but something else is blocking it, like an external commit status check.
		logger.Info("Pull request can not be merged, probably due to SCM", "pr", pullRequest.Name)

		return &pullRequest, nil
	}

	if pullRequest.Status.ID == "" {
		// We could rely on XValidation to catch the missing ID when setting the PR to merged, but this gives a
		// better error message.

		// If the PR has a ready condition with status false, get that reason/message for this error message.
		prReadyCondition := meta.FindStatusCondition(pullRequest.Status.Conditions, string(promoterConditions.Ready))
		if prReadyCondition != nil && prReadyCondition.Status == metav1.ConditionFalse {
			return &pullRequest, fmt.Errorf("cannot merge PullRequest without an ID: PullRequest not ready: %s: %s", prReadyCondition.Reason, prReadyCondition.Message)
		}

		return &pullRequest, fmt.Errorf("cannot merge PullRequest %q without an ID", pullRequest.Name)
	}

	// Update the PR state to merged using SSA.
	// Re-specify labels, owner references, finalizers, and spec so this field manager stays consistent with createOrUpdatePullRequest.
	prApply := pullRequestApplyOwnedByChangeTransferPolicy(&pullRequest, ptr.To(promoterv1alpha1.PullRequestMerged), true)

	// Apply using Server-Side Apply with Patch to get the result directly
	pr := &promoterv1alpha1.PullRequest{}
	pr.Name = pullRequest.Name
	pr.Namespace = pullRequest.Namespace
	if err := r.Patch(ctx, pr, utils.ApplyPatch{ApplyConfig: prApply}, client.FieldOwner(constants.ChangeTransferPolicyControllerFieldOwner), client.ForceOwnership); err != nil {
		return &pullRequest, fmt.Errorf("failed to apply PR %q state to merged: %w", pullRequest.Name, err)
	}
	r.Recorder.Eventf(ctp, nil, "Normal", constants.PullRequestMergedReason, "MergingPullRequest", constants.PullRequestMergedMessage, pr.Name)
	logger.Info("Merged pull request")
	return pr, nil
}

// gitMergeStrategyOurs tests if there is a conflict between the active and proposed branches. If there is, we
// perform a merge with ours as the strategy. This is to prevent conflicts in the pull request by assuming that
// the proposed branch is the source of truth.
//
// Returns true if a merge was performed (and therefore the proposed branch tip on origin is now ahead of
// ctp.Status.Proposed.Hydrated.Sha as set by calculateStatus). Callers should requeue rather than continue this
// reconcile when true is returned, so that the next reconcile re-derives Status.Proposed from the new tip.
func (r *ChangeTransferPolicyReconciler) gitMergeStrategyOurs(ctx context.Context, gitOperations *git.EnvironmentOperations, ctp *promoterv1alpha1.ChangeTransferPolicy) (bool, error) {
	logger := log.FromContext(ctx)
	logger.Info("Testing for conflicts between branches", "proposed", ctp.Spec.GetPromotionSourceBranch(), "active", ctp.Spec.ActiveBranch)

	// Check if there's a conflict between branches
	hasConflict, err := gitOperations.HasConflict(ctx, ctp.Spec.GetPromotionSourceBranch(), ctp.Spec.ActiveBranch)
	if err != nil {
		return false, fmt.Errorf("failed to check for conflicts between branches %q and %q: %w", ctp.Spec.GetPromotionSourceBranch(), ctp.Spec.ActiveBranch, err)
	}

	if !hasConflict {
		logger.V(4).Info("No conflicts detected between branches", "proposed", ctp.Spec.GetPromotionSourceBranch(), "active", ctp.Spec.ActiveBranch)
		return false, nil // No conflict, nothing to do
	}

	// If we have a conflict, perform a merge with "ours" strategy
	logger.Info("Conflicts detected, performing merge with 'ours' strategy", "proposed", ctp.Spec.GetPromotionSourceBranch(), "active", ctp.Spec.ActiveBranch, "activePath", ctp.Spec.ActivePath)

	if ctp.Spec.ActivePath != "" {
		err = gitOperations.MergeWithOursStrategyForPath(ctx, ctp.Spec.GetPromotionSourceBranch(), ctp.Spec.ActiveBranch, ctp.Spec.ActivePath)
	} else {
		err = gitOperations.MergeWithOursStrategy(ctx, ctp.Spec.GetPromotionSourceBranch(), ctp.Spec.ActiveBranch)
	}
	if err != nil {
		return false, fmt.Errorf("failed to merge branches %q and %q with 'ours' strategy: %w", ctp.Spec.GetPromotionSourceBranch(), ctp.Spec.ActiveBranch, err)
	}

	r.Recorder.Eventf(ctp, nil, "Normal", constants.ResolvedConflictReason, "ResolvingConflict", constants.ResolvedConflictMessage, ctp.Spec.GetPromotionSourceBranch(), ctp.Spec.ActiveBranch)

	return true, nil
}

// handleFinalizer ensures ChangeTransferPolicyPullRequestCleanupFinalizer is on the CTP while it exists so deletion
// runs handleCTPCleanupOnDelete (strip ChangeTransferPolicyPullRequestFinalizer from PullRequests) before the policy
// is removed. This is reconciled at entry, separate from PullRequest SSA in createOrUpdatePullRequest / mergePullRequests.
//
// The first bool is true when Reconcile should not run normal promotion work (for example the CTP is deleting, so we
// must not re-add PR finalizers via createOrUpdatePullRequest / mergePullRequests).
func (r *ChangeTransferPolicyReconciler) handleFinalizer(ctx context.Context, ctp *promoterv1alpha1.ChangeTransferPolicy) (bool, error) {
	finalizer := promoterv1alpha1.ChangeTransferPolicyPullRequestCleanupFinalizer

	if ctp.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(ctp, finalizer) {
			// Not being deleted and already has finalizer, nothing to do.
			return false, nil
		}

		// Finalizer is missing, add it.
		return false, retry.RetryOnConflict(retry.DefaultRetry, func() error { //nolint:wrapcheck // RetryOnConflict returns wrapped error
			if err := r.Get(ctx, client.ObjectKeyFromObject(ctp), ctp); err != nil {
				return err //nolint:wrapcheck // error will be wrapped by caller
			}
			if controllerutil.AddFinalizer(ctp, finalizer) {
				return r.Update(ctx, ctp) //nolint:wrapcheck // RetryOnConflict returns wrapped error
			}
			return nil
		})
	}

	// If we're here, the object is being deleted
	if !controllerutil.ContainsFinalizer(ctp, finalizer) {
		// Finalizer already removed; still skip normal reconcile while terminating.
		return true, nil
	}

	// Remove CTP finalizers from any PRs. The CTP is being deleted, we don't care about monitoring those PRs anymore.
	err := r.handleCTPCleanupOnDelete(ctx, ctp)
	if err != nil {
		return false, fmt.Errorf("failed to clean up PullRequest finalizers for deleted ChangeTransferPolicy: %w", err)
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error { //nolint:wrapcheck // RetryOnConflict returns wrapped error
		if err := r.Get(ctx, client.ObjectKeyFromObject(ctp), ctp); err != nil {
			return err //nolint:wrapcheck // error will be wrapped by caller
		}
		if controllerutil.RemoveFinalizer(ctp, finalizer) {
			return r.Update(ctx, ctp) //nolint:wrapcheck // error will be wrapped by caller
		}
		return nil
	}); err != nil {
		return true, fmt.Errorf("failed to remove finalizer: %w", err)
	}
	return true, nil
}

// TemplatePullRequest renders the title and description of a pull request using the provided data map.
func TemplatePullRequest(prt promoterv1alpha1.PullRequestTemplate, data map[string]any) (string, string, error) {
	title, err := utils.RenderStringTemplate(prt.Title, data)
	if err != nil {
		return "", "", fmt.Errorf("failed to render pull request title template: %w", err)
	}

	description, err := utils.RenderStringTemplate(prt.Description, data)
	if err != nil {
		return "", "", fmt.Errorf("failed to render pull request description template: %w", err)
	}

	return title, description, nil
}

// pullRequestUpdateEnqueuesChangeTransferPolicyPredicate limits CTP reconciles triggered by owned
// PullRequests. Without it, bare Owns(PullRequest) re-enqueues CTP on every PR status write and can
// drive a CTP→PR→CTP feedback loop (PR SCM sync → status churn → CTP SSA → PR generation bump → repeat).
//
// Enqueues CTP on:
//   - Create and Delete of the owned PullRequest
//   - Update when metadata.generation changes (spec was patched — title, commit.message, state, etc.)
//   - Update when the CTP pull-request finalizer is added or removed
//   - Update when status.state changes (open, merged, closed, or cleared on external close)
//   - Update when status.externallyMergedOrClosed changes
//   - Update when status.id is first set ("" → non-empty; PR now exists on the SCM)
//
// Does not enqueue CTP on status-only updates, including:
//   - status.url (stable or changed)
//   - status.prCreationTime (e.g. fake FindOpen returns time.Now() each sync)
//   - status.conditions (Ready message, reason, lastTransitionTime)
//   - status.observedGeneration
//   - status.id changes after the ID is already populated
func pullRequestUpdateEnqueuesChangeTransferPolicyPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		DeleteFunc: func(event.DeleteEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPR, ok := e.ObjectOld.(*promoterv1alpha1.PullRequest)
			if !ok || oldPR == nil {
				return false
			}
			newPR, ok := e.ObjectNew.(*promoterv1alpha1.PullRequest)
			if !ok || newPR == nil {
				return false
			}
			if oldPR.Generation != newPR.Generation {
				return true
			}
			oldHasCTPFinalizer := controllerutil.ContainsFinalizer(oldPR, promoterv1alpha1.ChangeTransferPolicyPullRequestFinalizer)
			newHasCTPFinalizer := controllerutil.ContainsFinalizer(newPR, promoterv1alpha1.ChangeTransferPolicyPullRequestFinalizer)
			if oldHasCTPFinalizer != newHasCTPFinalizer {
				return true
			}
			if oldPR.Status.State != newPR.Status.State {
				return true
			}
			if !boolPtrEqual(oldPR.Status.ExternallyMergedOrClosed, newPR.Status.ExternallyMergedOrClosed) {
				return true
			}
			if oldPR.Status.ID == "" && newPR.Status.ID != "" {
				return true
			}
			return false
		},
	}
}

// boolPtrEqual compares two *bool pointers for equality.
// Returns true if both are nil, or if both are non-nil and point to equal values.
func boolPtrEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
