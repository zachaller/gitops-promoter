# PR Close History via Git Notes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Capture PR merge history when PRs are merged externally via the SCM by writing git notes in trailer format, and update the CTP history lookup to check notes before falling back to git trailers.

**Architecture:** The webhook receiver detects GitHub PR merge events and patches an annotation (`promoter.argoproj.io/external-merge-commit-sha`) onto the matching `PullRequest` resource. The `AnnotationChangedPredicate` fires a reconcile; the PR controller reads the annotation, writes a git note in trailer format to `refs/notes/promoter.history`, then cleans up. The CTP controller's `buildHistoryEntry` tries the note first, falls back to trailers — same `map[string][]string` type, same populate functions.

**Tech Stack:** Go, controller-runtime, Ginkgo/Gomega tests, `git notes`, `git interpret-trailers`, Kubernetes annotations

**Design doc:** `docs/plans/2026-02-25-pr-close-history-design.md`

---

## Task 1: Git layer — promoter history notes

**Files:**
- Modify: `internal/git/git.go`
- Modify: `internal/git/git_test.go`

The existing hydrator notes (`refs/notes/hydrator.metadata`) use identical patterns. Mirror them exactly. `GetPromoterHistoryNote` reuses the existing `ParseTrailersFromMessage` function.

**Step 1: Write the failing tests**

Add to `internal/git/git_test.go`, inside a new `Describe("PromoterHistoryNotes", ...)` block. The test sets up a bare remote + working clone using the same pattern as `GetBranchShas` tests already in the file.

```go
var _ = Describe("PromoterHistoryNotes", func() {
    var (
        remoteDir  string
        workDir    string
        gitPath    string
        commitSha  string
        ops        *git.EnvironmentOperations
    )

    BeforeEach(func() {
        var err error

        // bare remote
        remoteDir, err = os.MkdirTemp("", "remote-*")
        Expect(err).NotTo(HaveOccurred())
        _, err = runGitCmd(remoteDir, "init", "--bare")
        Expect(err).NotTo(HaveOccurred())

        // working clone
        workDir, err = os.MkdirTemp("", "work-*")
        Expect(err).NotTo(HaveOccurred())
        _, err = runGitCmd(workDir, "clone", remoteDir, workDir)
        Expect(err).NotTo(HaveOccurred())
        _, err = runGitCmd(workDir, "config", "user.email", "test@test.com")
        Expect(err).NotTo(HaveOccurred())
        _, err = runGitCmd(workDir, "config", "user.name", "Test")
        Expect(err).NotTo(HaveOccurred())

        // initial commit
        _, err = runGitCmd(workDir, "commit", "--allow-empty", "-m", "initial")
        Expect(err).NotTo(HaveOccurred())
        _, err = runGitCmd(workDir, "push", "origin", "HEAD:main")
        Expect(err).NotTo(HaveOccurred())
        out, err := runGitCmd(workDir, "rev-parse", "HEAD")
        Expect(err).NotTo(HaveOccurred())
        commitSha = strings.TrimSpace(out)

        // register the working clone path the way EnvironmentOperations expects it
        gitpaths.Set(remoteDir+"main", workDir)

        fakeProvider := &fakeGitOpsProvider{repoURL: remoteDir}
        gitRepo := &v1alpha1.GitRepository{}
        ops = git.NewEnvironmentOperations(gitRepo, fakeProvider, "main")
    })

    AfterEach(func() {
        os.RemoveAll(remoteDir)
        os.RemoveAll(workDir)
        gitpaths.Delete(remoteDir + "main")
    })

    It("returns empty map when no note exists", func() {
        trailers, err := ops.GetPromoterHistoryNote(context.Background(), commitSha)
        Expect(err).NotTo(HaveOccurred())
        Expect(trailers).To(BeEmpty())
    })

    It("round-trips a note written in trailer format", func() {
        content := "Pull-request-id: 42\nPull-request-url: https://github.com/org/repo/pull/42\n"
        err := ops.WritePromoterHistoryNote(context.Background(), commitSha, content)
        Expect(err).NotTo(HaveOccurred())

        trailers, err := ops.GetPromoterHistoryNote(context.Background(), commitSha)
        Expect(err).NotTo(HaveOccurred())
        Expect(trailers).To(HaveKeyWithValue("Pull-request-id", ContainElement("42")))
        Expect(trailers).To(HaveKeyWithValue("Pull-request-url", ContainElement("https://github.com/org/repo/pull/42")))
    })

    It("FetchPromoterHistoryNotes succeeds when the ref does not exist yet", func() {
        err := ops.FetchPromoterHistoryNotes(context.Background())
        Expect(err).NotTo(HaveOccurred())
    })
})
```

