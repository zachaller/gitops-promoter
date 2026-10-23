// -*- mode: Bluespec; -*-
//
// A model of how GitOps Promoter moves one dry commit through the environments of a single
// PromotionStrategy. It covers the parts of the controllers that decide whether a hydrated commit
// may merge into an environment's active branch:
//
//   - ChangeTransferPolicy: calculateStatus, gitMergeStrategyOurs, createOrUpdatePullRequest and
//     mergePullRequests (internal/controller/changetransferpolicy_controller.go)
//   - PullRequest: create on the SCM, merge against spec.mergeSha, then delete
//     (internal/controller/pullrequest_controller.go)
//   - PromotionStrategy: copy each ChangeTransferPolicy status into status.environments
//   - DependentsSuccessfulCommitStatus: write the order gate CommitStatus for each environment's
//     proposed hydrated SHA (internal/controller/dependentssuccessfulcommitstatus_controller.go)
//   - the outside world: new dry commits, a hydrator that may be slow, skip dry commits or produce
//     no-op hydrations, and Argo CD reporting health for the active commit.
//
// Every controller acts on what it read earlier, not on the live state. A ChangeTransferPolicy
// reconcile is split into ctpCompute (read git and CommitStatuses) and ctpAct (write the
// PullRequest and status), and other controllers and the hydrator may run in between.
// DependentsSuccessfulCommitStatus reads the PromotionStrategy status, which is itself a copy of
// the ChangeTransferPolicy statuses taken at some earlier point. That is the staleness a real
// informer cache has.
//
// Left out for now: RestoreActiveCommit and the revert gate, PR labels and templates, finalizers,
// history notes, external (human) merges and closes, more than one proposed commit status key,
// commit-time comparisons (dry commits are ordered by number here), and controller restarts.
//
// Run it with `make -C hack/formal check` or see hack/formal/README.md.
module promoter {

  // ----------------------------------------------------------------------------------------------
  // Parameters
  // ----------------------------------------------------------------------------------------------

  // Environment branches of the PromotionStrategy.
  const ENVS: Set[str]
  // The resolved dependsOn graph (explicit dependsOn, or the inferred linear chain).
  const DEPENDS: str -> Set[str]
  // Dry commits are numbered 0 (already everywhere at the start) up to MAX_DRY.
  const MAX_DRY: int
  // true: createOrUpdatePullRequest resets spec.state from merged to open whenever it moves
  // spec.mergeSha, as the code does. false: it keeps spec.state, as the code did before that fix.
  const RESET_MERGE_ON_NEW_SHA: bool

  // ----------------------------------------------------------------------------------------------
  // Types
  // ----------------------------------------------------------------------------------------------

  // A hydrated commit. Hydrated SHAs are opaque ids; dry is the dry SHA it was hydrated from.
  type Commit = { id: int, dry: int }

  // One CommitStatus resource: the SHA it reports on and its spec.phase.
  type CommitStatus = { sha: int, phase: str }

  // What a ChangeTransferPolicy status (and so a PromotionStrategy status.environments entry)
  // records for one environment.
  type EnvView = {
    activeSha: int,
    activeDry: int,
    // Phase of the active commit statuses (argocd-health) for activeSha.
    activeHealth: str,
    proposedSha: int,
    proposedDry: int,
    // Dry SHA from the hydrator's git note on the proposed tip. It runs ahead of proposedDry when
    // the hydrator processed a dry commit without producing a new hydrated commit (a no-op).
    noteDry: int,
    // Phase of the proposed commit statuses (the order gate) for proposedSha.
    proposedGate: str,
  }

  // A PullRequest resource. scm is status.state: "none" until the PR controller creates it on the
  // SCM, then "open", then "merged".
  type PullRequest = { exists: bool, specState: str, mergeSha: int, scm: str }

  // One merge on the SCM, with the property checks evaluated at the moment it happened.
  type Merge = { env: str, sha: int, dry: int, gated: bool, ordered: bool, forward: bool }

  // ----------------------------------------------------------------------------------------------
  // State
  // ----------------------------------------------------------------------------------------------

