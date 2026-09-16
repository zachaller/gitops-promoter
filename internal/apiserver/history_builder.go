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

package apiserver

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	viewv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/view/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/git"
	"github.com/argoproj-labs/gitops-promoter/internal/gitauth"
	"github.com/argoproj-labs/gitops-promoter/internal/promotionhistory"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

var historyUIDNamespace = uuid.MustParse("a8e3c1f2-4b5d-6e7f-8091-a2b3c4d5e6f7")

func historyUID(psUID types.UID) types.UID {
	return types.UID(uuid.NewSHA1(historyUIDNamespace, []byte(psUID)).String())
}

func historyShellFromPS(ps *promoterv1alpha1.PromotionStrategy, resourceVersion string) *viewv1alpha1.PromotionStrategyHistory {
	envs := make([]viewv1alpha1.EnvironmentHistory, 0, len(ps.Spec.Environments))
	for _, e := range ps.Spec.Environments {
		envs = append(envs, viewv1alpha1.EnvironmentHistory{Branch: e.Branch})
	}
	return &viewv1alpha1.PromotionStrategyHistory{
		Name:              ps.Name,
		Namespace:         ps.Namespace,
		UID:               historyUID(ps.UID),
		ResourceVersion:   resourceVersion,
		CreationTimestamp: ps.CreationTimestamp,
		Labels:            ps.Labels,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: promoterv1alpha1.SchemeGroupVersion.String(),
			Kind:       "PromotionStrategy",
			Name:       ps.Name,
			UID:        ps.UID,
		}},
		Environments: envs,
	}
}

func buildHistoryShell(ctx context.Context, reader client.Reader, namespace, name, resourceVersion string) (*viewv1alpha1.PromotionStrategyHistory, error) {
	ps := &promoterv1alpha1.PromotionStrategy{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, ps); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, apierrors.NewNotFound(viewv1alpha1.Resource("promotionstrategyhistories"), name)
		}
		return nil, fmt.Errorf("failed to get PromotionStrategy %s/%s: %w", namespace, name, err)
	}
	return historyShellFromPS(ps, resourceVersion), nil
}

func buildHistory(
	ctx context.Context,
	reader client.Client,
	controllerNamespace string,
	namespace, name, resourceVersion string,
	maxEntries int,
	entryCache *promotionhistory.EntryCache,
	envStore *promotionhistory.EnvStateStore,
) (*viewv1alpha1.PromotionStrategyHistory, error) {
	ps := &promoterv1alpha1.PromotionStrategy{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, ps); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, apierrors.NewNotFound(viewv1alpha1.Resource("promotionstrategyhistories"), name)
		}
		return nil, fmt.Errorf("failed to get PromotionStrategy %s/%s: %w", namespace, name, err)
	}

	out := historyShellFromPS(ps, resourceVersion)

	ctpList := &promoterv1alpha1.ChangeTransferPolicyList{}
	if err := reader.List(ctx, ctpList, client.InNamespace(namespace),
		client.MatchingLabels{promoterv1alpha1.PromotionStrategyLabel: name}); err != nil {
		return nil, fmt.Errorf("failed to list ChangeTransferPolicies: %w", err)
	}

	byBranch := map[string]*promoterv1alpha1.ChangeTransferPolicy{}
	for i := range ctpList.Items {
		ctp := &ctpList.Items[i]
		byBranch[ctp.Spec.ActiveBranch] = ctp
	}

	g, gctx := errgroup.WithContext(ctx)
	for i := range out.Environments {
		i := i
		branch := out.Environments[i].Branch
		if branch == "" && i < len(ps.Spec.Environments) {
			branch = ps.Spec.Environments[i].Branch
			out.Environments[i].Branch = branch
		}
		ctp := byBranch[branch]
		if ctp == nil {
			continue
		}
		g.Go(func() error {
			activeBranch := ctp.Spec.ActiveBranch
			hist, err := historyForCTP(gctx, reader, controllerNamespace, name, ctp, maxEntries, entryCache, envStore)
			if err != nil {
				log.Error(err, "failed to build history for CTP", "namespace", ctp.Namespace, "name", ctp.Name)
				return nil
			}
			out.Environments[i].Branch = activeBranch
			out.Environments[i].History = hist
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return out, nil
}

func historyForCTP(
	ctx context.Context,
	reader client.Client,
	controllerNamespace string,
	promotionStrategyName string,
	ctp *promoterv1alpha1.ChangeTransferPolicy,
	maxEntries int,
	entryCache *promotionhistory.EntryCache,
	envStore *promotionhistory.EnvStateStore,
) ([]promoterv1alpha1.History, error) {
	overall := time.Now()

	gitOps, err := gitOperationsForCTP(ctx, reader, controllerNamespace, ctp)
	if err != nil {
		return nil, err
	}
	reusedClone := gitOps.ClonePath() != ""

	tClone := time.Now()
	if err := gitOps.CloneRepoForHistory(ctx, ctp.Spec.ActiveBranch, maxEntries); err != nil {
		return nil, fmt.Errorf("clone repo: %w", err)
	}
	cloneDur := time.Since(tClone)

	tNotes := time.Now()
	if err := gitOps.FetchNotes(ctx); err != nil {
		return nil, fmt.Errorf("fetch notes: %w", err)
	}
	notesDur := time.Since(tNotes)

	tBranch := time.Now()
	if _, err := gitOps.GetBranchSha(ctx, ctp.Spec.ActiveBranch, ctp.Status.Active.Hydrated.Sha); err != nil {
		return nil, fmt.Errorf("fetch active branch: %w", err)
	}
	branchDur := time.Since(tBranch)

	ctpKey := ctp.Namespace + "/" + ctp.Name
	tCalc := time.Now()
	calcCtx := crlog.IntoContext(ctx, historyLog)
	hist := promotionhistory.CalculateHistoryIncremental(calcCtx, ctpKey, ctp.Spec.ActiveBranch, ctp.Spec.ActivePath, maxEntries, gitOps, entryCache, envStore)
	calcDur := time.Since(tCalc)

	historyLog.Info("PromotionStrategyHistory environment built",
		"promotionStrategy", promotionStrategyName,
		"namespace", ctp.Namespace,
		"changeTransferPolicy", ctp.Name,
		"branch", ctp.Spec.ActiveBranch,
		"duration", durationField(time.Since(overall)),
		"clone", durationField(cloneDur),
		"fetchNotes", durationField(notesDur),
		"fetchActiveBranch", durationField(branchDur),
		"calculateHistory", durationField(calcDur),
		"historyEntries", len(hist),
		"reusedClone", reusedClone,
	)
	return hist, nil
}

func gitOperationsForCTP(ctx context.Context, reader client.Client, controllerNamespace string, ctp *promoterv1alpha1.ChangeTransferPolicy) (*git.EnvironmentOperations, error) {
	scmProvider, secret, gitRepo, err := utils.GetScmProviderSecretAndGitRepositoryFromRepositoryReference(
		ctx, reader, controllerNamespace, ctp.Spec.RepositoryReference, ctp)
	if err != nil {
		return nil, err
	}
	gitAuthProvider, err := gitauth.CreateGitOperationsProvider(ctx, reader, scmProvider, secret, client.ObjectKey{Namespace: ctp.Namespace, Name: ctp.Spec.RepositoryReference.Name})
	if err != nil {
		return nil, fmt.Errorf("git auth provider: %w", err)
	}
	return git.NewEnvironmentOperations(gitRepo, gitAuthProvider, ctp.Namespace+"/"+ctp.Name), nil
}
