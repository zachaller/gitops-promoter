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

// Package promotionhistory reconstructs promotion history entries from git notes and commit trailers.
package promotionhistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/git"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
)

// ShouldSkipHistoryRecalculation reports whether existing history already fully describes the current
// active hydrated tip. See the controller call site for the full rationale.
func ShouldSkipHistoryRecalculation(history []promoterv1alpha1.History, activeSha string) bool {
	if activeSha == "" || len(history) == 0 {
		return false
	}
	newest := history[0]
	return newest.PullRequest != nil && newest.PullRequest.ID != "" &&
		newest.PullRequest.MergedTargetSha == activeSha &&
		newest.Active.Hydrated.Sha == activeSha
}

// CalculateHistory walks the active branch and reconstructs history entries from git notes and trailers.
// Best effort: errors are logged and processing continues where possible.
func CalculateHistory(ctx context.Context, activeBranch, activePath string, maxEntries int, gitOperations *git.EnvironmentOperations) []promoterv1alpha1.History {
	logger := log.FromContext(ctx)

	shaListActive, err := gitOperations.GetRevListFirstParent(ctx, "origin/"+activeBranch, maxEntries)
	if err != nil {
		logger.V(4).Info("failed to get rev-list commit history for active branch", "branch", activeBranch, "err", err)
		return nil
	}
	logger.V(4).Info("Rev-list history for active branch", "shaList", shaListActive)

	if err := gitOperations.LoadCommitAndMetadataBlobs(ctx, activePath, shaListActive...); err != nil {
		logger.V(4).Info("failed to prefetch history commit objects", "err", err)
		return nil
	}
	if err := gitOperations.LoadHistoryNotes(ctx, shaListActive...); err != nil {
		logger.V(4).Info("failed to prefetch promotion history notes", "err", err)
		return nil
	}

	if !prefetchProposedHistoryCommits(ctx, shaListActive, gitOperations, &calculateTimings{}) {
		return nil
	}

	history := make([]promoterv1alpha1.History, 0, len(shaListActive))
	for _, sha := range shaListActive {
		historyEntry, shouldInclude, err := BuildHistoryEntry(ctx, sha, activePath, gitOperations)
		if err != nil {
			logger.V(4).Info("failed to build history entry", "sha", sha, "err", err)
			continue
		}
		if shouldInclude {
			history = append(history, historyEntry)
		}
	}
	return history
}

// BuildHistoryEntry creates a single history entry for the given SHA.
func BuildHistoryEntry(ctx context.Context, sha, activePath string, gitOperations *git.EnvironmentOperations) (promoterv1alpha1.History, bool, error) {
	activeTrailers, err := trailersForHistorySHA(ctx, sha, gitOperations)
	if err != nil {
		return promoterv1alpha1.History{}, false, err
	}

	historyEntry := promoterv1alpha1.History{
		Proposed:    promoterv1alpha1.CommitBranchStateHistoryProposed{},
		Active:      promoterv1alpha1.CommitBranchState{},
		PullRequest: &promoterv1alpha1.PullRequestCommonStatus{},
	}

	populateActiveMetadata(ctx, &historyEntry, sha, activePath, gitOperations)
	populateProposedMetadata(ctx, &historyEntry, activeTrailers, gitOperations)
	populatePullRequestMetadata(ctx, &historyEntry, activeTrailers)
	populateCommitStatuses(ctx, &historyEntry, activeTrailers)
	historyEntry.MergeCommitSnapshotMismatch = FirstTrailerValue(activeTrailers, constants.TrailerMergeCommitSnapshotMismatch) == "true"
	historyEntry.PullRequest.MergedTargetSha = sha

	return historyEntry, true, nil
}

func trailersForHistorySHA(ctx context.Context, sha string, gitOperations *git.EnvironmentOperations) (map[string][]string, error) {
	logger := log.FromContext(ctx)
	trailers, err := gitOperations.GetHistoryNote(ctx, sha)
	if err != nil {
		logger.V(4).Info("failed to get history note, falling back to commit message trailers", "sha", sha, "err", err)
	}
	if len(trailers) == 0 {
		trailers, err = gitOperations.GetTrailers(ctx, sha)
		if err != nil {
			return nil, fmt.Errorf("failed to get trailers for SHA %q: %w", sha, err)
		}
	}
	return trailers, nil
}

// FirstTrailerValue returns the first value for a given trailer key, or an empty string if not found.
func FirstTrailerValue(trailers map[string][]string, key string) string {
	if values, ok := trailers[key]; ok && len(values) > 0 {
		return values[0]
	}
	return ""
}

