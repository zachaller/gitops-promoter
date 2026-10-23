# Formal model of promotion

`promoter.qnt` is a [Quint](https://quint-lang.org) model of how GitOps Promoter moves a dry commit
through the environments of one PromotionStrategy. Quint checks the model against safety properties,
either by random simulation or by bounded model checking with Apalache. `promoter_test.qnt` holds
scripted runs through the model.

It is a model, not the code. Each action names the Go function it stands for. When that function
changes in a way that matters to the properties below, update the model too. Trace validation (below)
checks recorded runs of the real controllers against the model's decisions, so drift between the two
shows up as a failing e2e job.

## What it models

| Model                                                 | Code                                                                                                           |
|-------------------------------------------------------|----------------------------------------------------------------------------------------------------------------|
| `ctpCompute`                                          | `calculateStatus` / `setCommitStatusState` (a CommitStatus counts only for the SHA in its spec)                |
| `ctpResolveConflict`                                  | `gitMergeStrategyOurs` (push a merge commit to the proposed branch, requeue)                                   |
| `ctpAct` / `upsertPullRequest`                        | `createOrUpdatePullRequest` (keeps `spec.state`, sets `spec.mergeSha` to the proposed hydrated SHA)            |
| `ctpAct` / `requestMerge`                             | `mergePullRequests` (sets `spec.state: merged` once every proposed commit status is success)                   |
| `prCreate`, `prMerge`, `prDelete`                     | `PullRequestReconciler`; the SCM refuses a merge unless the source branch is at `spec.mergeSha`                |
| `psAggregate`                                         | `PromotionStrategyReconciler.calculateStatus`                                                                  |
| `dscsEvaluate` / `upstreamCheck`                      | `updateDependentsSuccessfulCommitStatus` / `isUpstreamPending` / `getEffectiveHydratedDrySha`                  |
| `commitDry`, `hydrate`, `reportHealth`                | a developer, the hydrator (may skip dry commits or produce no-ops), Argo CD health                             |

Controllers act on what they read earlier. A ChangeTransferPolicy reconcile is split into
`ctpCompute` and `ctpAct`, and anything can happen in between. DependentsSuccessfulCommitStatus reads
the PromotionStrategy status, which is itself an earlier copy of the ChangeTransferPolicy statuses.

The controllers' decisions (`gateFor`, `upstreamCheck`, `upsertPullRequest`, `requestMerge`) and the
order rule (`cleared*`, `orderKept`) are pure definitions in the module `logic`, which both the
model and the trace checker import.

Not modelled yet: RestoreActiveCommit and the revert gate, external (human) merges and closes, more
than one proposed commit status key, finalizers, history notes, controller restarts.

## Properties

* `gatedMerge`: the SCM only merges a commit into an environment if the order gate CommitStatus was
  success, at some point, for a proposed commit of the same dry SHA in that environment.
* `promotionOrder`: a dry SHA lands on an environment only after each `dependsOn` upstream had it
  active and healthy, or passed it through as a clean, healthy no-op.
* `noRollback`: an environment's active dry SHA never moves to an older dry commit.

`safety` is all three. The `witness*` properties are meant to be violated. Violating them shows that
runs reach full promotions and no-op pass-throughs, which a model where nothing ever merges would not.

## Finding (fixed): a refreshed `spec.mergeSha` merged a commit nothing approved

`mergeShaRefreshPromotesUngatedDryTest` in `promoter_test.qnt` breaks `gatedMerge` and
`promotionOrder` in the model of the code before the fix:

1. stg's order gate approves the commit hydrated from dry 1, and `mergePullRequests` sets the
   PullRequest's `spec.state` to `merged`.
2. Before the SCM merges, the hydrator pushes the commit for dry 2 to stg's proposed branch. The
   SCM refuses the merge, because the branch is no longer at `spec.mergeSha`.
3. The next ChangeTransferPolicy reconcile sees dry 2, with its gate pending.
   `createOrUpdatePullRequest` copies `spec.state: merged` from the existing PullRequest and moves
   `spec.mergeSha` to the dry-2 commit. `mergePullRequests` declines to merge, but `spec.state` is
   already `merged`.
4. The PullRequest controller merges the dry-2 commit. dev never had dry 2.

The same steps reproduced against the real controllers in envtest with the fake SCM.

`createOrUpdatePullRequest` now resets `spec.state` from `merged` to `open` whenever it moves
`spec.mergeSha`, so `mergePullRequests` has to approve the new SHA again. If the commit statuses
already pass, it does so in the same reconcile, or the next one if its cache has not caught up with
the PullRequest write yet. This also covers conflict resolution, where the new
commit has the same content: the merge now waits for the conflict-resolution commit's statuses. The
envtest regression test is "createOrUpdatePullRequest after the proposed branch moves past an
approved merge" in `internal/controller/changetransferpolicy_controller_test.go`.

The models with `RESET_MERGE_ON_NEW_SHA = true` (`linearReset`, `diamondReset`) match the code now.
`resetPreventsUngatedMergeTest` replays the steps against them, and random simulation of the linear
and diamond graphs finds no violation of `safety`. `linear` keeps the code as it was before the fix.

Random simulation of `linear` rarely hits this interleaving. That is why it has a scripted test.

## Running

Quint 0.33 runs through `npx`. Apalache needs Java 17 or newer and is fetched by Quint on first use.

```shell
make -C hack/formal check     # typecheck, scripted tests, simulation, witnesses
make -C hack/formal verify    # bounded model checking with Apalache, VERIFY_STEPS=4 by default
```

Useful while changing the model:

```shell
cd hack/formal
quint run promoter.qnt --main=linearReset --invariant=safety --max-samples=100000 --max-steps=80
quint run promoter.qnt --main=linear --invariant=gatedMerge --max-samples=100000 --max-steps=80 --mbt --out-itf=trace.itf.json
```

`--mbt` records the action and nondeterministic choices taken at each step in the ITF trace.

## Trace validation

`test/tracerecord` watches PromotionStrategies, ChangeTransferPolicies, PullRequests and
CommitStatuses and records every change the API server stores. `hack/formal/tracecheck` orders the
changes by resourceVersion (an etcd-backed API server assigns them from one counter), splits them into
one trace per PromotionStrategy, numbers the SHAs the way the model does (dry SHAs in commit-time
order), and renders each trace as a Quint module that runs `checkTrace` from `tracecheck.qnt`.

`checkTrace` reports every recorded change the model cannot explain:

| Rule              | Recorded change                                                         | What explains it                                                                                                   |
|-------------------|-------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------|
| `gate`            | a DependentsSuccessfulCommitStatus CommitStatus spec                    | `gateFor` on some PromotionStrategy status stored before it (the controller's cache may be stale, never ahead)    |
| `pullRequestSpec` | a PullRequest created, or its `spec.state` or `spec.mergeSha` changed   | `upsertPullRequest` or `requestMerge` on that reconcile's ChangeTransferPolicy status and a PullRequest version the controller could have read |
| `ungated`, `outOfOrder`, `rollback` | a merge on the SCM the PullRequest asked for          | the safety properties, on the facts recorded so far                                                              |

Merges nobody asked for in `spec.state` (a person merged on the SCM) are counted but not checked. The
check only covers what happened: a controller that did nothing is always explained.

The e2e workflow sets `PROMOTER_TRACE_DIR`, so the SCM provider specs record the `scm-e2e` namespace,
and then runs `make -C hack/formal check-traces`. The envtest controller suite records too when the
variable is set:

```shell
export PROMOTER_TRACE_DIR=$(mktemp -d)
make test                                  # or make test-e2e against a Kind cluster
make -C hack/formal check-traces TRACE_DIR=$PROMOTER_TRACE_DIR
```

It prints, per PromotionStrategy, how many gate writes, PullRequest spec changes and merges it checked,
and for a violation the rule, the environment and the recorded change. The generated modules are in
`$PROMOTER_TRACE_DIR/quint`; each event there is commented with the object and resourceVersion it
came from.

Traces only see what reaches the API server, so the checker cannot tell which of several cached
versions a controller read. It accepts any version that was stored and not yet superseded by the
controller's own later write.

## Next steps

* Model RestoreActiveCommit and the revert gate, and check that a pending restore blocks auto-merge.
* Model external merges and closes, and human-set `spec.state`.
* Check liveness: with green statuses and a hydrator that catches up, every environment eventually
  reaches the newest dry SHA.
* Check ChangeTransferPolicy status writes against git, which the recorder cannot see yet.