  // Git and the SCM.
  var dryHead: int // newest dry commit on the dry branch
  var proposed: str -> Commit // tip of each environment's -next (proposed) branch
  var note: str -> int // hydrator note dry SHA on each proposed tip
  var active: str -> Commit // tip of each environment's active branch
  var nextId: int // next unused hydrated commit id
  var scmMerges: Set[Merge] // every merge the SCM performed (history, for the properties)

  // Kubernetes resources.
  var health: str -> CommitStatus // argocd-health CommitStatus per environment
  var gate: str -> CommitStatus // DependentsSuccessfulCommitStatus CommitStatus per environment
  var ctpStatus: str -> EnvView // ChangeTransferPolicy .status per environment
  var psStatus: str -> EnvView // PromotionStrategy .status.environments
  var pr: str -> PullRequest // the PullRequest per environment (one per CTP)

  // In-flight ChangeTransferPolicy reconciles: what ctpCompute read, used later by ctpAct.
  var ctpRead: str -> EnvView
  var ctpInFlight: str -> bool

  // History of facts the properties refer to. Each step adds what was true in the state it
  // started from.
  var everGateGreen: Set[(str, int)] // (env, dry) whose order gate was success for a commit of that dry
  var dryOf: int -> int // dry SHA of every commit that has been a proposed tip
  var everHealthy: Set[(str, int)] // (env, dry) that was active and healthy
  var everNoop: Set[(str, int)] // (env, dry) hydrated as a clean, healthy no-op

  // ----------------------------------------------------------------------------------------------
  // Helpers
  // ----------------------------------------------------------------------------------------------

  val PHASES = Set("pending", "success", "failure")

  // setCommitStatusState: a CommitStatus counts only for the SHA in its spec. Anything else reads
  // as pending ("Waiting for status to be reported").
  pure def phaseFor(cs: CommitStatus, sha: int): str =
    if (cs.sha == sha) cs.phase else "pending"

  // calculateStatus for one environment, against the live git and CommitStatus state.
  def observe(e: str): EnvView = {
    activeSha: active.get(e).id,
    activeDry: active.get(e).dry,
    activeHealth: phaseFor(health.get(e), active.get(e).id),
    proposedSha: proposed.get(e).id,
    proposedDry: proposed.get(e).dry,
    noteDry: note.get(e),
    proposedGate: phaseFor(gate.get(e), proposed.get(e).id),
  }

  // DependentsSuccessfulCommitStatus.isUpstreamPending for upstream u and target dry SHA t, with
  // the result of recursing into u's own upstreams passed in. Quint has no recursion, so the
  // recursion is unrolled below.
  pure def upstreamCheck(st: str -> EnvView, u: str, t: int, recursePending: bool): bool = {
    val v = st.get(u)
    if (v.noteDry != t) {
      // The upstream's hydrator has not processed the target dry SHA yet.
      true
    } else if (v.activeDry == t) {
      // The upstream merged the target: it must be healthy.
      v.activeHealth != "success"
    } else {
      // Only a clean, healthy no-op upstream can be looked past.
      val isNoop = v.noteDry != v.proposedDry
      val hasPendingChanges = v.activeDry != v.proposedDry
      if (not(isNoop) or hasPendingChanges) true
      else if (v.activeHealth != "success") true
      else recursePending
    }
  }

  // Unrolled to depth 3, which covers every graph used in this file. Past that depth an upstream
  // counts as pending, so a deeper graph can only make the model stricter than the code.
  pure def pending1(g: str -> Set[str], st: str -> EnvView, u: str, t: int): bool =
    upstreamCheck(st, u, t, g.get(u).size() > 0)
  pure def pending2(g: str -> Set[str], st: str -> EnvView, u: str, t: int): bool =
    upstreamCheck(st, u, t, g.get(u).exists(w => pending1(g, st, w, t)))
  pure def pending3(g: str -> Set[str], st: str -> EnvView, u: str, t: int): bool =
    upstreamCheck(st, u, t, g.get(u).exists(w => pending2(g, st, w, t)))