Note: `fakeGitOpsProvider` and `gitpaths.Delete` — check how existing tests in `internal/git/git_test.go` mock the provider; use the same approach. `gitpaths` is `internal/utils/gitpaths`. If `gitpaths.Delete` doesn't exist, add it (it's just `delete(m, key)` on the underlying map).

**Step 2: Run tests to confirm they fail**

```bash
go test ./internal/git/... -v -count=1 -run "PromoterHistoryNotes"
```

Expected: FAIL — `ops.GetPromoterHistoryNote undefined`

**Step 3: Implement**

Add to `internal/git/git.go`:

```go
// PromoterHistoryNotesRef is the git notes ref used to store PR merge history
// for externally merged pull requests.
const PromoterHistoryNotesRef = "refs/notes/promoter.history"

// FetchPromoterHistoryNotes fetches refs/notes/promoter.history from the remote.
// If the ref does not exist yet, it returns nil (not an error).
func (g *EnvironmentOperations) FetchPromoterHistoryNotes(ctx context.Context) error {
	logger := log.FromContext(ctx)
	gitPath := gitpaths.Get(g.gap.GetGitHttpsRepoUrl(*g.gitRepo) + g.activeBranch)
	if gitPath == "" {
		return fmt.Errorf("no repo path found for repo %q", g.gitRepo.Name)
	}

	start := time.Now()
	_, stderr, err := g.runCmd(ctx, gitPath, "fetch", "origin", "+"+PromoterHistoryNotesRef+":"+PromoterHistoryNotesRef)
	if err != nil {
		if strings.Contains(stderr, "couldn't find remote ref") {
			metrics.RecordGitOperation(g.gitRepo, metrics.GitOperationFetchNotes, metrics.GitOperationResultSuccess, time.Since(start))
			logger.V(4).Info("Promoter history notes ref does not exist on remote yet", "ref", PromoterHistoryNotesRef)
			return nil
		}
		metrics.RecordGitOperation(g.gitRepo, metrics.GitOperationFetchNotes, metrics.GitOperationResultFailure, time.Since(start))
		return fmt.Errorf("failed to fetch promoter history notes: %w", err)
	}
	metrics.RecordGitOperation(g.gitRepo, metrics.GitOperationFetchNotes, metrics.GitOperationResultSuccess, time.Since(start))
	logger.V(4).Info("Fetched promoter history notes", "ref", PromoterHistoryNotesRef)
	return nil
}

// GetPromoterHistoryNote reads the promoter history git note for a given commit SHA
// and returns the parsed trailers. Returns an empty map if no note exists.
func (g *EnvironmentOperations) GetPromoterHistoryNote(ctx context.Context, sha string) (map[string][]string, error) {
	logger := log.FromContext(ctx)
	gitPath := gitpaths.Get(g.gap.GetGitHttpsRepoUrl(*g.gitRepo) + g.activeBranch)
	if gitPath == "" {
		return nil, fmt.Errorf("no repo path found for repo %q", g.gitRepo.Name)
	}

	stdout, stderr, err := g.runCmd(ctx, gitPath, "notes", "--ref="+PromoterHistoryNotesRef, "show", sha)
	if err != nil {
		if strings.Contains(strings.ToLower(stderr), "no note found") {
			logger.V(4).Info("No promoter history note found for commit", "sha", sha)
			return map[string][]string{}, nil
		}
		return nil, fmt.Errorf("failed to read promoter history note for sha %q: %w", sha, err)
	}

	trailers, err := ParseTrailersFromMessage(ctx, strings.TrimSpace(stdout))
	if err != nil {
		logger.V(4).Info("Failed to parse promoter history note as trailers, ignoring", "sha", sha, "error", err)
		return map[string][]string{}, nil
	}
	logger.V(4).Info("Got promoter history note", "sha", sha, "trailers", trailers)
	return trailers, nil
}

// WritePromoterHistoryNote writes content (in trailer format) as a git note on the given
// commit SHA and pushes it to the remote.
func (g *EnvironmentOperations) WritePromoterHistoryNote(ctx context.Context, sha, content string) error {
	logger := log.FromContext(ctx)
	gitPath := gitpaths.Get(g.gap.GetGitHttpsRepoUrl(*g.gitRepo) + g.activeBranch)
	if gitPath == "" {
		return fmt.Errorf("no repo path found for repo %q", g.gitRepo.Name)
	}

	_, stderr, err := g.runCmd(ctx, gitPath, "notes", "--ref="+PromoterHistoryNotesRef, "add", "-f", "-m", content, sha)
	if err != nil {
		return fmt.Errorf("failed to add promoter history note for sha %q: %w (stderr: %s)", sha, err, stderr)
	}

	_, stderr, err = g.runCmd(ctx, gitPath, "push", "origin", PromoterHistoryNotesRef+":"+PromoterHistoryNotesRef)
	if err != nil {
		return fmt.Errorf("failed to push promoter history notes: %w (stderr: %s)", err, stderr)
	}

	logger.V(4).Info("Wrote and pushed promoter history note", "sha", sha)
	return nil
}
```

