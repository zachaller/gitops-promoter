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

package git

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/argoproj-labs/gitops-promoter/internal/metrics"
	"github.com/argoproj-labs/gitops-promoter/internal/utils/gitpaths"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// HistoryCloneDepthSlack is extra first-parent history beyond maxHistoryEntries when shallow-cloning for promotion history.
const HistoryCloneDepthSlack = 5

// HistoryCloneDepth returns the --depth value for a history shallow clone.
func HistoryCloneDepth(maxHistoryEntries int) int {
	if maxHistoryEntries < 1 {
		maxHistoryEntries = 1
	}
	return maxHistoryEntries + HistoryCloneDepthSlack
}

// CloneRepoForHistory shallow-clones only the active branch with a bounded depth and includes blobs
// for objects in that window (no partial-clone filter). That keeps hydrator.metadata and similar
// small files local for history prefetch. Intended for read-only promotion history in the dashboard
// apiserver; the controller should keep using CloneRepo with --filter=blob:none.
func (g *EnvironmentOperations) CloneRepoForHistory(ctx context.Context, activeBranch string, maxHistoryEntries int) error {
	if g.ClonePath() != "" {
		return nil
	}
	if activeBranch == "" {
		return fmt.Errorf("active branch is required for history clone")
	}

	depth := HistoryCloneDepth(maxHistoryEntries)
	g.branchFetchDepth = depth

	logger := log.FromContext(ctx)

	path, err := os.MkdirTemp("", "*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	logger.V(4).Info("Created directory for history shallow clone", "directory", path, "branch", activeBranch, "depth", depth)

	repoURL := g.gap.GetGitHttpsRepoUrl(*g.gitRepo)
	start := time.Now()
	stdout, stderr, err := g.runCmd(ctx, path, "clone",
		"--single-branch",
		"--branch", activeBranch,
		"--depth", fmt.Sprintf("%d", depth),
		repoURL,
		path,
	)
	metrics.RecordGitOperation(g.gitRepo, metrics.GitOperationClone, metrics.GitOperationResultFromError(err), time.Since(start))
	if err != nil {
		logger.Error(err, "history shallow clone failed", "repo", repoURL, "branch", activeBranch, "depth", depth, "stdout", stdout, "stderr", stderr)
		return err
	}

	for _, cfg := range [][2]string{
		{"pull.rebase", "false"},
		{"user.name", "GitOps Promoter"},
		{"user.email", "GitOpsPromoter@argoproj.io"},
	} {
		stdout, stderr, err = g.runCmd(ctx, path, "config", cfg[0], cfg[1])
		if err != nil {
			logger.Error(err, "could not set git config", "stdout", stdout, "stderr", stderr)
			return err
		}
	}

	logger.V(4).Info("History shallow clone successful", "repo", repoURL, "branch", activeBranch, "depth", depth, "identity", g.identity)
	gitpaths.Set(g.cloneKey(), path)
	return nil
}