// DecodeTrailerDescription decodes a JSON-encoded commit status description from a trailer value.
func DecodeTrailerDescription(ctx context.Context, encoded string) string {
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

// RemoveKnownTrailers strips promoter trailer lines from a commit message body for display.
func RemoveKnownTrailers(input string) string {
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
		constants.TrailerMergeCommitSnapshotMismatch,
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
	return strings.TrimSpace(result)
}

func populateActiveMetadata(ctx context.Context, h *promoterv1alpha1.History, sha, activePath string, gitOperations *git.EnvironmentOperations) {
	logger := log.FromContext(ctx)
	activeHydrated, err := gitOperations.GetShaMetadataFromGit(ctx, sha)
	if err != nil {
		logger.V(4).Info("failed to get active historic metadata from git", "sha", sha, "error", err)
	}
	h.Active.Hydrated = activeHydrated
	h.Active.Hydrated.Body = RemoveKnownTrailers(h.Active.Hydrated.Body)

	activeDry, err := gitOperations.GetShaMetadataFromFile(ctx, sha, activePath)
	if err != nil {
		logger.V(4).Info("failed to get active historic metadata from file", "sha", sha, "error", err)
	}
	h.Active.Dry = activeDry
}

func populateProposedMetadata(ctx context.Context, h *promoterv1alpha1.History, activeTrailers map[string][]string, gitOperations *git.EnvironmentOperations) {
	logger := log.FromContext(ctx)

	proposedHydratedSha := FirstTrailerValue(activeTrailers, constants.TrailerShaHydratedProposed)
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

func populatePullRequestMetadata(ctx context.Context, h *promoterv1alpha1.History, activeTrailers map[string][]string) {
	logger := log.FromContext(ctx)

	if pullRequestID := FirstTrailerValue(activeTrailers, constants.TrailerPullRequestID); pullRequestID != "" {
		h.PullRequest.ID = pullRequestID
	} else {
		logger.V(4).Info("No " + constants.TrailerPullRequestID + " found in trailers")
	}

	if pullRequestUrl := FirstTrailerValue(activeTrailers, constants.TrailerPullRequestUrl); pullRequestUrl != "" {
		if !strings.HasPrefix(pullRequestUrl, "http://") && !strings.HasPrefix(pullRequestUrl, "https://") {
			logger.V(4).Info("pull request URL does not start with http:// or https://", "url", pullRequestUrl)
		} else {
			h.PullRequest.Url = pullRequestUrl
		}
	} else {
		logger.V(4).Info("No " + constants.TrailerPullRequestUrl + " found in trailers")
	}

	if timeStr := FirstTrailerValue(activeTrailers, constants.TrailerPullRequestCreationTime); timeStr != "" {
		if creationTime, err := time.Parse(time.RFC3339, timeStr); err != nil {
			logger.V(4).Info("failed to parse "+constants.TrailerPullRequestCreationTime, "time", timeStr, "err", err)
		} else {
			h.PullRequest.PRCreationTime = metav1.NewTime(creationTime)
		}
	} else {
		logger.V(4).Info("No " + constants.TrailerPullRequestCreationTime + " found in trailers")
	}

	if timeStr := FirstTrailerValue(activeTrailers, constants.TrailerPullRequestMergeTime); timeStr != "" {
		if mergeTime, err := time.Parse(time.RFC3339, timeStr); err != nil {
			logger.V(4).Info("failed to parse "+constants.TrailerPullRequestMergeTime, "time", timeStr, "err", err)
		} else {
			h.PullRequest.PRMergeTime = metav1.NewTime(mergeTime)
		}
	} else {
		logger.V(4).Info("No " + constants.TrailerPullRequestMergeTime + " found in trailers")
	}
}

// PopulateCommitStatuses fills history commit status fields from trailers (exported for tests).
func PopulateCommitStatuses(ctx context.Context, h *promoterv1alpha1.History, activeTrailers map[string][]string) {
	populateCommitStatuses(ctx, h, activeTrailers)
}

func populateCommitStatuses(ctx context.Context, h *promoterv1alpha1.History, activeTrailers map[string][]string) {
	activeKeys, proposedKeys := commitStatusKeysFromTrailers(ctx, activeTrailers)

	h.Active.CommitStatuses = make([]promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase, 0, len(activeKeys))
	for _, key := range activeKeys {
		url := FirstTrailerValue(activeTrailers, constants.TrailerCommitStatusActivePrefix+key+"-url")
		if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			log.FromContext(ctx).Error(errors.New("invalid URL"), "active commit status URL does not start with http:// or https://", "url", url, "key", key)
			url = ""
		}
		h.Active.CommitStatuses = append(h.Active.CommitStatuses, promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
			Key:         key,
			Phase:       FirstTrailerValue(activeTrailers, constants.TrailerCommitStatusActivePrefix+key+"-phase"),
			Url:         url,
			Description: DecodeTrailerDescription(ctx, FirstTrailerValue(activeTrailers, constants.TrailerCommitStatusActivePrefix+key+"-description")),
		})
	}

	h.Proposed.CommitStatuses = make([]promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase, 0, len(proposedKeys))
	for _, key := range proposedKeys {
		url := FirstTrailerValue(activeTrailers, constants.TrailerCommitStatusProposedPrefix+key+"-url")
		if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			log.FromContext(ctx).Error(errors.New("invalid URL"), "proposed commit status URL does not start with http:// or https://", "url", url, "key", key)
			url = ""
		}
		h.Proposed.CommitStatuses = append(h.Proposed.CommitStatuses, promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
			Key:         key,
			Phase:       FirstTrailerValue(activeTrailers, constants.TrailerCommitStatusProposedPrefix+key+"-phase"),
			Url:         url,
			Description: DecodeTrailerDescription(ctx, FirstTrailerValue(activeTrailers, constants.TrailerCommitStatusProposedPrefix+key+"-description")),
		})
	}
}

// CommitStatusKeysFromTrailers extracts commit status keys from promotion trailers (exported for tests).
func CommitStatusKeysFromTrailers(ctx context.Context, trailers map[string][]string) (activeKeys []string, proposedKeys []string) {
	return commitStatusKeysFromTrailers(ctx, trailers)
}

func commitStatusKeysFromTrailers(ctx context.Context, trailers map[string][]string) (activeKeys []string, proposedKeys []string) {
	logger := log.FromContext(ctx)

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