**Step 4: Run tests to confirm they pass**

```bash
go test ./internal/git/... -v -count=1 -run "PromoterHistoryNotes"
```

Expected: PASS

**Step 5: Commit**

```bash
git add internal/git/git.go internal/git/git_test.go
git commit -m "feat(git): add promoter history notes read/write/fetch functions"
```

---

## Task 2: CTP controller — note-first history lookup

**Files:**
- Modify: `internal/controller/changetransferpolicy_controller.go:149-153` (fetch) and `226-244` (buildHistoryEntry)
- Modify: `internal/controller/changetransferpolicy_controller_test.go`

**Step 1: Write the failing test**

Find the existing `buildHistoryEntry` tests in `changetransferpolicy_controller_test.go`. Add a new case that places a promoter history note on the SHA (using `git notes --ref=refs/notes/promoter.history add`) and verifies the history is populated from the note, not from trailers.

```go
It("populates history from promoter history note when present", func() {
    By("pushing a git note in trailer format onto the active SHA")
    noteContent := fmt.Sprintf(
        "Pull-request-id: 99\nPull-request-url: https://github.com/org/repo/pull/99\n"+
        "Pull-request-creation-time: %s\nPull-request-merge-time: %s\n",
        time.Now().Add(-10*time.Minute).Format(time.RFC3339),
        time.Now().Format(time.RFC3339),
    )
    _, err := runGitCmd(ctx, gitPath, "notes", "--ref=refs/notes/promoter.history", "add", "-f", "-m", noteContent, activeSha)
    Expect(err).NotTo(HaveOccurred())
    _, err = runGitCmd(ctx, gitPath, "push", "origin", "refs/notes/promoter.history:refs/notes/promoter.history")
    Expect(err).NotTo(HaveOccurred())

    By("reconciling the CTP")
    // trigger reconcile and wait for status update as per existing test patterns

    By("verifying history is populated from the note")
    Eventually(func(g Gomega) {
        var ctp promoterv1alpha1.ChangeTransferPolicy
        g.Expect(k8sClient.Get(ctx, ctpKey, &ctp)).To(Succeed())
        g.Expect(ctp.Status.History).NotTo(BeEmpty())
        g.Expect(ctp.Status.History[0].PullRequest).NotTo(BeNil())
        g.Expect(ctp.Status.History[0].PullRequest.ID).To(Equal("99"))
    }, timeout, interval).Should(Succeed())
})
```

Adapt field names and test helpers to match the pattern used in existing CTP controller tests (look at how other `buildHistoryEntry` tests in that file set up repos and trigger reconciles).

**Step 2: Run test to confirm it fails**

```bash
go test ./internal/controller/... -v -count=1 -run "populates history from promoter history note"
```

Expected: FAIL — history is empty because note path is not yet implemented

**Step 3: Implement**

In `changetransferpolicy_controller.go`, after `FetchNotes` (line ~150):

```go
// Fetch promoter history notes for externally merged PR history
if err = gitOperations.FetchPromoterHistoryNotes(ctx); err != nil {
    logger.V(4).Info("failed to fetch promoter history notes, skipping", "error", err)
    // non-fatal: history is best-effort
}
```

In `buildHistoryEntry` (line ~227), replace the existing `GetTrailers` call:

