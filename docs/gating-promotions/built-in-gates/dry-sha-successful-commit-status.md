# DryShaSuccessfulCommitStatus

`DryShaSuccessfulCommitStatus` is a promotion **ordering gate**: it decides whether an environment may take a
change, based on how that change fared in the environments upstream of it.

It answers a different question from
[DependentsSuccessfulCommitStatus](dependents-successful-commit-status.md):

| | DependentsSuccessfulCommitStatus | DryShaSuccessfulCommitStatus |
|---|---|---|
| Question | Is the upstream running this dry commit **and healthy right now**? | Has this dry commit **already been successful** upstream? |
| Source | Live `PromotionStrategy` status | Promotion-history git notes on the upstream's active branch |
| Upstream races ahead | Gate stalls | Gate still passes |

Both gates resolve the dependency graph identically, and both use the same definition of success: the upstream
environment's **active** commit statuses were all passing.

## The problem it solves

Consider `dev → staging → prod`. A change `A` lands in `dev` and goes healthy. Before `staging` promotes it, two more
changes `B` and `C` land in `dev`.

With `DependentsSuccessfulCommitStatus`, `staging`'s gate for `A` looks at `dev` and sees it running `C`, not `A`. The
gate reports pending and `staging` never promotes `A` — even though `A` was healthy in `dev`.

`DryShaSuccessfulCommitStatus` remembers. `A` appears in `dev`'s record as successful, so `staging`'s gate passes.

## How the record is built

Every promotion the promoter performs writes a **promotion-history git note** on the merge commit of the target
environment's active branch (`refs/notes/promoter.history`). Two of that note's trailers are captured from the same
snapshot and therefore describe the same moment:

- `Sha-dry-active` — the dry commit the environment **was running** immediately before the merge
- `Commit-status-active-<key>-phase` — that dry commit's active commit status phases

Pairing them yields "dry commit X was running in this environment, and these were its checks". Walking the active
branch's first-parent history and reading those notes reconstructs the whole record.

Because git history is the source of truth, the record survives controller restarts, does not depend on the controller
having been watching at the right moment, and can be regenerated at any time.

### Status is a cache

The reconstructed record is stored on `status.environments[].dryShaHistory`, newest first, alongside
`rebuiltFromSha` (the active branch tip it was walked from) and `rebuiltAt`.

`rebuiltFromSha` is the cache key: while an environment's live active hydrated SHA still matches it, the controller does
no git work at all. Deleting `status.environments` is safe — the next reconcile regenerates it.

The dry commit an environment is running *right now* has no note yet (one is written when the *next* promotion merges),
so it is prepended each reconcile from live status with `source: live`. Git-derived entries carry `source: note`, or
`source: commit-message` when the note is missing and the merge commit's own trailers were used instead.

### Live checks versus recorded verdicts

The record is not purely historical. The head entry is recomputed from live `PromotionStrategy` status on **every**
reconcile — including reconciles where the git walk is skipped because the branch tip has not moved — because an
environment's health changes without its active branch moving.

That splits the gate's freshness by case. For upstream `U` and the dry commit `T` being promoted:

| Situation | What the gate reads |
|---|---|
| `U` is running `T` right now | `U`'s **live** active commit statuses, re-read each reconcile |
| `U` has already promoted past `T` | the verdict recorded in `T`'s promotion-history note |

So when environments move in lockstep, this gate checks exactly what
[DependentsSuccessfulCommitStatus](dependents-successful-commit-status.md) checks, at the same freshness. Recorded
verdicts only come into play for commits the upstream has already left behind — where a live check is impossible,
because nothing is running that commit any more.

### No-op hydrations

There is one case the record can never answer. When a dry commit renders no change for an environment — a
change scoped to another environment's values, or to a chart path this one does not use — that environment's
hydrator advances its git note to the dry commit **without producing a new hydrated commit**. No promotion
happens, so no promotion-history note ever names it, and it can never enter that environment's
`dryShaHistory`. Waiting for it to be promoted would stall forever.

The gate detects this from live status and looks *past* the environment to its own upstreams, the same way
[DependentsSuccessfulCommitStatus](dependents-successful-commit-status.md) does. An environment is only
skipped when the no-op is clean:

- its hydrator has processed the target dry commit,
- its git note has advanced past the dry commit its hydrated content was rendered from,
- it has no promotion of its own still in flight, and
- its own active commit statuses are passing.

Skipping it never skips what is behind it — every upstream it depends on still has to be satisfied in its own
right. When an environment is skipped this way, its `status.environments[].upstreams[]` entry is reported
satisfied with a `reason` saying the proposed dry commit renders no change there, because no
`dryShaHistory` entry records it.

