# Promotion Policies

A promotion policy decides **which change** an environment promotes when several are waiting. It is
set per environment on the `PromotionStrategy`, and it is the knob to reach for when later
environments in a chain stop deploying.

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: PromotionStrategy
metadata:
  name: example-promotion-strategy
spec:
  gitRepositoryRef:
    name: example-git-repo
  promotionPolicy: LatestVerified   # default for every environment below
  activeCommitStatuses:
    - key: argocd-health
  environments:
    - branch: environment/dev
      promotionPolicy: Latest       # per-environment override
    - branch: environment/test
    - branch: environment/prod
```

## The problem it solves

Your hydrator writes one hydrated commit per dry commit to *every* environment's proposed branch, so
`environment/prod-next` is not a single pending change — it is a stream of candidates, and its tip is
always the newest dry commit.

Under the default `Latest` policy, an environment promotes that tip. Its gate is "is the preceding
environment healthy on the tip right now?" The tip is also the change the preceding environment has
had the least time to verify, so when dry commits arrive faster than an environment can sync and
report health, that gate is never satisfied at the moment it is evaluated:

| Time | `main` | dev is verifying | prod is trying to promote | prod's gate |
|------|--------|------------------|---------------------------|-------------|
| t0   | `c1`   | `c1`             | `c1`                      | dev not healthy yet |
| t1   | `c2`   | `c2`             | `c2`                      | dev not healthy yet |
| t2   | `c3`   | `c3`             | `c3`                      | dev not healthy yet |

Every change dev finishes verifying has already been replaced by a newer one by the time prod looks.
Production starves — not because nothing is good, but because the only thing it is ever offered is
the least-validated change available. Bake times (see
[TimedCommitStatus](gating-promotions/built-in-gates/timed-commit-status.md)) make this worse, since
each new active commit restarts the clock.

## The policies

### `Latest` (default)

Always promotes the newest candidate. This is the behavior of releases before promotion policies
existed, and it is the right choice when your dry branch changes slowly enough that each change can
be verified before the next arrives — most obviously for the first environment in a chain, which has
nothing upstream to wait for.

### `LatestVerified`

Promotes the newest candidate that **every preceding environment has verified**, skipping over newer
but unverified ones. In the table above, prod would promote `c1` as soon as dev finished verifying
it, then `c2`, and so on: it advances to the high-water mark of verified change rather than chasing
the tip. Production runs one verified step behind development instead of standing still.

This is the policy to use when a later environment is starving.

### `Sequential`

Promotes the **oldest** candidate that has not been promoted yet, again only once every preceding
environment has verified it. Unlike `LatestVerified` it never skips a change, so every dry commit is
promoted through the environment in order and appears in its history.

Use it when each change has to be observed individually — a manual production approval where the
reviewer expects to see one change per pull request, for instance. Expect the environment to fall
further behind the tip under churn, since it works through the queue one change at a time.

## What "verified" means

An environment verifies a change when that change is active in it and **every one of the environment's
active commit statuses is passing**. An environment with no active commit statuses configured gates on
nothing, so everything it runs counts as verified.

Verifications are **kept after the environment moves on** to a newer change. That is what makes the
whole thing work: a change stays promotable on the strength of a verification that has since been
superseded. Without it, a churning environment would never hand anything downstream, because it is
almost never healthy on a given change at the exact moment a later environment looks.

A change must be verified by *every* environment before the one promoting it, not just the immediately
preceding one.

### Where the record lives

The record is kept on each environment's `ChangeTransferPolicy` at `status.verification`, and surfaced
on the `PromotionStrategy` at `status.environments[].lastHealthyDryShas`. It holds the 50 most recent
verifications per environment, which bounds how far behind a downstream environment can fall and still
catch up.

It has two halves, which say deliberately different things:

- **Changes the environment has moved past** come from git. Every promotion records the outgoing
  change's health in its merge commit on the active branch — the controller refreshes the pull request
  body and merges it in the same reconcile, so those statuses describe health *at the moment the
  environment stopped running the change*. Nothing is accumulated: the record is rebuilt by walking
  the branch whenever the status is lost, so a deleted and recreated resource costs nothing permanent.
- **The change running right now** has no merge commit yet, so it is judged on live health and
  composed in when the record is read. This half is what closes the lag: its evidence only reaches git
  at the next promotion, and without it a later environment would only ever be offered changes that
  are already one promotion stale — which under churn is the whole problem.

Because the live half is composed rather than stored, an environment that goes unhealthy stops
vouching for what it is running. A transient green never becomes a permanent verification.

Walking the active branch, a commit falls into one of three buckets:

| Commit | Read as |
|---|---|
| Promoter trailers showing every active status passing | verified |
| Promoter trailers with any status not passing | not verified |
| No promoter trailers at all — a direct push, a squash merge that dropped the message, a hand-edited message | **unknown**: skipped, and the walk continues past it |

Unknown is skipped rather than treated as a stopping point, so a single manual push to an environment
branch cannot blind the record behind it. It is also safe in the right direction: an unknown commit can
only ever withhold a promotion, never grant one. Skipped commits still consume the walk budget, so a
repository with heavy direct pushing has a shorter effective record than the 50-entry cap suggests.

## The promotion branch

`Latest` promotes the proposed branch tip, so it opens its pull request directly from
`environment/<env>-next`.

The other policies cannot, because that branch belongs to the hydrator and always moves to the newest
change. Instead the controller picks a commit out of the proposed branch's history and force-pushes it
to a branch it owns, `environment/<env>-promote`, and opens the pull request from there:

| Branch | Owner | Contents |
|--------|-------|----------|
| `environment/prod-next` | hydrator | every candidate, newest at the tip |
| `environment/prod-promote` | GitOps Promoter | the one candidate currently being promoted |
| `environment/prod` | promotion (pull request merge) | what is deployed |

With `activePath` set, the promotion branch follows the same convention as the proposed branch:
`environment/prod-promote/<activePath>`.

!!! warning
    Nothing other than GitOps Promoter may write to the promotion branch. The controller force-pushes
    it whenever its selection changes, and any other commit on it will be discarded.

## Observing the lag

When a policy selects candidates, `status.environments[]` reports both ends of the gap:

- `candidate` — the tip of the hydrator's branch: the newest change that exists.
- `proposed` — the change actually being promoted.
- `active` — the change currently deployed.

`candidate.dry.sha` differing from `proposed.dry.sha` is normal and expected under churn: it is the
policy deliberately declining to chase the tip. A promotion is announced with a
`PromotionCandidateSelected` event naming both the selected and the newest available change.

## Choosing a policy

| Situation | Policy |
|-----------|--------|
| Dry commits arrive slower than environments verify them | `Latest` |
| First environment in the chain | `Latest` |
| Later environment starving under churn | `LatestVerified` |
| Every change must be promoted individually | `Sequential` |

Policies are per environment, and mixing them is normal: `Latest` for development so it tracks the
tip, `LatestVerified` for everything downstream.