```go
func (r *ChangeTransferPolicyReconciler) buildHistoryEntry(ctx context.Context, sha string, gitOperations *git.EnvironmentOperations) (promoterv1alpha1.History, bool, error) {
    // Check promoter history note first (externally merged PRs), fall back to commit trailers (promoter-merged PRs)
    activeTrailers, err := gitOperations.GetPromoterHistoryNote(ctx, sha)
    if err != nil {
        return promoterv1alpha1.History{}, false, fmt.Errorf("failed to get promoter history note for SHA %q: %w", sha, err)
    }
    if len(activeTrailers) == 0 {
        activeTrailers, err = gitOperations.GetTrailers(ctx, sha)
        if err != nil {
            return promoterv1alpha1.History{}, false, fmt.Errorf("failed to get trailers for SHA %q: %w", sha, err)
        }
    }

    historyEntry := promoterv1alpha1.History{
        Proposed:    promoterv1alpha1.CommitBranchStateHistoryProposed{},
        Active:      promoterv1alpha1.CommitBranchState{},
        PullRequest: &promoterv1alpha1.PullRequestCommonStatus{},
    }

    r.populateActiveMetadata(ctx, &historyEntry, sha, gitOperations)
    r.populateProposedMetadata(ctx, &historyEntry, activeTrailers, gitOperations)
    r.populatePullRequestMetadata(ctx, &historyEntry, activeTrailers)
    r.populateCommitStatuses(ctx, &historyEntry, activeTrailers)

    return historyEntry, true, nil
}
```

**Step 4: Run tests to confirm they pass**

```bash
go test ./internal/controller/... -v -count=1 -run "populates history from promoter history note"
```

Expected: PASS

**Step 5: Confirm existing CTP tests still pass**

```bash
go test ./internal/controller/... -v -count=1 -run "ChangeTransferPolicy"
```

Expected: all PASS (trailer fallback unchanged)

**Step 6: Commit**

```bash
git add internal/controller/changetransferpolicy_controller.go internal/controller/changetransferpolicy_controller_test.go
git commit -m "feat(ctp): check promoter history notes before trailers in buildHistoryEntry"
```

---

## Task 3: PR controller — annotation-based note writing

**Files:**
- Modify: `internal/controller/pullrequest_controller.go`
- Modify: `internal/controller/pullrequest_controller_test.go`

The PR controller currently only uses `PullRequestProvider`. This task adds git operations so it can write the history note. See `changetransferpolicy_controller.go:126-139` for the exact pattern for obtaining a `GitOperationsProvider` and creating `EnvironmentOperations`.

The annotation key is:
```
promoter.argoproj.io/external-merge-commit-sha
```

**Step 1: Write the failing tests**

Add to `pullrequest_controller_test.go`:

```go
It("writes a promoter history note and removes annotation when external-merge-commit-sha annotation is present", func() {
    By("creating a PullRequest with the annotation set")
    pr := buildTestPullRequest() // use existing test helper
    pr.Status.ID = "77"
    pr.Status.Url = "https://github.com/org/repo/pull/77"
    pr.Status.ExternallyMergedOrClosed = ptr.To(true)
    pr.Annotations = map[string]string{
        "promoter.argoproj.io/external-merge-commit-sha": mergeCommitSha,
    }
    Expect(k8sClient.Create(ctx, &pr)).To(Succeed())

    By("verifying the annotation is removed after reconcile")
    Eventually(func(g Gomega) {
        var updated promoterv1alpha1.PullRequest
        err := k8sClient.Get(ctx, client.ObjectKeyFromObject(&pr), &updated)
        // PR will be deleted; either it's gone or annotation is cleared
        if errors.IsNotFound(err) {
            return // success: cleaned up
        }
        g.Expect(updated.Annotations).NotTo(HaveKey("promoter.argoproj.io/external-merge-commit-sha"))
    }, timeout, interval).Should(Succeed())

    By("verifying the git note was written on the merge commit")
    // Read note directly via git command on the test repo
    out, err := runGitCmd(ctx, gitPath, "notes", "--ref=refs/notes/promoter.history", "show", mergeCommitSha)
    Expect(err).NotTo(HaveOccurred())
    Expect(out).To(ContainSubstring("Pull-request-id: 77"))
})

It("proceeds with cleanup normally when annotation is absent", func() {
    pr := buildTestPullRequest()
    pr.Status.ExternallyMergedOrClosed = ptr.To(true)
    // no annotation
    Expect(k8sClient.Create(ctx, &pr)).To(Succeed())

    Eventually(func(g Gomega) {
        var updated promoterv1alpha1.PullRequest
        err := k8sClient.Get(ctx, client.ObjectKeyFromObject(&pr), &updated)
        g.Expect(errors.IsNotFound(err)).To(BeTrue())
    }, timeout, interval).Should(Succeed())
})
```

