# Formal model of promotion

`promoter.qnt` is a [Quint](https://quint-lang.org) model of how GitOps Promoter moves a dry commit
through the environments of one PromotionStrategy. Quint checks the model against safety properties,
either by random simulation or by bounded model checking with Apalache. `promoter_test.qnt` holds
scripted runs through the model.

It is a model, not the code. Each action names the Go function it stands for. When that function
changes in a way that matters to the properties below, update the model too.

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

Not modelled yet: RestoreActiveCommit and the revert gate, external (human) merges and closes, more
than one proposed commit status key, commit-time comparisons, finalizers, history notes, controller
restarts.

## Properties

* `gatedMerge`: the SCM only merges a commit into an environment if the order gate CommitStatus was
  success, at some point, for a proposed commit of the same dry SHA in that environment.
* `promotionOrder`: a dry SHA lands on an environment only after each `dependsOn` upstream had it
  active and healthy, or passed it through as a clean, healthy no-op.
* `noRollback`: an environment's active dry SHA never moves to an older dry commit.

`safety` is all three. The `witness*` properties are meant to be violated. Violating them shows that
runs reach full promotions and no-op pass-throughs, which a model where nothing ever merges would not.

## Finding: a refreshed `spec.mergeSha` merges a commit nothing approved

`mergeShaRefreshPromotesUngatedDryTest` in `promoter_test.qnt` breaks `gatedMerge` and
`promotionOrder` in the model of the current code:

1. stg's order gate approves the commit hydrated from dry 1, and `mergePullRequests` sets the
   PullRequest's `spec.state` to `merged`.
2. Before the SCM merges, the hydrator pushes the commit for dry 2 to stg's proposed branch. The
   SCM refuses the merge, because the branch is no longer at `spec.mergeSha`.
3. The next ChangeTransferPolicy reconcile sees dry 2, with its gate pending.
   `createOrUpdatePullRequest` copies `spec.state: merged` from the existing PullRequest and moves
   `spec.mergeSha` to the dry-2 commit. `mergePullRequests` declines to merge, but `spec.state` is
   already `merged`.
4. The PullRequest controller merges the dry-2 commit. dev never had dry 2.

The same steps reproduce against the real controllers in envtest with the fake SCM: the active
branch's merge commit has the unapproved SHA as its second parent.

Moving `spec.mergeSha` on an approved PullRequest is deliberate after conflict resolution, where the
new commit has the same content (see the comment in `createOrUpdatePullRequest`). The models with
`RESET_MERGE_ON_NEW_SHA = true` reset `spec.state` to `open` whenever `spec.mergeSha` changes, so
`mergePullRequests` has to approve the new SHA again. In the same reconcile it does, if the gate is
already green. `resetPreventsUngatedMergeTest` replays the steps against that variant, and random
simulation of the linear and diamond graphs finds no violation of `safety`.

Random simulation of the current-code model rarely hits this interleaving. That is why it has a
scripted test.

## Running

Quint 0.33 runs through `npx`. Apalache needs Java 17 or newer and is fetched by Quint on first use.

```shell
make -C hack/formal check     # typecheck, scripted tests, simulation, witnesses
make -C hack/formal verify    # bounded model checking with Apalache, VERIFY_STEPS=8 by default
```

Useful while changing the model:

```shell
cd hack/formal
quint run promoter.qnt --main=linearReset --invariant=safety --max-samples=100000 --max-steps=80
quint run promoter.qnt --main=linear --invariant=gatedMerge --max-samples=100000 --max-steps=80 --mbt --out-itf=trace.itf.json
```

`--mbt` records the action and nondeterministic choices taken at each step in the ITF trace.

## Next steps

* Model RestoreActiveCommit and the revert gate, and check that a pending restore blocks auto-merge.
* Model external merges and closes, and human-set `spec.state`.
* Check liveness: with green statuses and a hydrator that catches up, every environment eventually
  reaches the newest dry SHA.
* Record traces from the e2e tests and check them against the model (trace validation).
