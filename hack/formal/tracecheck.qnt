// -*- mode: Bluespec; -*-
//
// Checks a run of the real controllers, recorded by test/tracerecord and converted by
// hack/formal/tracecheck, against the decisions in the `logic` module of promoter.qnt.
//
// A trace is the sequence of changes the API server stored for one PromotionStrategy, ordered by
// resourceVersion. Every change of interest becomes an Event. The check walks the events in order
// and reports each one the model cannot explain:
//
//   - gate: a DependentsSuccessfulCommitStatus CommitStatus write that gateFor does not produce
//     from any PromotionStrategy status stored before it (the controller reads a cache that may be
//     stale, never one from the future).
//   - pullRequestSpec: a change to a PullRequest's existence, spec.state or spec.mergeSha that
//     neither upsertPullRequest nor requestMerge produces from the ChangeTransferPolicy status of
//     that reconcile and a PullRequest version the controller could have read.
//   - mergeNotRequested, ungated, outOfOrder, rollback: a merge on the SCM that breaks the model's
//     safety properties. Merges no PullRequest asked for (a person merged on the SCM) are skipped.
//
// It does not check what the controllers did not do: the model allows any controller to sit idle.
module tracecheck {
  import logic.* from "./promoter"

  type Event =
    // A ChangeTransferPolicy status: the environment's view.
    | CtpStatus({ env: str, view: EnvView })
    // A PromotionStrategy status: every environment's view it lists.
    | PsStatus(str -> EnvView)
    // A gate CommitStatus spec.
    | Gate({ env: str, cs: CommitStatus })
    // The environment's PullRequest. exists is false once it is deleted.
    | PullRequestChange({ env: str, pr: PullRequest })

  type Violation = { at: int, rule: str, env: str }

  val NO_VIEW: EnvView = {
    activeSha: -1, activeDry: -1, activeHealth: "pending",
    proposedSha: -1, proposedDry: -1, noteDry: -1, proposedGate: "pending",
  }

  type State = {
    ctpView: str -> EnvView, // latest ChangeTransferPolicy status per environment
    psViews: List[str -> EnvView], // every PromotionStrategy status so far
    pr: str -> PullRequest, // latest PullRequest per environment
    // PullRequest versions since its last spec change, the change included. The controller reads
    // one of these from its cache. An older one would undo its own last write.
    prSince: str -> List[PullRequest],
    mergeRequested: str -> Set[int], // mergeShas the environment's PullRequest asked to merge
    dryOf: int -> int, // dry SHA of every proposed hydrated SHA seen
    gateGreen: Set[(str, int)], // (env, dry) whose gate was success
    healthy: Set[(str, int)],
    noop: Set[(str, int)],
    violations: List[Violation],
  }

  pure def initialState(envs: Set[str]): State = {
    ctpView: Map(),
    psViews: [],
    pr: envs.mapBy(_ => NO_PULL_REQUEST),
    prSince: envs.mapBy(_ => [NO_PULL_REQUEST]),
    mergeRequested: envs.mapBy(_ => Set()),
    dryOf: Map(),
    gateGreen: Set(),
    healthy: Set(),
    noop: Set(),
    violations: [],
  }

  pure def addView(s: State, e: str, v: EnvView): State = {
    ...s,
    dryOf: s.dryOf.put(v.proposedSha, v.proposedDry),
    healthy: s.healthy.union(healthyFacts(e, v)),
    noop: s.noop.union(noopFacts(e, v)),
  }

  pure def violate(s: State, at: int, rule: str, e: str): State =
    { ...s, violations: s.violations.append({ at: at, rule: rule, env: e }) }

  pure def sameSpec(a: PullRequest, b: PullRequest): bool =
    a.exists == b.exists and (not(a.exists) or (a.specState == b.specState and a.mergeSha == b.mergeSha))

  // The ChangeTransferPolicy status a reconcile acts on is written at the end of the reconcile, so
  // a PullRequest write at index i used the latest status before i, or the first one after it.
  pure def viewsAround(trace: List[Event], s: State, e: str, i: int): List[EnvView] = {
    val before = if (s.ctpView.keys().contains(e)) [s.ctpView.get(e)] else []
    val after = range(i + 1, trace.length()).foldl([], (acc, j) =>
      if (acc.length() > 0) acc
      else match trace[j] {
        | CtpStatus(c) => if (c.env == e) [c.view] else acc
        | PsStatus(_) => acc
        | Gate(_) => acc
        | PullRequestChange(_) => acc
      }
    )
    before.concat(after)
  }