**Step 2: Run tests to confirm they fail**

```bash
go test ./internal/controller/... -v -count=1 -run "writes a promoter history note"
```

Expected: FAIL

**Step 3: Implement — field index**

In `SetupWithManager` in `pullrequest_controller.go`, register a field index on `.status.id`. Follow the same pattern as existing field indices — search for `IndexField` calls in `main.go` or `suite_test.go` to see how others are registered:

```go
if err := mgr.GetFieldIndexer().IndexField(ctx, &promoterv1alpha1.PullRequest{}, ".status.id", func(obj client.Object) []string {
    pr := obj.(*promoterv1alpha1.PullRequest)
    if pr.Status.ID == "" {
        return nil
    }
    return []string{pr.Status.ID}
}); err != nil {
    return fmt.Errorf("failed to index PullRequest by status.id: %w", err)
}
```

**Step 4: Implement — updated predicate**

In `SetupWithManager`, change:
```go
For(&promoterv1alpha1.PullRequest{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
```
to:
```go
For(&promoterv1alpha1.PullRequest{}, builder.WithPredicates(
    predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}),
)).
```

**Step 5: Implement — `writeHistoryNote` helper**

Add the annotation key constant near the top of `pullrequest_controller.go`:

```go
const ExternalMergeCommitSHAAnnotation = "promoter.argoproj.io/external-merge-commit-sha"
```

Add the helper function:

```go
// writeHistoryNote reads the external-merge-commit-sha annotation, writes a promoter
// history git note in trailer format, and removes the annotation. It is a no-op if the
// annotation is absent. Errors are logged but do not block PR cleanup (history is best-effort).
func (r *PullRequestReconciler) writeHistoryNote(ctx context.Context, pr *promoterv1alpha1.PullRequest) {
    logger := log.FromContext(ctx)

    mergeCommitSHA, ok := pr.Annotations[ExternalMergeCommitSHAAnnotation]
    if !ok || mergeCommitSHA == "" {
        return
    }

    scmProvider, secret, err := utils.GetScmProviderAndSecretFromRepositoryReference(
        ctx, r.Client, r.SettingsMgr.GetControllerNamespace(), pr.Spec.RepositoryReference, pr)
    if err != nil {
        logger.Error(err, "failed to get SCM provider for history note, skipping")
        r.removeHistoryNoteAnnotation(ctx, pr)
        return
    }

    gitAuthProvider, err := gitauth.CreateGitOperationsProvider(
        ctx, r.Client, scmProvider, secret,
        client.ObjectKey{Namespace: pr.Namespace, Name: pr.Spec.RepositoryReference.Name})
    if err != nil {
        logger.Error(err, "failed to create git auth provider for history note, skipping")
        r.removeHistoryNoteAnnotation(ctx, pr)
        return
    }

    gitRepo, err := utils.GetGitRepositoryFromObjectKey(
        ctx, r.Client, client.ObjectKey{Namespace: pr.Namespace, Name: pr.Spec.RepositoryReference.Name})
    if err != nil {
        logger.Error(err, "failed to get GitRepository for history note, skipping")
        r.removeHistoryNoteAnnotation(ctx, pr)
        return
    }

    gitOps := git.NewEnvironmentOperations(gitRepo, gitAuthProvider, pr.Spec.TargetBranch)
    if err := gitOps.CloneRepo(ctx); err != nil {
        logger.Error(err, "failed to clone repo for history note, skipping")
        r.removeHistoryNoteAnnotation(ctx, pr)
        return
    }

    content := buildHistoryNoteContent(pr)
    if err := gitOps.WritePromoterHistoryNote(ctx, mergeCommitSHA, content); err != nil {
        logger.Error(err, "failed to write promoter history note, skipping")
    }

    r.removeHistoryNoteAnnotation(ctx, pr)
}

// buildHistoryNoteContent formats PR status fields as a git trailer block.
func buildHistoryNoteContent(pr *promoterv1alpha1.PullRequest) string {
    mergeTime := time.Now().Format(time.RFC3339)
    lines := []string{
        fmt.Sprintf("%s: %s", constants.TrailerPullRequestID, pr.Status.ID),
        fmt.Sprintf("%s: %s", constants.TrailerPullRequestUrl, pr.Status.Url),
        fmt.Sprintf("%s: %s", constants.TrailerPullRequestCreationTime, pr.Status.PRCreationTime.Format(time.RFC3339)),
        fmt.Sprintf("%s: %s", constants.TrailerPullRequestMergeTime, mergeTime),
        fmt.Sprintf("%s: %s", constants.TrailerPullRequestSourceBranch, pr.Spec.SourceBranch),
        fmt.Sprintf("%s: %s", constants.TrailerPullRequestTargetBranch, pr.Spec.TargetBranch),
    }
    return strings.Join(lines, "\n")
}

func (r *PullRequestReconciler) removeHistoryNoteAnnotation(ctx context.Context, pr *promoterv1alpha1.PullRequest) {
    logger := log.FromContext(ctx)
    patch := client.MergeFrom(pr.DeepCopy())
    delete(pr.Annotations, ExternalMergeCommitSHAAnnotation)
    if err := r.Patch(ctx, pr, patch); err != nil && !errors.IsNotFound(err) {
        logger.Error(err, "failed to remove external-merge-commit-sha annotation")
    }
}
```

