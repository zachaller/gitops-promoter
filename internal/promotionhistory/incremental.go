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

package promotionhistory

import (
	"context"
	"slices"
	"time"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/git"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// CalculateHistoryIncremental rebuilds history using a bounded entry cache and per-CTP state.
func CalculateHistoryIncremental(
	ctx context.Context,
	ctpKey, activeBranch, activePath string,
	maxEntries int,
	gitOperations *git.EnvironmentOperations,
	entryCache *EntryCache,
	envStore *EnvStateStore,
) []promoterv1alpha1.History {
	logger := log.FromContext(ctx)

	tRev := time.Now()
	shaListActive, err := gitOperations.GetRevListFirstParent(ctx, "origin/"+activeBranch, maxEntries)
	if err != nil {
		logger.V(4).Info("failed to get rev-list commit history for active branch", "branch", activeBranch, "err", err)
		return nil
	}
	var timings calculateTimings
	timings.revList = time.Since(tRev)

	prev := envStore.Get(ctpKey)
	if slices.Equal(shaListActive, prev.LastSHAs) && len(prev.LastResult) > 0 {
		return prev.LastResult
	}

	t0 := time.Now()
	if err := gitOperations.LoadCommitAndMetadataBlobs(ctx, activePath, shaListActive...); err != nil {
		logger.V(4).Info("failed to prefetch history commit objects", "err", err)
		return nil
	}
	timings.prefetchActive = time.Since(t0)

	t1 := time.Now()
	if err := gitOperations.LoadHistoryNotes(ctx, shaListActive...); err != nil {
		logger.V(4).Info("failed to prefetch promotion history notes", "err", err)
		return nil
	}
	timings.prefetchNotes = time.Since(t1)

	if !prefetchProposedHistoryCommits(ctx, shaListActive, gitOperations, &timings) {
		return nil
	}

	overlap := shaListsOverlap(prev.LastSHAs, shaListActive)
	history := make([]promoterv1alpha1.History, 0, len(shaListActive))
	t2 := time.Now()
	for _, sha := range shaListActive {
		if overlap {
			if cached, ok := entryCache.Get(sha); ok {
				history = append(history, cached)
				continue
			}
		}
		historyEntry, shouldInclude, err := BuildHistoryEntry(ctx, sha, activePath, gitOperations)
		if err != nil {
			logger.V(4).Info("failed to build history entry", "sha", sha, "err", err)
			continue
		}
		if shouldInclude {
			entryCache.Put(sha, historyEntry)
			history = append(history, historyEntry)
		}
	}
	timings.buildEntries = time.Since(t2)
	timings.log(ctx)

	tip := ""
	if len(shaListActive) > 0 {
		tip = shaListActive[0]
	}
	envStore.Set(ctpKey, &EnvState{
		LastTipSha: tip,
		LastSHAs:   shaListActive,
		LastResult: history,
	})
	return history
}

func shaListsOverlap(prev, next []string) bool {
	if len(prev) == 0 || len(next) == 0 {
		return false
	}
	for _, sha := range next {
		if slices.Contains(prev, sha) {
			return true
		}
	}
	return false
}