  pure def onGate(g: str -> Set[str], dt: int -> int, s: State, i: int, e: str, cs: CommitStatus): State = {
    val explained = s.psViews.foldl(false, (found, st) =>
      found or { val w = gateFor(g, st, dt, e) w.write and w.cs == cs }
    )
    val s1 = if (explained) s else violate(s, i, "gate", e)
    if (cs.phase == "success" and s1.dryOf.keys().contains(cs.sha))
      { ...s1, gateGreen: s1.gateGreen.union(Set((e, s1.dryOf.get(cs.sha)))) }
    else s1
  }

  pure def onMerge(g: str -> Set[str], dryIds: Set[int], s: State, i: int, e: str, p: PullRequest): State =
    if (not(s.mergeRequested.get(e).contains(p.mergeSha))) {
      // Nothing asked to merge this SHA: a person merged on the SCM.
      s
    } else if (not(s.dryOf.keys().contains(p.mergeSha))) {
      violate(s, i, "mergeOfUnknownCommit", e)
    } else {
      val dry = s.dryOf.get(p.mergeSha)
      val known = s.ctpView.keys().contains(e)
      val v = if (known) s.ctpView.get(e) else { ...NO_VIEW, proposedSha: p.mergeSha, noteDry: dry }
      val note = if (v.proposedSha == p.mergeSha) v.noteDry else dry
      val ds = dryIds.filter(d => dry <= d and d <= note).union(Set(dry, note))
      val s1 = if (s.gateGreen.contains((e, dry))) s else violate(s, i, "ungated", e)
      val s2 = if (orderKept(g, s.healthy, s.noop, e, ds)) s1 else violate(s1, i, "outOfOrder", e)
      if (dry >= v.activeDry) s2 else violate(s2, i, "rollback", e)
    }

  pure def onPullRequest(
    g: str -> Set[str], dryIds: Set[int], trace: List[Event], s: State, i: int, e: str, p: PullRequest
  ): State = {
    val prev = s.pr.get(e)
    val specChanged = not(sameSpec(prev, p)) and p.exists
    val s1 = if (not(specChanged)) s else {
      val explained = viewsAround(trace, s, e, i).foldl(false, (found, v) =>
        found or s.prSince.get(e).foldl(false, (f, read) =>
          f or sameSpec(p, upsertPullRequest(v, read, true)) or sameSpec(p, requestMerge(v, read))
        )
      )
      if (explained) s else violate(s, i, "pullRequestSpec", e)
    }
    val merged = p.exists and p.scm == "merged" and prev.scm != "merged"
    val s2 = if (merged) onMerge(g, dryIds, s1, i, e, p) else s1
    if (not(p.exists)) {
      { ...s2, pr: s2.pr.set(e, NO_PULL_REQUEST), prSince: s2.prSince.set(e, [NO_PULL_REQUEST]),
        mergeRequested: s2.mergeRequested.set(e, Set()) }
    } else {
      {
        ...s2,
        pr: s2.pr.set(e, p),
        prSince: s2.prSince.set(e, if (specChanged) [p] else s2.prSince.get(e).append(p)),
        mergeRequested: if (p.specState == "merged")
          s2.mergeRequested.set(e, s2.mergeRequested.get(e).union(Set(p.mergeSha)))
        else s2.mergeRequested,
      }
    }
  }

  // g: the resolved dependsOn graph. dt: dry SHA to commit-time order. dryIds: every dry SHA.
  pure def checkTrace(g: str -> Set[str], dt: int -> int, trace: List[Event]): List[Violation] = {
    val final = range(0, trace.length()).foldl(initialState(g.keys()), (s, i) =>
      match trace[i] {
        | CtpStatus(c) => { ...addView(s, c.env, c.view), ctpView: s.ctpView.put(c.env, c.view) }
        | PsStatus(views) => {
            val s1 = views.keys().fold(s, (acc, e) => addView(acc, e, views.get(e)))
            { ...s1, psViews: s1.psViews.append(views) }
          }
        | Gate(w) => onGate(g, dt, s, i, w.env, w.cs)
        | PullRequestChange(c) => onPullRequest(g, dt.keys(), trace, s, i, c.env, c.pr)
      }
    )
    final.violations
  }
}