Call `writeHistoryNote` at the start of `cleanupTerminalStates`, before the delete:

```go
func (r *PullRequestReconciler) cleanupTerminalStates(ctx context.Context, pr *promoterv1alpha1.PullRequest) (bool, error) {
    // ... existing externallyMergedOrClosed / isTerminalState checks ...

    if externallyMergedOrClosed {
        r.writeHistoryNote(ctx, pr)  // best-effort; errors logged internally
    }

    // ... existing delete logic unchanged ...
}
```

Also add the required imports: `gitauth "github.com/argoproj-labs/gitops-promoter/internal/gitauth"`, `"github.com/argoproj-labs/gitops-promoter/internal/git"`, `"github.com/argoproj-labs/gitops-promoter/internal/types/constants"`.

**Step 6: Run tests to confirm they pass**

```bash
go test ./internal/controller/... -v -count=1 -run "writes a promoter history note|proceeds with cleanup normally"
```

Expected: PASS

**Step 7: Confirm all PR controller tests still pass**

```bash
go test ./internal/controller/... -v -count=1 -run "PullRequest"
```

Expected: all PASS

**Step 8: Commit**

```bash
git add internal/controller/pullrequest_controller.go internal/controller/pullrequest_controller_test.go
git commit -m "feat(pullrequest): write promoter history note on external merge via annotation"
```

---

## Task 4: Webhook handler — PR merge event detection and annotation patch

**Files:**
- Modify: `internal/webhookreceiver/server.go`
- Modify: `internal/webhookreceiver/server_test.go`

The webhook handler currently processes push events. This task adds a parallel path for GitHub PR merge events. The handler detects `X-Github-Event: pull_request` with `action: "closed"` and `merged: true`, extracts the PR number and `merge_commit_sha`, finds the matching `PullRequest` resource by `Status.ID`, and patches the annotation.

**Step 1: Write the failing tests**

Add to `server_test.go`. These tests require a real k8s client (the existing suite already has one via `envtest`). Check `webhookreceiver_suite_test.go` to see how it is set up.

```go
var _ = Describe("postRoot PR merge handling", func() {
    var (
        wr     *webhookreceiver.WebhookReceiver
        server *httptest.Server
    )

    BeforeEach(func() {
        wr = webhookreceiver.NewWebhookReceiver(mgr, nil)
        server = httptest.NewServer(http.HandlerFunc(wr.HandleWebhook))
    })

    AfterEach(func() { server.Close() })

    It("patches the annotation when a GitHub PR merge event matches a PullRequest", func() {
        pr := &promoterv1alpha1.PullRequest{
            ObjectMeta: metav1.ObjectMeta{Name: "test-pr", Namespace: "default"},
            Spec:       promoterv1alpha1.PullRequestSpec{SourceBranch: "env/staging-next", TargetBranch: "env/staging"},
        }
        pr.Status.ID = "42"
        Expect(k8sClient.Create(ctx, pr)).To(Succeed())
        Expect(k8sClient.Status().Update(ctx, pr)).To(Succeed())

        body := `{"action":"closed","pull_request":{"number":42,"merged":true,"merge_commit_sha":"abc123"}}`
        req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
        req.Header.Set("X-Github-Event", "pull_request")
        rec := httptest.NewRecorder()
        server.Config.Handler.ServeHTTP(rec, req)

        Expect(rec.Code).To(Equal(http.StatusNoContent))

        Eventually(func(g Gomega) {
            var updated promoterv1alpha1.PullRequest
            g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pr), &updated)).To(Succeed())
            g.Expect(updated.Annotations).To(HaveKeyWithValue(
                "promoter.argoproj.io/external-merge-commit-sha", "abc123"))
        }, timeout, interval).Should(Succeed())
    })

    It("returns 204 and does nothing when no PullRequest matches the PR number", func() {
        body := `{"action":"closed","pull_request":{"number":99999,"merged":true,"merge_commit_sha":"def456"}}`
        req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
        req.Header.Set("X-Github-Event", "pull_request")
        rec := httptest.NewRecorder()
        server.Config.Handler.ServeHTTP(rec, req)
        Expect(rec.Code).To(Equal(http.StatusNoContent))
    })

    It("ignores closed-but-not-merged PR events", func() {
        body := `{"action":"closed","pull_request":{"number":1,"merged":false,"merge_commit_sha":""}}`
        req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
        req.Header.Set("X-Github-Event", "pull_request")
        rec := httptest.NewRecorder()
        server.Config.Handler.ServeHTTP(rec, req)
        Expect(rec.Code).To(Equal(http.StatusNoContent))
    })
})
```