  // The order the code is meant to enforce: dry SHA d may land on an environment only after each
  // upstream either had d active and healthy, or hydrated d as a clean no-op and its own upstreams
  // satisfy this rule. Defined over history, because reads may be stale.
  def cleared1(u: str, d: int): bool =
    everHealthy.contains((u, d)) or (everNoop.contains((u, d)) and DEPENDS.get(u).size() == 0)
  def cleared2(u: str, d: int): bool =
    everHealthy.contains((u, d)) or (everNoop.contains((u, d)) and DEPENDS.get(u).forall(w => cleared1(w, d)))
  def cleared3(u: str, d: int): bool =
    everHealthy.contains((u, d)) or (everNoop.contains((u, d)) and DEPENDS.get(u).forall(w => cleared2(w, d)))

  // Facts true in the current state, added to the history on every step.
  def gateGreenNow: Set[(str, int)] =
    ENVS.filter(e => gate.get(e).phase == "success").map(e => (e, dryOf.get(gate.get(e).sha)))
  def isHealthyNow(e: str): bool =
    health.get(e).sha == active.get(e).id and health.get(e).phase == "success"
  def healthyNow: Set[(str, int)] =
    ENVS.filter(e => isHealthyNow(e)).map(e => (e, active.get(e).dry))
  def noopNow: Set[(str, int)] =
    ENVS.filter(e =>
      isHealthyNow(e) and note.get(e) != proposed.get(e).dry and active.get(e).dry == proposed.get(e).dry
    ).map(e => (e, note.get(e)))

  // ----------------------------------------------------------------------------------------------
  // Initial state: dry commit 0 is hydrated, merged and healthy everywhere.
  // ----------------------------------------------------------------------------------------------

  action init = {
    val c0 = { id: 0, dry: 0 }
    val v0 = {
      activeSha: 0, activeDry: 0, activeHealth: "success",
      proposedSha: 0, proposedDry: 0, noteDry: 0, proposedGate: "pending",
    }
    all {
      dryHead' = 0,
      proposed' = ENVS.mapBy(_ => c0),
      note' = ENVS.mapBy(_ => 0),
      active' = ENVS.mapBy(_ => c0),
      nextId' = 1,
      scmMerges' = Set(),
      health' = ENVS.mapBy(_ => { sha: 0, phase: "success" }),
      gate' = ENVS.mapBy(_ => { sha: -1, phase: "pending" }),
      ctpStatus' = ENVS.mapBy(_ => v0),
      psStatus' = ENVS.mapBy(_ => v0),
      pr' = ENVS.mapBy(_ => { exists: false, specState: "open", mergeSha: -1, scm: "none" }),
      ctpRead' = ENVS.mapBy(_ => v0),
      ctpInFlight' = ENVS.mapBy(_ => false),
      everGateGreen' = Set(),
      dryOf' = Map(0 -> 0),
      everHealthy' = ENVS.map(e => (e, 0)),
      everNoop' = Set(),
    }
  }

  // ----------------------------------------------------------------------------------------------
  // The outside world
  // ----------------------------------------------------------------------------------------------

  // A developer pushes a new dry commit.
  action commitDry = all {
    dryHead < MAX_DRY,
    dryHead' = dryHead + 1,
    proposed' = proposed, note' = note, active' = active, nextId' = nextId, scmMerges' = scmMerges,
    health' = health, gate' = gate, ctpStatus' = ctpStatus, psStatus' = psStatus, pr' = pr,
    ctpRead' = ctpRead, ctpInFlight' = ctpInFlight,
  }

  // The hydrator processes the newest dry commit for one environment, possibly skipping older
  // ones. If the environment's manifests did not change, it only moves the git note (a no-op).
  action hydrate(e: str, changed: bool): bool = {
    all {
      note.get(e) < dryHead,
      note' = note.set(e, dryHead),
      if (changed) all {
        proposed' = proposed.set(e, { id: nextId, dry: dryHead }),
        nextId' = nextId + 1,
      } else all {
        proposed' = proposed,
        nextId' = nextId,
      },
      dryHead' = dryHead, active' = active, scmMerges' = scmMerges,
      health' = health, gate' = gate, ctpStatus' = ctpStatus, psStatus' = psStatus, pr' = pr,
      ctpRead' = ctpRead, ctpInFlight' = ctpInFlight,
    }
  }

