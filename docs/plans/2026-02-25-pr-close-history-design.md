# PR Close History via Git Notes

**Date:** 2026-02-25
**Branch:** webrequest-commitstatus-r2
**Status:** Approved

## Problem

When a PR is merged externally via the SCM (e.g. a human merges through the GitHub UI), the promoter
has no record of it in the CTP history. History today is built by reading git trailers embedded in
merge commits — trailers that are only written when the promoter itself performs the merge. Externally
merged PRs produce no trailers, so they silently disappear from history.

## Goals

- Capture history for PRs merged externally via the SCM webhook
- Reuse existing trailer parsing logic for both note and trailer sources
- No new CRDs or controllers
- Backward compatible: promoter-merged PRs continue to use trailers as before

## Out of Scope

- PRs that are closed without merging (no commit lands on the active branch)
- SCMs that do not send PR merge webhooks

## Architecture

Three existing components are extended:

```
GitHub PR merge webhook
        │  (X-Github-Event: pull_request, action: closed, merged: true)
        ▼
WebhookReceiver.postRoot()
  ├── detect PR merge event alongside existing push event handling
  ├── extract pull_request.number + pull_request.merge_commit_sha
  ├── find PullRequest resource by Status.ID (new field index)
  └── patch annotation onto PullRequest resource:
      promoter.argoproj.io/external-merge-commit-sha: <sha>
        │
        ▼ (AnnotationChangedPredicate fires reconcile)
PullRequestReconciler.Reconcile()
  └── writeHistoryNote() called before cleanupTerminalStates()
        ├── read annotation → mergeCommitSHA
        ├── create git.EnvironmentOperations(targetBranch)
        ├── write + push note to refs/notes/promoter.history
        ├── remove annotation
        └── delete PullRequest resource (existing behavior unchanged)
        │
        ▼ (push webhook also fires, CTP reconciles normally)
CTPReconciler.buildHistoryEntry(sha)
  ├── FetchPromoterHistoryNotes()    ← new, fetches refs/notes/promoter.history
  ├── GetPromoterHistoryNote(sha)    ← check note first
  └── fallback: GetTrailers(sha)    ← existing path for promoter-merged PRs
```

## Data

### Annotation

Written by the webhook handler onto the `PullRequest` resource. Read and removed by the PR
controller after writing the git note.

```
promoter.argoproj.io/external-merge-commit-sha: <merge-commit-sha>
```

### Git Notes Ref

A new ref separate from the hydrator's `refs/notes/hydrator.metadata`:

```
refs/notes/promoter.history
```

### Note Content Format

Trailer format — the same format used in merge commit messages — so that `ParseTrailersFromMessage`
and all existing `populate*` functions can be reused without modification.

```
Pull-request-id: 123
Pull-request-url: https://github.com/org/repo/pull/123
Pull-request-creation-time: 2024-01-01T00:00:00Z
Pull-request-merge-time: 2024-01-01T00:00:00Z
Pull-request-source-branch: environment/staging-next
Pull-request-target-branch: environment/staging
```

All keys are existing constants from `internal/types/constants/trailers.go`. Two new constants are
added for source and target branch:

- `TrailerPullRequestSourceBranch = "Pull-request-source-branch"` (already exists)
- `TrailerPullRequestTargetBranch = "Pull-request-target-branch"` (already exists)

`pullRequestMergeTime` is set at the time the PR controller writes the note, consistent with how
`TrailerPullRequestMergeTime` is set today when the promoter merges.

## Component Changes

### 1. `internal/webhookreceiver/server.go`

- Detect PR merge events: `X-Github-Event: pull_request` header + `action: "closed"` + `merged: true`
  in the payload body, alongside the existing push event detection
- Add `findPullRequestByID(ctx, provider, jsonBytes)` — lists `PullRequest` resources filtered by
  a new field index on `.status.id`, matching `pull_request.number` from the payload
- Patch `promoter.argoproj.io/external-merge-commit-sha` annotation onto the found `PullRequest`
  resource via the k8s client; this triggers reconcile via the updated predicate
- On failure to find a matching `PullRequest`: log and return 204 (same behavior as today when no
  CTP is found for a push event)

