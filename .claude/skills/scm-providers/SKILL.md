---
name: scm-providers
description: Add or modify SCM provider integrations (GitHub, GitLab, Gitea, Forgejo, Bitbucket Cloud, Azure DevOps) in internal/scms/ — provider interfaces, RecordSCMCall metrics, and controller wiring. Use when touching internal/scms/ or adding support for a new SCM.
---

# SCM provider work

Authoritative doc: `docs/contributing/adding-an-scm-provider.md`. Use an existing provider (e.g. `internal/scms/github/` or `internal/scms/gitea/`) as the structural template.

## What a provider implements

- `scms.CommitStatusProvider` (`internal/scms/commitstatus.go`) — create/update commit statuses or checks for promotion gates.
- `scms.PullRequestProvider` (`internal/scms/pullrequest.go`) — open, update, merge, close, list PRs for change transfer.

Then wire it into the controller layer: constructor selection from `ScmProvider` / `ClusterScmProvider` spec, auth secret handling, RBAC markers, and tests. `internal/scms/fake/` and `internal/scms/mock/` exist for testing.

## Non-negotiable: record every SCM API call

Every provider HTTP API request must call `metrics.RecordSCMCall` (`internal/metrics/metrics.go`) **exactly once per logical request**, after the call completes (success or failure). This feeds `scm_calls_total` / `scm_calls_duration_seconds` and the structured "SCM API call" debug log. Don't skip "small" calls — if it hits the REST API and counts against rate limits, record it.

Pattern when the SDK exposes a response:

```go
start := time.Now()
pr, resp, err := client.CreatePullRequest(owner, repo, opts)
if resp != nil {
    metrics.RecordSCMCall(ctx, gitRepo, metrics.SCMAPIPullRequest, metrics.SCMOperationCreate,
        resp.StatusCode, time.Since(start), nil)
}
if err != nil {
    return "", err // add a fallback RecordSCMCall with e.g. 500 if resp was nil
}
```

Pattern when it doesn't (single call, both paths):

```go
start := time.Now()
created, err := client.CreatePullRequest(ctx, args)
statusCode := 201
if err != nil {
    statusCode = 500 // or map from provider-specific error
}
metrics.RecordSCMCall(ctx, gitRepo, metrics.SCMAPIPullRequest, metrics.SCMOperationCreate,
    statusCode, time.Since(start), nil)
if err != nil {
    return "", fmt.Errorf("failed to create pull request: %w", err)
}
```

Details:

- Use the `context.Context` passed into the provider method so logging inherits reconcile fields.
- Resolve the `GitRepository` the standard way (`utils.GetGitRepositoryFromObjectKey` or equivalent).
- `api` is `metrics.SCMAPICommitStatus` or `metrics.SCMAPIPullRequest`; `operation` is the closest `metrics.SCMOperation` (`create`, `update`, `merge`, `close`, `list`, `get`).
- GitHub only: pass `getRateLimitMetrics(response.Rate)` as the last arg; other providers pass `nil`.

## Auth and webhooks

- Credentials come from the Secret referenced by `ScmProvider`/`ClusterScmProvider` (GitHub Apps, GitLab/Forgejo/Gitea tokens, etc.); git auth plumbing is in `internal/gitauth/`, git operations in `internal/git/` (bare repos).
- Webhook handling for real-time reconciles lives in `internal/webhookreceiver/` — a new provider usually needs its webhook event parsing added there.

## Checklist for a new provider

1. New package `internal/scms/<provider>/` implementing both interfaces, error-wrapping with `%w`.
2. `RecordSCMCall` on every API round trip.
3. Constructor wiring wherever providers are selected from the ScmProvider spec (grep for an existing provider name, e.g. `Forgejo`, to find all switch sites — API types, controllers, webhook receiver).
4. API type update: provider config struct on `ScmProviderSpec` (follow the `crd-changes` skill; regen with `make build-installer`).
5. Unit tests following an existing provider's tests; e2e coverage if feasible.
6. Docs: provider setup page under `docs/`, `mkdocs.yml` entry, and update supported-provider lists (README, `docs/index.md`).