  // Argo CD reports health for whatever commit is active.
  action reportHealth(e: str, phase: str): bool = {
    all {
      health' = health.set(e, { sha: active.get(e).id, phase: phase }),
      dryHead' = dryHead, proposed' = proposed, note' = note, active' = active, nextId' = nextId,
      scmMerges' = scmMerges, gate' = gate, ctpStatus' = ctpStatus, psStatus' = psStatus, pr' = pr,
      ctpRead' = ctpRead, ctpInFlight' = ctpInFlight,
    }
  }

  // ----------------------------------------------------------------------------------------------
  // ChangeTransferPolicy controller
  // ----------------------------------------------------------------------------------------------

  // calculateStatus: read the branch tips, the note and the CommitStatuses for this environment.
  action ctpCompute(e: str): bool = all {
    not(ctpInFlight.get(e)),
    ctpRead' = ctpRead.set(e, observe(e)),
    ctpInFlight' = ctpInFlight.set(e, true),
    dryHead' = dryHead, proposed' = proposed, note' = note, active' = active, nextId' = nextId,
    scmMerges' = scmMerges, health' = health, gate' = gate, ctpStatus' = ctpStatus,
    psStatus' = psStatus, pr' = pr,
  }

  // gitMergeStrategyOurs: the branches conflict, so the controller pushes a merge commit to the
  // proposed branch (same dry content, new SHA) and requeues without touching the PullRequest.
  action ctpResolveConflict(e: str): bool = {
    val s = ctpRead.get(e)
    all {
      ctpInFlight.get(e),
      s.proposedSha != s.activeSha,
      // git push fails if the branch moved since it was read.
      proposed.get(e).id == s.proposedSha,
      proposed' = proposed.set(e, { id: nextId, dry: proposed.get(e).dry }),
      nextId' = nextId + 1,
      ctpInFlight' = ctpInFlight.set(e, false),
      dryHead' = dryHead, note' = note, active' = active, scmMerges' = scmMerges,
      health' = health, gate' = gate, ctpStatus' = ctpStatus, psStatus' = psStatus, pr' = pr,
      ctpRead' = ctpRead,
    }
  }

  // createOrUpdatePullRequest: the PullRequest the controller applies for what it read.
  pure def upsertPullRequest(s: EnvView, p: PullRequest, resetOnNewSha: bool): PullRequest =
    if (s.proposedDry == s.activeDry) {
      // No promotion needed.
      p
    } else if (p.exists and p.scm == "merged") {
      // pullRequestStatusFreezesSpec: leave a merged PullRequest alone.
      p
    } else if (not(p.exists)) {
      { exists: true, specState: "open", mergeSha: s.proposedSha, scm: "none" }
    } else {
      // prState = existingPR.Spec.State, then spec.mergeSha = Status.Proposed.Hydrated.Sha.
      val keepState = not(resetOnNewSha) or p.mergeSha == s.proposedSha
      { ...p, mergeSha: s.proposedSha, specState: if (keepState) p.specState else "open" }
    }

  // mergePullRequests: set spec.state to merged once every proposed commit status is success.
  // autoMerge is on.
  pure def requestMerge(s: EnvView, p: PullRequest): PullRequest =
    if (s.proposedGate == "success" and p.exists and p.scm == "open" and p.specState == "open")
      { ...p, specState: "merged" }
    else p

  // The rest of the reconcile, acting on what ctpCompute read, and the status write at the end.
  action ctpAct(e: str): bool = {
    val s = ctpRead.get(e)
    all {
      ctpInFlight.get(e),
      pr' = pr.set(e, requestMerge(s, upsertPullRequest(s, pr.get(e), RESET_MERGE_ON_NEW_SHA))),
      ctpStatus' = ctpStatus.set(e, s),
      ctpInFlight' = ctpInFlight.set(e, false),
      dryHead' = dryHead, proposed' = proposed, note' = note, active' = active, nextId' = nextId,
      scmMerges' = scmMerges, health' = health, gate' = gate, psStatus' = psStatus,
      ctpRead' = ctpRead,
    }
  }

