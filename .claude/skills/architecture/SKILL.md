---
name: architecture
description: Understand GitOps Promoter's architecture — the promotion model, CRDs and their relationships, controller layout, and where code lives. Use when orienting in the codebase, designing a feature, or answering "where does X happen / where should Y go" questions.
---

# GitOps Promoter Architecture

## What it does

GitOps Promoter is a Kubernetes operator (controller-runtime / kubebuilder) that promotes GitOps config through environments. Users commit once to a "DRY branch"; a hydrator renders environment-specific "hydrated branches" (`environment/<env>` + `environment/<env>-next`), and the promoter moves changes between them via pull requests, gated by commit statuses. Diagrams: `docs/architecture.md`.

## CRDs (`api/v1alpha1/`) and relationships

| Kind | Role |
| --- | --- |
| `PromotionStrategy` | Top-level: ordered environments + which commit statuses gate promotion. Creates one `ChangeTransferPolicy` per environment. |
| `ChangeTransferPolicy` (CTP) | Moves changes from proposed to active branch for one environment; creates `PullRequest` objects. |
| `PullRequest` | Represents a promotion PR in the SCM (open/merge/close via provider). |
| `CommitStatus` | A gate result for a SHA; matched to strategy gates by the `promoter.argoproj.io/commit-status` label. |
| `GitRepository` | A git repo; references an `ScmProvider` or `ClusterScmProvider`. |
| `ScmProvider` / `ClusterScmProvider` | SCM config (GitHub, GitLab, Gitea, Forgejo, Bitbucket Cloud, Azure DevOps); references a Secret for creds. |
| Gate CRs: `ArgoCDCommitStatus`, `TimedCommitStatus`, `GitCommitStatus`, `WebRequestCommitStatus` | Built-in gate controllers that create `CommitStatus` objects; all carry `spec.promotionStrategyRef`. |
| `RevertCommit` | Represents a revert operation. |
| `ControllerConfiguration` | Operator-wide config; defaults live in `config/config/controllerconfiguration.yaml` manifests, not code. |

Chain: `PromotionStrategy` → `ChangeTransferPolicy` (per env) → `PullRequest`; gates → `CommitStatus`; `PromotionStrategy`/gates reference `GitRepository` → `ScmProvider`/`ClusterScmProvider` → `Secret`.

## Package map

- `api/v1alpha1/` — CRD types, validation markers, label/finalizer constants (`constants.go`). `zz_generated.*` files are generated.
- `internal/controller/` — one reconciler per kind; shared helpers in `event_utils.go`, `fieldindex.go`, `finalizer_utils.go`; example CRs in `testdata/`.
- `internal/scms/` — provider interfaces (`commitstatus.go`, `pullrequest.go`) and per-provider packages (`github/`, `gitlab/`, `gitea/`, `forgejo/`, `bitbucket_cloud/`, `azuredevops/`, `fake/`, `mock/`).
- `internal/git/` — git plumbing (bare repos; auth via `internal/gitauth`).
- `internal/webhookreceiver/` — inbound SCM webhooks that trigger fast reconciles.
- `internal/metrics/` — Prometheus metrics incl. `RecordSCMCall`.
- `internal/apiserver/` + `api/view/` — dashboard extension apiserver (aggregation API, **not** CRDs — deliberately excluded from CRD codegen).
- `internal/webserver/`, `ui/dashboard/`, `ui/extension/`, `ui/components-lib/` — dashboard web server, React dashboard, Argo CD extension.
- `internal/settings/`, `internal/utils/`, `internal/types/argocd/` — config accessors, shared helpers (e.g. `KubeSafeLabel`), vendored Argo CD Application type.
- `config/` — kustomize manifests; `config/crd/bases/` is generated, never hand-edit.
- `hack/`, `test/e2e/`, `docs/` (MkDocs), `webrequestsimulator/`.

## Key architectural rules

- **Status writes go through one chokepoint**: the deferred `utils.HandleReconciliationResult` at the top of each `Reconcile` applies status via Server-Side Apply with a per-controller field owner. Controllers mutate `obj.Status` in memory only — never call `r.Status().Update()`/`Patch()` directly. Details: `docs/contributing/updating-status.md` and the `controller-patterns` skill.
- **Gate controllers list by field index**, not namespace-list + filter: `client.MatchingFields{controller.PromotionStrategyRefField: ps.Name}` with indexes registered in `internal/controller/fieldindex.go`.
- **Every SCM API call records metrics** via `metrics.RecordSCMCall`.
- **RBAC is declared as `// +kubebuilder:rbac` markers** on controllers; `config/rbac/role.yaml` is generated.
- Labels/finalizers use constants from `api/v1alpha1/constants.go`; free-form label values are sanitized with `utils.KubeSafeLabel()`.

## Where to go deeper

`docs/contributing/` covers: adding an SCM provider, developing a commit-status gate, maintaining CRDs, writing CEL rules, status handling, finalizers, CI, and the Argo CD extension. Prefer following an existing controller/provider as a template over inventing new patterns.
