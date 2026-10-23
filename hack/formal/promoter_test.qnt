// -*- mode: Bluespec; -*-
//
// Scripted runs through the promoter model. `quint test` runs every `run` whose name ends in Test.
//
// mergeShaRefreshPromotesUngatedDryTest replays a counterexample to gatedMerge and promotionOrder
// in the model of the current code: stg merges dry commit 2 although its order gate only approved
// dry commit 1 and dev never had dry commit 2. resetPreventsUngatedMergeTest replays the same
// steps against the model with RESET_MERGE_ON_NEW_SHA = true, where the merge does not happen.

module stepsCurrent {
  import promoter(
    ENVS = Set("dev", "stg", "prd"),
    DEPENDS = Map("dev" -> Set(), "stg" -> Set("dev"), "prd" -> Set("stg")),
    MAX_DRY = 2,
    RESET_MERGE_ON_NEW_SHA = false,
  ).* from "./promoter"

  // Each step records history, as `step` does.
  action CommitDry = all { recordHistory, commitDry }
  action Hydrate(e: str): bool = all { recordHistory, hydrate(e, true) }
  action Healthy(e: str): bool = all { recordHistory, reportHealth(e, "success") }
  action Reconcile(e: str): bool = all { recordHistory, ctpCompute(e) }.then(all { recordHistory, ctpAct(e) })
  action OpenOnScm(e: str): bool = all { recordHistory, prCreate(e) }
  action MergeOnScm(e: str): bool = all { recordHistory, prMerge(e) }
  action Gates = all { recordHistory, psAggregate }.then(all { recordHistory, dscsEvaluate })

  // dev gets dry 1 and is healthy. stg's PullRequest for dry 1 is approved by the order gate, and
  // the controller sets spec.state to merged.
  action stgApprovedForDry1 =
    init
      .then(CommitDry)
      .then(Hydrate("dev"))
      .then(Hydrate("stg"))
      .then(Reconcile("dev")) // PullRequest for dev, mergeSha = dev's dry-1 commit
      .then(OpenOnScm("dev"))
      .then(Gates) // dev has no upstreams: gate success
      .then(Reconcile("dev")) // spec.state = merged
      .then(MergeOnScm("dev")) // dev active = dry 1
      .then(Healthy("dev"))
      .then(Reconcile("stg")) // PullRequest for stg, mergeSha = stg's dry-1 commit
      .then(OpenOnScm("stg"))
      .then(Reconcile("dev")) // dev status: active dry 1, healthy
      .then(Gates) // stg gate success for its dry-1 commit
      .then(Reconcile("stg")) // spec.state = merged
      .expect(pr.get("stg").specState == "merged")

  // Before the PullRequest controller merges, the hydrator pushes dry 2 to stg's proposed branch. The
  // SCM would now refuse the dry-1 mergeSha. The next stg reconcile moves spec.mergeSha to the dry-2
  // commit, which nothing has approved.
  action dry2LandsBeforeTheMerge =
    stgApprovedForDry1
      .then(CommitDry)
      .then(Hydrate("stg"))
      .then(Reconcile("stg"))

  run mergeShaRefreshPromotesUngatedDryTest =
    dry2LandsBeforeTheMerge
      .expect(pr.get("stg").specState == "merged" and pr.get("stg").mergeSha == proposed.get("stg").id)
      .expect(ctpStatus.get("stg").proposedGate == "pending")
      .then(MergeOnScm("stg"))
      .expect(active.get("stg").dry == 2 and active.get("dev").dry == 1)
      .expect(not(gatedMerge))
      .expect(not(promotionOrder))
}

module stepsReset {
  import promoter(
    ENVS = Set("dev", "stg", "prd"),
    DEPENDS = Map("dev" -> Set(), "stg" -> Set("dev"), "prd" -> Set("stg")),
    MAX_DRY = 2,
    RESET_MERGE_ON_NEW_SHA = true,
  ).* from "./promoter"

  action CommitDry = all { recordHistory, commitDry }
  action Hydrate(e: str): bool = all { recordHistory, hydrate(e, true) }
  action Healthy(e: str): bool = all { recordHistory, reportHealth(e, "success") }
  action Reconcile(e: str): bool = all { recordHistory, ctpCompute(e) }.then(all { recordHistory, ctpAct(e) })
  action OpenOnScm(e: str): bool = all { recordHistory, prCreate(e) }
  action MergeOnScm(e: str): bool = all { recordHistory, prMerge(e) }
  action Gates = all { recordHistory, psAggregate }.then(all { recordHistory, dscsEvaluate })

  run resetPreventsUngatedMergeTest =
    init
      .then(CommitDry)
      .then(Hydrate("dev"))
      .then(Hydrate("stg"))
      .then(Reconcile("dev"))
      .then(OpenOnScm("dev"))
      .then(Gates)
      .then(Reconcile("dev"))
      .then(MergeOnScm("dev"))
      .then(Healthy("dev"))
      .then(Reconcile("stg"))
      .then(OpenOnScm("stg"))
      .then(Reconcile("dev"))
      .then(Gates)
      .then(Reconcile("stg"))
      .expect(pr.get("stg").specState == "merged")
      .then(CommitDry)
      .then(Hydrate("stg"))
      .then(Reconcile("stg"))
      // Moving to the dry-2 commit took back the merge request, so the PullRequest controller has
      // nothing to merge, and the order gate keeps it that way while dev lacks dry 2.
      .expect(pr.get("stg").specState == "open")
      .then(Gates)
      .then(Reconcile("stg"))
      .expect(pr.get("stg").specState == "open" and gate.get("stg").phase == "pending")
      .expect(gatedMerge and promotionOrder)
}