  // ----------------------------------------------------------------------------------------------
  // PullRequest controller and the SCM
  // ----------------------------------------------------------------------------------------------

  // Open the pull request on the SCM.
  action prCreate(e: str): bool = all {
    pr.get(e).exists,
    pr.get(e).scm == "none",
    pr' = pr.set(e, { ...pr.get(e), scm: "open" }),
    dryHead' = dryHead, proposed' = proposed, note' = note, active' = active, nextId' = nextId,
    scmMerges' = scmMerges, health' = health, gate' = gate, ctpStatus' = ctpStatus,
    psStatus' = psStatus, ctpRead' = ctpRead, ctpInFlight' = ctpInFlight,
  }

  // Merge on the SCM. The SCM refuses unless the source branch is still at spec.mergeSha. The
  // merge commit on the active branch gets a new SHA and carries the proposed dry SHA.
  action prMerge(e: str): bool = {
    val p = pr.get(e)
    val src = proposed.get(e)
    val m = {
      env: e,
      sha: p.mergeSha,
      dry: src.dry,
      gated: everGateGreen.union(gateGreenNow).contains((e, src.dry)),
      // The merged commit holds the hydrated content of every dry SHA from its own up to the note,
      // so the order is kept if the upstreams cleared any one of them.
      ordered: 0.to(MAX_DRY).exists(d =>
        src.dry <= d and d <= note.get(e) and DEPENDS.get(e).forall(u => cleared3(u, d))
      ),
      forward: src.dry >= active.get(e).dry,
    }
    all {
      p.exists,
      p.scm == "open",
      p.specState == "merged",
      src.id == p.mergeSha,
      active' = active.set(e, { id: nextId, dry: src.dry }),
      nextId' = nextId + 1,
      pr' = pr.set(e, { ...p, scm: "merged" }),
      scmMerges' = scmMerges.union(Set(m)),
      dryHead' = dryHead, proposed' = proposed, note' = note, health' = health, gate' = gate,
      ctpStatus' = ctpStatus, psStatus' = psStatus, ctpRead' = ctpRead, ctpInFlight' = ctpInFlight,
    }
  }

  // A merged PullRequest is deleted; the next promotion gets a new one.
  action prDelete(e: str): bool = all {
    pr.get(e).exists,
    pr.get(e).scm == "merged",
    pr' = pr.set(e, { exists: false, specState: "open", mergeSha: -1, scm: "none" }),
    dryHead' = dryHead, proposed' = proposed, note' = note, active' = active, nextId' = nextId,
    scmMerges' = scmMerges, health' = health, gate' = gate, ctpStatus' = ctpStatus,
    psStatus' = psStatus, ctpRead' = ctpRead, ctpInFlight' = ctpInFlight,
  }

  // ----------------------------------------------------------------------------------------------
  // PromotionStrategy and DependentsSuccessfulCommitStatus controllers
  // ----------------------------------------------------------------------------------------------

  // PromotionStrategy.calculateStatus: copy the ChangeTransferPolicy statuses.
  action psAggregate = all {
    psStatus' = ctpStatus,
    dryHead' = dryHead, proposed' = proposed, note' = note, active' = active, nextId' = nextId,
    scmMerges' = scmMerges, health' = health, gate' = gate, ctpStatus' = ctpStatus, pr' = pr,
    ctpRead' = ctpRead, ctpInFlight' = ctpInFlight,
  }

