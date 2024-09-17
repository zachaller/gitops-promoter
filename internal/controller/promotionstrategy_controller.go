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
	"k8s.io/apimachinery/pkg/util/rand"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"time"

	"k8s.io/client-go/util/retry"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// PromotionStrategyReconciler reconciles a PromotionStrategy object
type PromotionStrategyReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	indexer client.FieldIndexer
}

//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PromotionStrategy object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.2/pkg/reconcile
func (r *PromotionStrategyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(4).Info("Reconciling PromotionStrategy", "namespace", req.Namespace, "name", req.Name)

	var ps promoterv1alpha1.PromotionStrategy
	err := r.Get(ctx, req.NamespacedName, &ps, &client.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("PromotionStrategy not found", "namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get PromotionStrategy", "namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, err
	}

	// If a ProposedCommit does not exist, create it othwise get it and store the ProposedCommit in a map with the branch as the key.
	var proposedCommitMap = make(map[string]*promoterv1alpha1.ProposedCommit)
	for _, environment := range ps.Spec.Environments {
		pc, err := r.createOrGetProposedCommit(ctx, &ps, environment)
		if err != nil {
			logger.Error(err, "failed to create ProposedCommit", "namespace", ps.Namespace, "name", ps.Name)
			return ctrl.Result{}, err
		}
		proposedCommitMap[environment.Branch] = pc
	}

	err = r.calculateStatus(ctx, &ps, proposedCommitMap)
	if err != nil {
		return ctrl.Result{}, err
	}

	for _, environment := range ps.Spec.Environments {
		_, previousEnvironmentStatus := utils.GetPreviousEnvironmentStatusByBranch(ps, environment.Branch)
		environmentIndex, environmentStatus := utils.GetEnvironmentStatusByBranch(ps, environment.Branch)

		if previousEnvironmentStatus != nil {
			// If the previous environment's running commit is the same as the current proposed commit, copy the commit statuses.
			if previousEnvironmentStatus.Active.Dry.Sha == proposedCommitMap[environment.Branch].Status.Proposed.Dry.Sha {
				err = r.copyCommitStatuses(ctx, append(environment.ActiveCommitStatuses, ps.Spec.ActiveCommitStatuses...), previousEnvironmentStatus.Active.Hydrated.Sha, proposedCommitMap[environment.Branch].Status.Proposed.Hydrated.Sha, previousEnvironmentStatus.Branch) //pc.Status.Active.Hydrated.Sha
				if err != nil {
					return ctrl.Result{}, err
				}
			}
		}

		activeChecksPassed := previousEnvironmentStatus != nil &&
			previousEnvironmentStatus.Active.CommitStatus.State == "success" &&
			previousEnvironmentStatus.Active.Dry.Sha == proposedCommitMap[environment.Branch].Status.Proposed.Dry.Sha &&
			previousEnvironmentStatus.Active.Dry.CommitTime.After(environmentStatus.Active.Dry.CommitTime.Time)

		proposedChecksPassed := environmentStatus != nil &&
			environmentStatus.Proposed.CommitStatus.State == "success"

		if (environmentIndex == 0 || (activeChecksPassed && proposedChecksPassed)) && environment.GetAutoMerge() {
			prl := promoterv1alpha1.PullRequestList{}
			err := r.List(ctx, &prl, &client.ListOptions{
				LabelSelector: labels.SelectorFromSet(map[string]string{
					"promoter.argoproj.io/promotion-strategy": utils.KubeSafeLabel(ctx, ps.Name),
					"promoter.argoproj.io/proposed-commit":    utils.KubeSafeLabel(ctx, proposedCommitMap[environment.Branch].Name),
					"promoter.argoproj.io/environment":        utils.KubeSafeLabel(ctx, environment.Branch),
				}),
			})
			if err != nil {
				return ctrl.Result{}, err
			}

			if len(prl.Items) > 0 && prl.Items[0].Status.State == promoterv1alpha1.PullRequestOpen {
				if previousEnvironmentStatus != nil {
					logger.Info("Active checks passed", "branch", environment.Branch,
						"autoMerge", environment.AutoMerge,
						"previousEnvironmentState", previousEnvironmentStatus.Active.CommitStatus.State,
						"previousEnvironmentSha", previousEnvironmentStatus.Active.CommitStatus.Sha,
						"previousEnvironmentCommitTime", previousEnvironmentStatus.Active.Dry.CommitTime,
						"currentEnvironmentCommitTime", environmentStatus.Active.Dry.CommitTime)
				} else {
					logger.Info("Active checks passed without previous environment", "branch", environment.Branch,
						"autoMerge", environment.AutoMerge,
						"numberOfActiveCommitStatuses", len(append(environment.ActiveCommitStatuses, ps.Spec.ActiveCommitStatuses...)))
				}
			}

			if len(prl.Items) > 0 && prl.Items[0].Spec.State == promoterv1alpha1.PullRequestOpen && prl.Items[0].Status.State == promoterv1alpha1.PullRequestOpen {
				prl.Items[0].Spec.State = promoterv1alpha1.PullRequestMerged
				err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					err := r.Get(ctx, client.ObjectKey{Namespace: prl.Items[0].Namespace, Name: prl.Items[0].Name}, &prl.Items[0], &client.GetOptions{})
					if err != nil {
						return err
					}
					prl.Items[0].Spec.State = promoterv1alpha1.PullRequestMerged
					return r.Update(ctx, &prl.Items[0])
				})
				if err != nil {
					return ctrl.Result{}, err
				}
			} else if len(prl.Items) > 0 && prl.Items[0].Status.State == promoterv1alpha1.PullRequestOpen {
				logger.Info("Pull request not ready to merge yet", "namespace", prl.Items[0].Namespace, "name", prl.Items[0].Name)
			}
		}

	}

	err = r.Status().Update(ctx, &ps)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{
		Requeue:      true,
		RequeueAfter: 10 * time.Second,
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromotionStrategyReconciler) SetupWithManager(mgr ctrl.Manager) error {

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &promoterv1alpha1.CommitStatus{}, ".spec.sha", func(rawObj client.Object) []string {
		cs := rawObj.(*promoterv1alpha1.CommitStatus)
		return []string{cs.Spec.Sha}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&promoterv1alpha1.PromotionStrategy{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&promoterv1alpha1.ProposedCommit{}).
		Complete(r)
}

func (r *PromotionStrategyReconciler) createOrGetProposedCommit(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy, environment promoterv1alpha1.Environment) (*promoterv1alpha1.ProposedCommit, error) {
	logger := log.FromContext(ctx)

	pc := promoterv1alpha1.ProposedCommit{}
	//TODO: should add a hash of the ps.Name and environment.Branch to the name
	pcName := utils.KubeSafeUniqueName(ctx, fmt.Sprintf("%s-%s", ps.Name, environment.Branch))
	err := r.Get(ctx, client.ObjectKey{Namespace: ps.Namespace, Name: pcName}, &pc, &client.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("ProposedCommit not found, creating", "namespace", ps.Namespace, "name", pcName)

			// The code below sets the ownership for the Release Object
			kind := reflect.TypeOf(promoterv1alpha1.PromotionStrategy{}).Name()
			gvk := promoterv1alpha1.GroupVersion.WithKind(kind)
			controllerRef := metav1.NewControllerRef(ps, gvk)

			pc = promoterv1alpha1.ProposedCommit{
				ObjectMeta: metav1.ObjectMeta{
					Name:            pcName,
					Namespace:       ps.Namespace,
					OwnerReferences: []metav1.OwnerReference{*controllerRef},
					Labels: map[string]string{
						"promoter.argoproj.io/promotion-strategy": utils.KubeSafeLabel(ctx, ps.Name),
						"promoter.argoproj.io/proposed-commit":    utils.KubeSafeLabel(ctx, pcName),
						"promoter.argoproj.io/environment":        utils.KubeSafeLabel(ctx, environment.Branch),
					},
				},
				Spec: promoterv1alpha1.ProposedCommitSpec{
					RepositoryReference: ps.Spec.RepositoryReference,
					ProposedBranch:      fmt.Sprintf("%s-%s", environment.Branch, "next"),
					ActiveBranch:        environment.Branch,
				},
			}

			err = r.Create(ctx, &pc)
			if err != nil {
				return &pc, err
			}
		} else {
			logger.Error(err, "failed to get ProposedCommit", "namespace", ps.Namespace, "name", pcName)
			return &pc, err
		}
	}

	// Check that status has been updated if not retry in a loop until it is updated
	for {
		err = r.Get(ctx, client.ObjectKey{Namespace: ps.Namespace, Name: pcName}, &pc, &client.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				// Might not be found yet due to informer cache delay
				continue
			}
			return &pc, err
		}
		if pc.Status.Active.Dry.Sha != "" && pc.Status.Active.Hydrated.Sha != "" && pc.Status.Proposed.Dry.Sha != "" && pc.Status.Proposed.Hydrated.Sha != "" {
			break
		}
		// Add some sleep jitter to not spam Get requests while waiting for ProposedCommit controller to reconcile.
		sleepTime := time.Duration(rand.Intn(1000)) * time.Millisecond
		time.Sleep(sleepTime)
		logger.V(0).Info("ProposedCommit status not updated yet, retrying", "namespace", ps.Namespace, "name", pcName, "sleepTime", sleepTime)
	}

	return &pc, nil
}

