# Progressive Order Commit Status Controller

The Progressive Order Commit Status controller is an **ordering gate**, like
[DependentsSuccessfulCommitStatus](dependents-successful-commit-status.md), designed for **high-churn** repositories
where new dry commits land faster than upstream environments can soak them.

## Why a separate ordering gate?

`DependentsSuccessfulCommitStatus` only lets an environment promote once every upstream has promoted **the exact dry
commit** that environment is promoting and is successful on it. That is the safest ordering, but when commits arrive
faster than an upstream can roll out and become healthy, the upstream is almost always busy with the newest commit.
Because the hydrator moves every `-next` branch to the newest commit, the downstream's target keeps moving too, and the
downstream can wait indefinitely for a quiet period that never comes.

`ProgressiveOrderCommitStatus` keeps **progress** instead. For each environment it persists a *watermark*: the newest
dry commit the environment was observed running with every active commit status passing. A downstream environment may
promote as soon as every upstream has proven healthy on a commit **newer than what the downstream is running**, even if
that upstream is currently rolling out something newer still.

| | DependentsSuccessfulCommitStatus | ProgressiveOrderCommitStatus |
|---|---|---|
| Downstream may promote when | each upstream is successful on the *same* dry commit | each upstream was successful on a dry commit *newer than the downstream's active commit* |
| Upstream busy rolling out a newer commit | downstream waits | downstream proceeds (on progress already proven) |
| Upstream currently failing | downstream waits | downstream waits |
| Every commit soaked upstream before reaching the downstream | yes | no — see trade-off below |

> [!WARNING]
> **Trade-off.** The downstream promotes whatever is on its proposed branch, which is the newest hydrated commit. That
> commit may include changes newer than the upstream's watermark that the upstream has not finished soaking. The gate
> guarantees that the upstream has *made healthy progress* past the downstream and is not currently failing; it does not
> guarantee that every individual commit was healthy upstream first. Use `DependentsSuccessfulCommitStatus` where that
> stricter guarantee matters more than throughput, and pair this gate with other proposed gates (for example
> [TimedCommitStatus](timed-commit-status.md)) when you want extra protection.

## Gate evaluation

For each environment with an in-flight change (active dry SHA ≠ proposed dry SHA), the controller writes a proposed
`CommitStatus` with:

- **`success`** — for every direct `dependsOn` upstream:
    - the upstream has no active commit status in the `failure` phase, and
    - the upstream's watermark (`status.environments[].lastHealthyDry`) is newer, by commit time, than the environment's
      active dry commit. An environment that has never been deployed is behind any watermark.
- **`pending`** — otherwise. `description` explains which upstream is blocking.

Root environments (no `dependsOn`) are always `success`.

**No-op upstreams.** When an upstream's hydrator advances its git note without producing a new hydrated commit (the
change did not touch that environment), its active dry SHA never moves, so neither does its watermark. As in
`DependentsSuccessfulCommitStatus`, a clean and healthy no-op upstream is looked through: it is satisfied when its own
upstreams are, or immediately when it is a root.

### Progress watermark

`status.environments[].lastHealthyDry` is updated on every reconcile:

- When the environment's active commit statuses are all `success` on a dry commit newer than the current watermark,
  the watermark advances to that commit.
- While the environment is rolling out a newer commit (pending) or a newer commit is failing, the previous watermark is
  kept. Pending upstreams do not block downstreams; failing ones do (see above).
- If the active dry commit moves **backwards** (for example after a [rollback](../../advanced-usage/rolling-back.md)),
  the newer watermark is no longer trusted: it is reset to the current commit if healthy, or cleared.

The watermark lives in status, so it survives controller restarts. Deleting and recreating the
`ProgressiveOrderCommitStatus` resets all watermarks; they are re-established the next time each environment is healthy.

## Example

Dependency resolution is identical to `DependentsSuccessfulCommitStatus`: omit `dependsOn` for a linear chain inferred
from `spec.environments` order, or declare `dependsOn` for a custom graph.

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: PromotionStrategy
metadata:
  name: demo
spec:
  gitRepositoryRef:
    name: demo
  orderCommitStatusRef:
    group: promoter.argoproj.io
    kind: ProgressiveOrderCommitStatus
    name: demo
  activeCommitStatuses:
    - key: argocd-health
  environments:
    - branch: environment/dev
    - branch: environment/staging
    - branch: environment/prod
---
apiVersion: promoter.argoproj.io/v1alpha1
kind: ProgressiveOrderCommitStatus
metadata:
  name: demo
spec:
  key: progressive-order
  promotionStrategyRef:
    name: demo
```

> [!IMPORTANT]
> Configure at least one active commit status (for example `argocd-health`). With no active commit statuses an
> environment counts as healthy as soon as a commit is merged, and the watermark advances immediately.

### Walkthrough

Commits `A`, `B`, `C` land an hour apart; staging takes longer than an hour to become healthy.

1. Prod runs `A`. Staging became healthy on `B`, so its watermark is `B`. Staging is now rolling out `C` (pending).
2. Prod's proposed branch is at `C`. Staging's watermark `B` is newer than prod's active `A` and staging is not
   failing, so prod's gate is `success` and prod promotes `C`.
3. With `DependentsSuccessfulCommitStatus`, prod would wait for staging to be healthy on `C`, and if `D` landed first,
   for `D`, and so on.

## Switching from DependentsSuccessfulCommitStatus

1. Create the `ProgressiveOrderCommitStatus` for the PromotionStrategy.
2. Change `spec.orderCommitStatusRef.kind` (and `name`) on the PromotionStrategy. The PromotionStrategy controller
   injects the new `spec.key` onto every `ChangeTransferPolicy`.
3. Delete the old `DependentsSuccessfulCommitStatus`; its child `CommitStatus` resources are garbage collected.

If you use SCM branch protection on the gate's commit status context, update the required check name to the new
`spec.key`.

## `spec.url`

`spec.url.template` works as in
[DependentsSuccessfulCommitStatus](dependents-successful-commit-status.md#commit-status-url-template), with
`.ProgressiveOrderCommitStatus` (the whole CR) in place of `.DependentsSuccessfulCommitStatus`. `.Environment`,
`.PromotionStrategy`, `.DependsOn` and `.DependsOnQuery` are also available.

## Controller configuration

`ControllerConfiguration.spec.progressiveOrderCommitStatus.workQueue` is required, like the other controllers' work
queue settings. If you manage your own `ControllerConfiguration`, add a `progressiveOrderCommitStatus` block when
upgrading (see [Upgrading](../../upgrading.md)).

## Status

See [ProgressiveOrderCommitStatus](../../crd-specs.md#progressiveordercommitstatus) for a full example.
`status.environments[]` reports, per branch, the watermark (`lastHealthyDry`), the direct upstreams and whether each is
satisfied, a copy of the active commit statuses, and the child CommitStatus mirror fields (`phase`, `description`,
`url`, `reportedSha`).
