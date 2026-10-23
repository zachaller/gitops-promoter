// -*- mode: Bluespec; -*-
//
// Tests for tracecheck.qnt on hand-written traces of two environments, dev -> stg.
module tracecheckTest {
  import logic.* from "./promoter"
  import tracecheck.* from "./tracecheck"

  // `quint test` runs `run`s, which need a state.
  var unused: int
  action init = unused' = 0
  action step = unused' = unused

  pure val G = Map("dev" -> Set(), "stg" -> Set("dev"))
  pure val DT = Map(-1 -> -1, 0 -> 0, 1 -> 1, 2 -> 2)

  pure def view(aSha: int, aDry: int, pSha: int, pDry: int, note: int, gate: str): EnvView = {
    activeSha: aSha, activeDry: aDry, activeHealth: "success",
    proposedSha: pSha, proposedDry: pDry, noteDry: note, proposedGate: gate,
  }
  pure def pr(state: str, sha: int, scm: str): PullRequest =
    { exists: true, specState: state, mergeSha: sha, scm: scm, terminating: false }

  // dev and stg each get the dry-1 commit (11 and 21), in order, gated.
  pure val promoteDry1 = [
    CtpStatus({ env: "dev", view: view(10, 0, 11, 1, 1, "pending") }),
    CtpStatus({ env: "stg", view: view(20, 0, 21, 1, 1, "pending") }),
    PullRequestChange({ env: "dev", pr: pr("open", 11, "none") }),
    PullRequestChange({ env: "dev", pr: pr("open", 11, "open") }),
    PsStatus(Map("dev" -> view(10, 0, 11, 1, 1, "pending"), "stg" -> view(20, 0, 21, 1, 1, "pending"))),
    Gate({ env: "dev", cs: { sha: 11, phase: "success" } }),
    CtpStatus({ env: "dev", view: view(10, 0, 11, 1, 1, "success") }),
    PullRequestChange({ env: "dev", pr: pr("merged", 11, "open") }),
    PullRequestChange({ env: "dev", pr: pr("merged", 11, "merged") }),
    PullRequestChange({ env: "dev", pr: NO_PULL_REQUEST }),
    CtpStatus({ env: "dev", view: view(12, 1, 11, 1, 1, "success") }),
    PullRequestChange({ env: "stg", pr: pr("open", 21, "none") }),
    PullRequestChange({ env: "stg", pr: pr("open", 21, "open") }),
    PsStatus(Map("dev" -> view(12, 1, 11, 1, 1, "success"), "stg" -> view(20, 0, 21, 1, 1, "pending"))),
    Gate({ env: "stg", cs: { sha: 21, phase: "success" } }),
    CtpStatus({ env: "stg", view: view(20, 0, 21, 1, 1, "success") }),
    PullRequestChange({ env: "stg", pr: pr("merged", 21, "open") }),
    PullRequestChange({ env: "stg", pr: pr("merged", 21, "merged") }),
  ]

  run conformingPromotionTest =
    init.expect(checkTrace(G, DT, promoteDry1) == [])

  // The bug fixed in createOrUpdatePullRequest: stg's merge was requested for 21, the hydrator
  // pushed 22 (dry 2), and the next reconcile moved spec.mergeSha to 22 keeping spec.state merged.
  run keptMergeOnNewShaTest = {
    val trace = promoteDry1.slice(0, 17).concat([
      CtpStatus({ env: "stg", view: view(20, 0, 22, 2, 2, "pending") }),
      PullRequestChange({ env: "stg", pr: pr("merged", 22, "open") }),
      PullRequestChange({ env: "stg", pr: pr("merged", 22, "merged") }),
    ])
    init.expect(checkTrace(G, DT, trace) == [
      { at: 18, rule: "pullRequestSpec", env: "stg" },
      { at: 19, rule: "ungated", env: "stg" },
      { at: 19, rule: "outOfOrder", env: "stg" },
    ])
  }

  // A gate that approves stg before dev has dry 1 active.
  run earlyGateTest = {
    val trace = promoteDry1.slice(0, 5).concat([
      Gate({ env: "stg", cs: { sha: 21, phase: "success" } }),
    ])
    init.expect(checkTrace(G, DT, trace) == [{ at: 5, rule: "gate", env: "stg" }])
  }

  // A merge a person made on the SCM, which no PullRequest asked for, is not checked.
  run externalMergeTest = {
    val trace = promoteDry1.slice(0, 4).concat([
      PullRequestChange({ env: "dev", pr: pr("open", 11, "merged") }),
    ])
    init.expect(checkTrace(G, DT, trace) == [])
  }
}
