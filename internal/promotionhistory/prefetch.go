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
	"strings"
	"time"

	"github.com/argoproj-labs/gitops-promoter/internal/git"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
)

func proposedHydratedSHAsFromActiveList(ctx context.Context, shaListActive []string, gitOperations *git.EnvironmentOperations) []string {
	seen := make(map[string]struct{}, len(shaListActive))
	var out []string
	for _, sha := range shaListActive {
		trailers, err := trailersForHistorySHA(ctx, sha, gitOperations)
		if err != nil {
			continue
		}
		proposedSha := strings.ToLower(strings.TrimSpace(FirstTrailerValue(trailers, constants.TrailerShaHydratedProposed)))
		if proposedSha == "" {
			continue
		}
		if _, ok := seen[proposedSha]; ok {
			continue
		}
		seen[proposedSha] = struct{}{}
		out = append(out, proposedSha)
	}
	return out
}

func prefetchProposedHistoryCommits(ctx context.Context, shaListActive []string, gitOperations *git.EnvironmentOperations, timings *calculateTimings) bool {
	t0 := time.Now()
	proposed := proposedHydratedSHAsFromActiveList(ctx, shaListActive, gitOperations)
	timings.proposedCollect = time.Since(t0)
	timings.proposedCount = len(proposed)
	if len(proposed) == 0 {
		return true
	}
	t1 := time.Now()
	if err := gitOperations.FetchCommitsFromOrigin(ctx, proposed...); err != nil {
		timings.proposedFetchError = err.Error()
	}
	timings.proposedFetch = time.Since(t1)
	t2 := time.Now()
	if err := gitOperations.LoadCommits(ctx, proposed...); err != nil {
		timings.proposedLoad = time.Since(t2)
		return false
	}
	timings.proposedLoad = time.Since(t2)
	timings.proposedCached = gitOperations.CachedCommitCount(proposed...)
	return true
}