Note: `HandleWebhook` needs to be exported from `server.go` (currently `postRoot` is unexported). Either export it or test via `Start`. Look at how existing integration tests call `postRoot` and follow that pattern.

**Step 2: Run tests to confirm they fail**

```bash
go test ./internal/webhookreceiver/... -v -count=1 -run "PR merge handling"
```

Expected: FAIL

**Step 3: Implement — PR merge event detection**

Add a helper to detect PR merge events in `server.go`:

```go
// isPRMergeEvent returns true when the payload is a GitHub pull_request event
// where the PR was merged (not just closed). Returns the PR number and merge commit SHA.
func isPRMergeEvent(provider string, jsonBytes []byte) (ok bool, prNumber string, mergeCommitSHA string) {
    if provider != ProviderGitHub {
        return false, "", ""
    }
    if gjson.GetBytes(jsonBytes, "action").String() != "closed" {
        return false, "", ""
    }
    if !gjson.GetBytes(jsonBytes, "pull_request.merged").Bool() {
        return false, "", ""
    }
    number := strconv.FormatInt(gjson.GetBytes(jsonBytes, "pull_request.number").Int(), 10)
    sha := gjson.GetBytes(jsonBytes, "pull_request.merge_commit_sha").String()
    if number == "0" || sha == "" {
        return false, "", ""
    }
    return true, number, sha
}
```

**Step 4: Implement — findPullRequestByID**

```go
func (wr *WebhookReceiver) findPullRequestByID(ctx context.Context, prID string) (*promoterv1alpha1.PullRequest, error) {
    var prList promoterv1alpha1.PullRequestList
    if err := wr.k8sClient.List(ctx, &prList, &client.ListOptions{
        FieldSelector: fields.SelectorFromSet(map[string]string{
            ".status.id": prID,
        }),
    }); err != nil {
        return nil, fmt.Errorf("failed to list PullRequests by status.id %q: %w", prID, err)
    }
    if len(prList.Items) == 0 {
        return nil, nil
    }
    return &prList.Items[0], nil
}
```

**Step 5: Implement — extend postRoot**

In `postRoot`, after reading `jsonBytes` and before `findChangeTransferPolicy`, add:

```go
// Handle PR merge events — patch annotation to trigger history note writing
if ok, prNumber, mergeCommitSHA := isPRMergeEvent(provider, jsonBytes); ok {
    pr, err := wr.findPullRequestByID(r.Context(), prNumber)
    if err != nil {
        logger.V(4).Info("error finding PullRequest for PR merge event", "prNumber", prNumber, "error", err)
    } else if pr == nil {
        logger.V(4).Info("no PullRequest found for PR merge event", "prNumber", prNumber)
    } else {
        patch := client.MergeFrom(pr.DeepCopy())
        if pr.Annotations == nil {
            pr.Annotations = map[string]string{}
        }
        pr.Annotations[controller.ExternalMergeCommitSHAAnnotation] = mergeCommitSHA
        if err := wr.k8sClient.Patch(r.Context(), pr, patch); err != nil {
            logger.Error(err, "failed to patch external-merge-commit-sha annotation", "prNumber", prNumber)
        } else {
            logger.Info("patched external-merge-commit-sha annotation", "prNumber", prNumber, "sha", mergeCommitSHA)
        }
    }
    responseCode = http.StatusNoContent
    w.WriteHeader(responseCode)
    return
}
```