func (r *PromotionStrategyReconciler) calculateStatus(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy, pcMap map[string]*promoterv1alpha1.ProposedCommit) error {
	for _, environment := range ps.Spec.Environments {
		pc, ok := pcMap[environment.Branch]
		if !ok {
			return fmt.Errorf("ProposedCommit not found for branch %s", environment.Branch)
		}

		ps.Status.Environments = utils.UpsertEnvironmentStatus(ps.Status.Environments, func() promoterv1alpha1.EnvironmentStatus {
			status := promoterv1alpha1.EnvironmentStatus{
				Branch: environment.Branch,
				Active: promoterv1alpha1.PromotionStrategyBranchStateStatus{
					Dry:      promoterv1alpha1.ProposedCommitShaState{Sha: pc.Status.Active.Dry.Sha, CommitTime: pc.Status.Active.Dry.CommitTime},
					Hydrated: promoterv1alpha1.ProposedCommitShaState{Sha: pc.Status.Active.Hydrated.Sha, CommitTime: pc.Status.Active.Hydrated.CommitTime},
					CommitStatus: promoterv1alpha1.PromotionStrategyCommitStatus{
						State: "unknown",
						Sha:   "unknown",
					},
				},
				Proposed: promoterv1alpha1.PromotionStrategyBranchStateStatus{
					Dry:      promoterv1alpha1.ProposedCommitShaState{Sha: pc.Status.Proposed.Dry.Sha, CommitTime: pc.Status.Proposed.Dry.CommitTime},
					Hydrated: promoterv1alpha1.ProposedCommitShaState{Sha: pc.Status.Proposed.Hydrated.Sha, CommitTime: pc.Status.Proposed.Hydrated.CommitTime},
					CommitStatus: promoterv1alpha1.PromotionStrategyCommitStatus{
						State: "unknown",
						Sha:   "unknown",
					},
				},
			}

			return status
		}())

		i, _ := utils.GetEnvironmentStatusByBranch(*ps, environment.Branch)

		if i >= len(ps.Status.Environments) && len(ps.Status.Environments[i].LastHealthyDryShas) > 10 {
			ps.Status.Environments[i].LastHealthyDryShas = ps.Status.Environments[i].LastHealthyDryShas[:10]
		}

		//Bubble up active CommitStatus to PromotionStrategy Status
		activeCommitStatusList := append(environment.ActiveCommitStatuses, ps.Spec.ActiveCommitStatuses...)
		allActiveCSList := []promoterv1alpha1.CommitStatus{}
		for _, status := range activeCommitStatusList {
			var csList promoterv1alpha1.CommitStatusList
			err := r.List(ctx, &csList, &client.ListOptions{
				LabelSelector: labels.SelectorFromSet(map[string]string{
					"promoter.argoproj.io/commit-status": utils.KubeSafeLabel(ctx, status.Key),
				}),
				FieldSelector: fields.SelectorFromSet(map[string]string{
					".spec.sha": pc.Status.Active.Hydrated.Sha,
				}),
			})
			if err != nil {
				return err
			}

			csListSlice := []promoterv1alpha1.CommitStatus{}
			for _, item := range csList.Items {
				if item.Labels["promoter.argoproj.io/commit-status-copy"] != "true" {
					csListSlice = append(csListSlice, item)
				}
			}

			if len(csListSlice) == 1 {
				allActiveCSList = append(allActiveCSList, csListSlice[0])
				//ps.Status.Environments[i].Active.CommitStatus.State = string(csList.Items[0].Spec.State)
				//ps.Status.Environments[i].Active.CommitStatus.Sha = csList.Items[0].Spec.Sha
			} else if len(csListSlice) > 1 {
				ps.Status.Environments[i].Active.CommitStatus.State = "to-many-matching-sha"
				ps.Status.Environments[i].Active.CommitStatus.Sha = "to-many-matching-sha"
			} else if len(csListSlice) == 0 {
				ps.Status.Environments[i].Active.CommitStatus.State = "no-commit-status-found"
				ps.Status.Environments[i].Active.CommitStatus.Sha = "no-commit-status-found"
			}

		}

		//&& (ps.Status.Environments[i].Active.CommitStatus.State != "no-commit-status-found" || ps.Status.Environments[i].Active.CommitStatus.State != "to-many-matching-sha")
		if len(allActiveCSList) == 0 && i >= 0 {
			ps.Status.Environments[i].Proposed.CommitStatus.State = "success"
			ps.Status.Environments[i].Proposed.CommitStatus.Sha = pc.Status.Active.Hydrated.Sha
		} else {
			// Loop through allActiveCSList and bubble up success if all are successful
			ps.Status.Environments[i].Active.CommitStatus.State = "success"
			for _, cs := range allActiveCSList {
				if cs.Status.State != "success" {
					ps.Status.Environments[i].Active.CommitStatus.State = string(cs.Spec.State)
					ps.Status.Environments[i].Active.CommitStatus.Sha = cs.Spec.Sha
					break
				}
			}
		}

		//Bubble up proposed CommitStatus to PromotionStrategy Status
		proposedCommitStatusList := append(environment.ProposedCommitStatuses, ps.Spec.ProposedCommitStatuses...)
		allProposdedCSList := []promoterv1alpha1.CommitStatus{}
		for _, status := range proposedCommitStatusList {
			var csList promoterv1alpha1.CommitStatusList
			err := r.List(ctx, &csList, &client.ListOptions{
				LabelSelector: labels.SelectorFromSet(map[string]string{
					"promoter.argoproj.io/commit-status": utils.KubeSafeLabel(ctx, status.Key),
				}),
				FieldSelector: fields.SelectorFromSet(map[string]string{
					".spec.sha": pc.Status.Proposed.Hydrated.Sha,
				}),
			})
			if err != nil {
				return err
			}

			csListSlice := []promoterv1alpha1.CommitStatus{}
			for _, item := range csList.Items {
				if item.Labels["promoter.argoproj.io/commit-status-copy"] != "true" {
					csListSlice = append(csListSlice, item)
				}
			}

			if len(csListSlice) == 1 {
				allProposdedCSList = append(allProposdedCSList, csListSlice[0])
				//ps.Status.Environments[i].Proposed.CommitStatus.State = string(csList.Items[0].Spec.State)
				//ps.Status.Environments[i].Proposed.CommitStatus.Sha = csList.Items[0].Spec.Sha
			} else if len(csListSlice) > 1 {
				ps.Status.Environments[i].Proposed.CommitStatus.State = "to-many-matching-sha"
				ps.Status.Environments[i].Proposed.CommitStatus.Sha = "to-many-matching-sha"
			} else if len(csListSlice) == 0 {
				ps.Status.Environments[i].Proposed.CommitStatus.State = "no-commit-status-found"
				ps.Status.Environments[i].Proposed.CommitStatus.Sha = "no-commit-status-found"
			}

		}
		if len(allProposdedCSList) == 0 && i >= 0 {
			ps.Status.Environments[i].Proposed.CommitStatus.State = "success"
			ps.Status.Environments[i].Proposed.CommitStatus.Sha = pc.Status.Active.Hydrated.Sha
		} else {
			// Loop through allActiveCSList and bubble up success if all are successful
			ps.Status.Environments[i].Proposed.CommitStatus.State = "success"
			for _, cs := range allActiveCSList {
				if cs.Status.State != "success" {
					ps.Status.Environments[i].Proposed.CommitStatus.State = string(cs.Spec.State)
					ps.Status.Environments[i].Proposed.CommitStatus.Sha = cs.Spec.Sha
					break
				}
			}
		}

	}
	return nil
}