### 2. `internal/controller/pullrequest_controller.go`

- Register a field index on `.status.id` at setup time
- Update the controller predicate to:
  `predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{})`
- Add `writeHistoryNote(ctx, pr)` helper called at the start of the external-merge cleanup path:
  - Read `promoter.argoproj.io/external-merge-commit-sha` annotation
  - If absent: return (handles reconciles triggered for other reasons)
  - Build trailer-format note content from `pr.Status` fields
  - Create `git.EnvironmentOperations` for `pr.Spec.TargetBranch` using the PR's `GitRepository`
    and `GitOperationsProvider` (same setup as the CTP controller)
  - Call `WritePromoterHistoryNote(ctx, sha, content)`
  - Remove the annotation via patch
- The PR controller gains a dependency on `scms.GitOperationsProvider` (same pattern as the CTP
  controller's provider setup)

### 3. `internal/git/git.go`

New constants and methods, following the same patterns as the existing hydrator notes:

```go
const PromoterHistoryNotesRef = "refs/notes/promoter.history"

// FetchPromoterHistoryNotes fetches refs/notes/promoter.history from the remote.
func (g *EnvironmentOperations) FetchPromoterHistoryNotes(ctx context.Context) error

// WritePromoterHistoryNote writes and pushes a note in trailer format to the merge commit SHA.
func (g *EnvironmentOperations) WritePromoterHistoryNote(ctx context.Context, sha, content string) error

// GetPromoterHistoryNote reads the note for a given SHA and returns parsed trailers.
// Returns an empty map if no note exists.
func (g *EnvironmentOperations) GetPromoterHistoryNote(ctx context.Context, sha string) (map[string][]string, error)
```

`GetPromoterHistoryNote` calls `ParseTrailersFromMessage` on the raw note content — the same parser
used for commit message trailers.

### 4. `internal/controller/changetransferpolicy_controller.go`

- After existing `FetchNotes`, also call `FetchPromoterHistoryNotes`
- Replace the trailer-only lookup in `buildHistoryEntry` with note-first fallback:

```go
trailers, _ := gitOperations.GetPromoterHistoryNote(ctx, sha)
if len(trailers) == 0 {
    trailers, _ = gitOperations.GetTrailers(ctx, sha)
}
// existing populate functions unchanged
r.populateActiveMetadata(ctx, &historyEntry, sha, gitOperations)
r.populateProposedMetadata(ctx, &historyEntry, trailers, gitOperations)
r.populatePullRequestMetadata(ctx, &historyEntry, trailers)
r.populateCommitStatuses(ctx, &historyEntry, trailers)
```

## Error Handling

- **Note write failure**: log the error, remove the annotation, and proceed with PR cleanup. History
  is best-effort (consistent with the existing doc comment: *"History is constructed on a best-effort
  basis and should be used for informational purposes only"*).
- **Annotation absent at reconcile time**: `writeHistoryNote` is a no-op; PR cleanup proceeds
  normally. Handles reconciles triggered for reasons other than the webhook annotation.
- **Webhook cannot find PullRequest by ID**: log and return 204. The PR controller will still clean
  up the resource via its normal polling path; the history entry will simply be missing for that PR.
- **Note fetch failure in CTP**: log and skip notes for this reconcile cycle; the trailers fallback
  still works for promoter-merged PRs.
- **Controller restart between annotation write and reconcile**: the annotation persists in
  Kubernetes; the next reconcile triggered by any means will find it and write the note.

## Testing

- **Webhook unit tests**: PR merge payload detection; annotation patch; graceful handling when no
  `PullRequest` resource matches the PR ID
- **PR controller unit tests**: note written when annotation present; annotation removed after note
  write; cleanup proceeds normally when annotation absent; note write failure does not block cleanup
- **Git unit tests**: `WritePromoterHistoryNote` / `GetPromoterHistoryNote` round-trip; trailer
  format parse correctness
- **CTP controller unit tests**: history populated from note when present; trailer fallback when
  note absent; both sources produce equivalent `History` struct output
- **Integration test** (`suite_test.go`): simulate external PR merge via webhook annotation →
  verify CTP `status.history` reflects the PR metadata from the note