Note: `controller.ExternalMergeCommitSHAAnnotation` — move the constant from `pullrequest_controller.go` to a shared location (e.g., `api/v1alpha1/constants.go`) so both packages can reference it without an import cycle.

**Step 6: Run tests to confirm they pass**

```bash
go test ./internal/webhookreceiver/... -v -count=1 -run "PR merge handling"
```

Expected: PASS

**Step 7: Confirm existing webhook tests still pass**

```bash
go test ./internal/webhookreceiver/... -v -count=1
```

Expected: all PASS

**Step 8: Commit**

```bash
git add internal/webhookreceiver/server.go internal/webhookreceiver/server_test.go \
        internal/controller/pullrequest_controller.go api/v1alpha1/constants.go
git commit -m "feat(webhook): detect GitHub PR merge events and patch history annotation"
```

---

## Task 5: Full integration test

**Files:**
- Modify: `internal/controller/suite_test.go`

The existing suite test already has helpers for pushing commits, simulating webhooks, and asserting CTP status. This test ties all four tasks together: webhook patches annotation → PR controller writes note → CTP reconcile reads note → CTP status.history populated.

**Step 1: Write the failing test**

Find the section in `suite_test.go` that tests external PR merge behavior. Add a new `It` block:

```go
It("records PR history in CTP status when PR is merged externally via webhook", func() {
    By("creating a PullRequest resource in open state with a known PR ID")
    // use existing test helpers to set up GitRepository, SCMProvider, CTP, PullRequest

    By("simulating the merge commit landing on the active branch")
    mergeCommitSha := pushMergeCommit(ctx, gitPath, activeBranch, proposedBranch)

    By("simulating the GitHub PR merge webhook")
    body := fmt.Sprintf(`{"action":"closed","pull_request":{"number":%s,"merged":true,"merge_commit_sha":"%s"}}`,
        prID, mergeCommitSha)
    resp, err := http.Post(webhookServerURL+"/", "application/json", strings.NewReader(body))
    // add X-Github-Event header via http.NewRequest instead
    Expect(err).NotTo(HaveOccurred())
    Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

    By("verifying CTP history is populated with the PR details")
    Eventually(func(g Gomega) {
        var ctp promoterv1alpha1.ChangeTransferPolicy
        g.Expect(k8sClient.Get(ctx, ctpKey, &ctp)).To(Succeed())
        g.Expect(ctp.Status.History).NotTo(BeEmpty())
        g.Expect(ctp.Status.History[0].PullRequest).NotTo(BeNil())
        g.Expect(ctp.Status.History[0].PullRequest.ID).To(Equal(prID))
        g.Expect(ctp.Status.History[0].PullRequest.Url).NotTo(BeEmpty())
    }, longTimeout, interval).Should(Succeed())
})
```

Adapt to use the exact test helpers and setup patterns from nearby integration tests in the same file.

**Step 2: Run test to confirm it fails**

```bash
go test ./internal/controller/... -v -count=1 -run "records PR history.*merged externally"
```

Expected: FAIL — CTP history empty

**Step 3: Fix any wiring issues**

The integration test exercises the full path. Common issues to check:
- Is the field index for `.status.id` registered in `suite_test.go`'s `BeforeSuite`?
- Is the webhook server started and its URL accessible in the test?
- Is the `AnnotationChangedPredicate` actually firing? (add a log or check the PR resource annotations)

Fix wiring issues as they arise, re-run the test each time.

**Step 4: Run test to confirm it passes**

```bash
go test ./internal/controller/... -v -count=1 -run "records PR history.*merged externally"
```

Expected: PASS

**Step 5: Run the full test suite**

```bash
go test ./... -count=1
```

Expected: all PASS

**Step 6: Commit**

```bash
git add internal/controller/suite_test.go
git commit -m "test(integration): add end-to-end test for external PR merge history via webhook"
```

---

## Task 6: Create the team and assign work

Now that the plan is complete, create a parallel team to implement the four independent tasks concurrently. Tasks 1, 2, 3, and 4 can be assigned in parallel since they touch different packages. Task 5 depends on all four being complete.

Use `superpowers:dispatching-parallel-agents` to dispatch agents for Tasks 1–4 simultaneously, then run Task 5 after all four are merged.