func (r *PromotionStrategyReconciler) copyCommitStatuses(ctx context.Context, csSelector []promoterv1alpha1.CommitStatusSelector, copyFromActiveHydratedSha string, copyToProposedHydratedSha string, branch string) error {
	logger := log.FromContext(ctx)

	for _, value := range csSelector {
		var commitStatuses promoterv1alpha1.CommitStatusList
		err := r.List(ctx, &commitStatuses, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(map[string]string{
				"promoter.argoproj.io/commit-status": utils.KubeSafeLabel(ctx, value.Key),
			}),
			FieldSelector: fields.SelectorFromSet(map[string]string{
				".spec.sha": copyFromActiveHydratedSha,
			}),
		})
		if err != nil {
			return err
		}

		for _, commitStatus := range commitStatuses.Items {
			if commitStatus.Labels["promoter.argoproj.io/commit-status-copy"] == "true" {
				continue
			}

			cs := promoterv1alpha1.CommitStatus{}
			proposedCSObjectKey := client.ObjectKey{Namespace: commitStatus.Namespace, Name: "proposed-" + commitStatus.Name}
			errGet := r.Get(ctx, proposedCSObjectKey, &cs)
			if errGet != nil {
				if errors.IsNotFound(errGet) {
					status := &promoterv1alpha1.CommitStatus{
						ObjectMeta: metav1.ObjectMeta{
							Name:        proposedCSObjectKey.Name,
							Annotations: commitStatus.Annotations,
							Labels:      commitStatus.Labels,
							Namespace:   commitStatus.Namespace,
						},
						Spec: promoterv1alpha1.CommitStatusSpec{
							RepositoryReference: commitStatus.Spec.RepositoryReference,
							Sha:                 copyToProposedHydratedSha,
							Name:                branch + " - " + commitStatus.Spec.Name,
							Description:         commitStatus.Spec.Description,
							State:               commitStatus.Spec.State,
							Url:                 "https://github.com/" + commitStatus.Spec.RepositoryReference.Owner + "/" + commitStatus.Spec.RepositoryReference.Name + "/commit/" + copyFromActiveHydratedSha,
						},
					}
					if status.Labels == nil {
						status.Labels = make(map[string]string)
					}
					status.Labels["promoter.argoproj.io/commit-status-copy"] = "true"
					status.Labels["promoter.argoproj.io/commit-status-copy-from"] = utils.KubeSafeLabel(ctx, commitStatus.Spec.Name)
					status.Labels["promoter.argoproj.io/commit-status-copy-from-sha"] = utils.KubeSafeLabel(ctx, copyFromActiveHydratedSha)
					status.Labels["promoter.argoproj.io/commit-status-copy-from-branch"] = utils.KubeSafeLabel(ctx, branch)
					err := r.Create(ctx, status)
					if err != nil {
						return err
					}
					return nil
				} else {
					logger.Error(errGet, "failed to get CommitStatus", "namespace", proposedCSObjectKey.Namespace, "name", proposedCSObjectKey.Name)
					return errGet
				}
			}
			commitStatus.Spec.DeepCopyInto(&cs.Spec)

			cs.Labels["promoter.argoproj.io/commit-status-copy"] = "true"
			cs.Labels["promoter.argoproj.io/commit-status-copy-from"] = utils.KubeSafeLabel(ctx, commitStatus.Spec.Name)
			cs.Labels["promoter.argoproj.io/commit-status-copy-from-sha"] = utils.KubeSafeLabel(ctx, copyFromActiveHydratedSha)
			cs.Labels["promoter.argoproj.io/commit-status-copy-from-branch"] = utils.KubeSafeLabel(ctx, branch)
			cs.Spec.Sha = copyToProposedHydratedSha
			cs.Spec.Name = branch + " - " + commitStatus.Spec.Name

			cs.Spec.Url = "https://github.com/" + cs.Spec.RepositoryReference.Owner + "/" + cs.Spec.RepositoryReference.Name + "/commit/" + copyFromActiveHydratedSha
			err = r.Update(ctx, &cs)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