## Ordering and the `allowNewerDrySha` rule

The record is a **first-parent walk**, so a lower index is strictly a later promotion on that branch. That makes the
"newer commit" rule sound rather than a guess: if the upstream was successful on an entry newer than the target, it
demonstrably ran the target's content and has since become healthy past it.

An upstream is satisfied when either:

1. the target dry commit itself is recorded successful, or
2. a **newer** entry is recorded successful (`spec.allowNewerDrySha`, default `true`).

No timestamps are compared, so clock skew, second-granularity ties, and force-pushed commit times cannot affect the
decision.

Set `allowNewerDrySha: false` to require the target dry commit itself to have been successful. This is stricter, but an
environment whose health window was never captured can stall — see the caveats below.

## Configuration

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: DryShaSuccessfulCommitStatus
metadata:
  name: my-ordering-gate
  namespace: promoter-system
spec:
  promotionStrategyRef:
    name: my-promotion-strategy
  key: dry-sha-successful
  historyDepth: 20
  allowNewerDrySha: true
```

Attach it to the PromotionStrategy. `kind` must be set explicitly, because `orderCommitStatusRef.kind` defaults to
`DependentsSuccessfulCommitStatus`:

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: PromotionStrategy
metadata:
  name: my-promotion-strategy
spec:
  orderCommitStatusRef:
    kind: DryShaSuccessfulCommitStatus
    name: my-ordering-gate
  environments:
    - branch: environment/dev
    - branch: environment/staging
    - branch: environment/prod
```

The PromotionStrategy controller injects `spec.key` onto every ChangeTransferPolicy's `proposedCommitStatuses`, so you do
not list it yourself. The child `CommitStatus` uses the bare `spec.key` as its SCM context name for every environment, so
a single predictable name can be used in branch-protection rules.

| Field | Default | Meaning |
|-------|---------|---------|
| `key` | required | Commit status key, and the SCM context name |
| `historyDepth` | `20` | First-parent commits walked per active branch |
| `allowNewerDrySha` | `true` | Allow a newer successful entry to satisfy the target |
| `url.template` | — | Go template for the child CommitStatus details link |

## Caveats

- **An environment with no `activeCommitStatuses` records every dry commit as successful.** With nothing to check, there
  is nothing to fail. `DependentsSuccessfulCommitStatus` behaves the same way. Configure at least one active gate (for
  example [ArgoCDCommitStatus](argocd-commit-status.md)) on any environment you want this gate to mean something for.
- **`historyDepth` is a hard boundary.** A dry commit older than the walk is not found and the gate reports pending,
  naming `historyDepth` in the description so the fix is discoverable. Raise it for environments that promote in large
  batches.
- **Promotion history is best-effort.** A pull request created and merged before the promoter ever refreshed its commit
  message carries no trailers, so that merge contributes no entry. The git note survives SCM-side message rewrites
  (squash merges, merges performed directly on the SCM); the commit-message fallback does not.
- **A recorded verdict is a point sample, not a soak.** The trailers a note is built from are a snapshot of
  `ctp.Status.Active.CommitStatuses` written into the pull request's commit message. That snapshot is refreshed on each
  reconcile that applies the PR and frozen once the PR reaches merged or closed, and the note is written from the last
  snapshot persisted. A recorded verdict therefore means *the upstream's active checks as of the last PR-message refresh
  before the promotion that superseded this dry commit merged* — not "healthy for the whole time it was live". A commit
  whose health flapped records only its final sample, and an upstream that degraded between that refresh and the merge
  records the stale passing verdict. This applies only to commits the upstream has already promoted past; the one it is
  running now is always checked live (see
  [Live checks versus recorded verdicts](#live-checks-versus-recorded-verdicts)).
- **This is the only built-in gate that clones the repository.** Its ServiceAccount needs read access to the
  `GitRepository`, `ScmProvider`/`ClusterScmProvider`, and the credentials `Secret`. Steady-state cost is near zero — the
  clone is reused across reconciles and the walk is skipped while branch tips are unchanged — but expect one clone per
  gate resource after a controller restart.

## Choosing between the two ordering gates

Use `DependentsSuccessfulCommitStatus` when you want promotion to move in lockstep and an upstream that has moved on
*should* block downstream work.

Use `DryShaSuccessfulCommitStatus` when lower environments legitimately move faster than higher ones — a busy `dev` that
takes many changes a day while `prod` promotes weekly — and you want the question to be "was this change good in dev?"
rather than "is dev on this change right now?".