  // updateDependentsSuccessfulCommitStatus: for every environment with a proposed change, write the
  // gate for its proposed hydrated SHA, success when no upstream is pending for the target dry SHA.
  action dscsEvaluate = all {
    gate' = ENVS.mapBy(e => {
      val v = psStatus.get(e)
      if (v.activeDry == v.proposedDry) {
        // No proposed change: leave the last CommitStatus alone.
        gate.get(e)
      } else {
        // getEffectiveHydratedDrySha
        val t = v.noteDry
        val blocked = DEPENDS.get(e).exists(u => pending3(DEPENDS, psStatus, u, t))
        { sha: v.proposedSha, phase: if (blocked) "pending" else "success" }
      }
    }),
    dryHead' = dryHead, proposed' = proposed, note' = note, active' = active, nextId' = nextId,
    scmMerges' = scmMerges, health' = health, ctpStatus' = ctpStatus, psStatus' = psStatus,
    pr' = pr, ctpRead' = ctpRead, ctpInFlight' = ctpInFlight,
  }

  // ----------------------------------------------------------------------------------------------
  // Steps
  // ----------------------------------------------------------------------------------------------

  // Every step adds to the history what was true in the state it started from.
  action recordHistory = all {
    everGateGreen' = everGateGreen.union(gateGreenNow),
    everHealthy' = everHealthy.union(healthyNow),
    everNoop' = everNoop.union(noopNow),
    dryOf' = ENVS.fold(dryOf, (m, e) => m.put(proposed.get(e).id, proposed.get(e).dry)),
  }

  action step = all {
    recordHistory,
    any {
      commitDry,
      nondet e = ENVS.oneOf()
      nondet changed = Set(true, false).oneOf()
      nondet phase = PHASES.oneOf()
      any {
        hydrate(e, changed),
        reportHealth(e, phase),
        ctpCompute(e),
        ctpResolveConflict(e),
        ctpAct(e),
        prCreate(e),
        prMerge(e),
        prDelete(e),
      },
      psAggregate,
      dscsEvaluate,
    },
  }

  // ----------------------------------------------------------------------------------------------
  // Properties
  // ----------------------------------------------------------------------------------------------

  // Gated merge: the SCM only merges a commit into an environment if the order gate CommitStatus
  // was success, at some point, for a proposed commit of the same dry SHA in that environment.
  // Keyed by dry SHA rather than hydrated SHA because createOrUpdatePullRequest deliberately moves
  // an approved PullRequest's spec.mergeSha to the conflict-resolution merge commit, which has the
  // same content as the commit the gate approved.
  val gatedMerge = scmMerges.forall(m => m.gated)

  // Order: a dry SHA lands on an environment only after every dependsOn upstream had it active and
  // healthy, or passed it through as a clean, healthy no-op. A proposed commit hydrated from dry 1
  // whose note says dry 2 holds dry 2's content too (dry 2 was a no-op for that environment), so
  // either dry SHA counts.
  val promotionOrder = scmMerges.forall(m => m.ordered)

  // An environment's active dry SHA never moves to an older dry commit.
  val noRollback = scmMerges.forall(m => m.forward)

  val safety = gatedMerge and promotionOrder and noRollback

  // Witnesses: these should be violated, which shows the model can reach the interesting states
  // at all. A model where nothing merges satisfies every safety property.
  val witnessLastEnvGetsNewestDry = ENVS.forall(e => active.get(e).dry < MAX_DRY)
  val witnessNoopPassThrough = everNoop.size() == 0
}

// dev -> stg -> prd, the linear chain inferred from spec.environments order, before the fix.
module linear {
  import promoter(
    ENVS = Set("dev", "stg", "prd"),
    DEPENDS = Map("dev" -> Set(), "stg" -> Set("dev"), "prd" -> Set("stg")),
    MAX_DRY = 2,
    RESET_MERGE_ON_NEW_SHA = false,
  ).*
}

// The same chain with createOrUpdatePullRequest resetting spec.state when spec.mergeSha changes,
// as the code does now.
module linearReset {
  import promoter(
    ENVS = Set("dev", "stg", "prd"),
    DEPENDS = Map("dev" -> Set(), "stg" -> Set("dev"), "prd" -> Set("stg")),
    MAX_DRY = 2,
    RESET_MERGE_ON_NEW_SHA = true,
  ).*
}

// The diamond from docs/architecture.md: dev -> {e2e, prf} -> prd.
module diamondReset {
  import promoter(
    ENVS = Set("dev", "e2e", "prf", "prd"),
    DEPENDS = Map("dev" -> Set(), "e2e" -> Set("dev"), "prf" -> Set("dev"), "prd" -> Set("e2e", "prf")),
    MAX_DRY = 2,
    RESET_MERGE_ON_NEW_SHA = true,
  ).*
}
