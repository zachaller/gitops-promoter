# `promoter loadtest`

Repeatably provision (and tear down) one or more independent, fully-wired GitOps Promoter test
environments for generating reconciler load: a real GitHub repository, GitHub App secrets, a
`PromotionStrategy`, and a full set of commit-status gates - `TimedCommitStatus`, a
revert-keyword `GitCommitStatus`, a change-management `WebRequestCommitStatus` trio, and
(optionally) an `ArgoCDCommitStatus`.

This assumes the GitOps Promoter controller is already running (locally via `make run`, or
in-cluster) and pointed at the same kubeconfig context you run these commands against. This tool
does not install the controller or Argo CD itself.

## Commands

- `promoter loadtest setup` - create (or converge) one or more instances.
- `promoter loadtest bump` - commit a no-op change to drive a promotion cycle.
- `promoter loadtest teardown` - delete one or more instances, including their GitHub repos.
- `promoter loadtest change-mgmt-mock` - run a throwaway mock of the external change-management
  service the change-management gate calls (see below).

Run `promoter loadtest [command] --help` for the full flag reference; the root `promoter` command
also accepts the usual `--kubeconfig`/`--context`/`--namespace` kubectl-style flags (see
`promoter loadtest --help`).

## Deploy modes

`--mode` controls how the gating CRDs are deployed and who performs hydration:

- **`direct`** (default) - no Argo CD involved. `setup` applies the gating CRDs straight to the
  cluster and bootstraps the git branches itself, acting as a one-shot "fake hydrator" for the
  `*-next` branches (there's nothing else to do it).
- **`argocd`** - `setup` pushes the gating CRDs into the repo's `promotion-app/` directory and
  creates Argo CD `Application` objects (a plain-sync one for `promotion-app/`, plus one
  per-environment with `sourceHydrator`) so Argo CD performs hydration and reports health via
  `ArgoCDCommitStatus`. Requires Argo CD to already be installed in the cluster.

## Credentials

`setup` needs a GitHub personal access token, a GitHub App ID, and that App's private key (used
by the `ScmProvider`/`GitRepository` to authenticate). `teardown` and `bump` only need the token.

Resolution order (first one found wins), for each credential:

1. Flag (`--github-token`, `--github-app-id`, `--github-app-private-key-path`)
2. Environment variable (`GITHUB_TOKEN`, `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY_PATH`)
3. `setup` only: an interactive prompt (reusing `cmd/demo`'s prompter) for whatever is still
   missing. `teardown`/`bump` error out instead of prompting.

The GitHub token needs permission to create/delete repositories and push to them under `--owner`
(or your own account if `--owner` is unset). The GitHub App needs `Contents`, `Pull requests`,
and `Commit statuses` read/write permissions, and must be installed on `--owner`.

## Quick start (direct mode, one instance)

```bash
export GITHUB_TOKEN=ghp_...
export GITHUB_APP_ID=123456
export GITHUB_APP_PRIVATE_KEY_PATH=/path/to/app.pem

# Creates github.com/<you>/loadtest-repo, a Secret/ScmProvider/GitRepository, a
# PromotionStrategy with all the gates, and bootstraps the git branches.
promoter loadtest setup

# Drive one promotion cycle through the pipeline (commits+pushes a no-op change,
# and - since we're in direct mode - fakes the hydrator for the *-next branches).
promoter loadtest bump

# Keep generating load: a bump every 30s until you Ctrl+C.
promoter loadtest bump --interval 30s

# Tear it all down, including the GitHub repo.
promoter loadtest teardown
```

## Scaling up: `--count`

`--count N` stamps out `N` independent instances in one invocation, named `<name>-1` ..
`<name>-N` (instead of the bare `<name>` used when `--count 1`). Every command
(`setup`/`bump`/`teardown`) needs the same `--name`/`--count`/`--namespace`/`--owner` to know
which instances it's operating on:

```bash
promoter loadtest setup --name loadtest --count 20
promoter loadtest bump  --name loadtest --count 20 --interval 15s
promoter loadtest teardown --name loadtest --count 20
```

`bump` fires all `--count` instances concurrently per tick, so the reconciler sees load from
independent instances roughly simultaneously rather than serialized.

## Argo CD mode

```bash
promoter loadtest setup --mode argocd
promoter loadtest bump  --mode argocd --interval 1m
promoter loadtest teardown --mode argocd
```

`--mode` must match between `setup` and any later `bump`/`teardown` call for the same
instance(s) - `bump` uses it to decide whether to replay the fake-hydrator step, and `teardown`
uses it to know whether to also delete Argo CD `Application`s/`ArgoCDCommitStatus`.

## The change-management gate

Production is gated by three `WebRequestCommitStatus` resources (`change-management-open`,
`-approval`, `-close`) that call out to an external change-management service. By default they
point at `http://localhost:8987/v1/change-management-service`.

`promoter loadtest change-mgmt-mock` runs a throwaway, in-memory stand-in for that service, with
just enough surface area for the three gates to flow from pending to success:

- `POST {prefix}/change/open` - creates a change record, immediately `APPROVED` (unless
  `--approve-delay` is set) with a window covering now. Idempotent per `(asset_id, commit_id)`.
- `GET {prefix}/changes/search?asset_id=...&commit_id=...` - returns matching, still-open records.
- `POST {prefix}/change/close/{id}` - closes a record (excluded from future searches).

Run it alongside the controller (it must be reachable from wherever the controller process runs,
e.g. both on your laptop for `make run`):

```bash
promoter loadtest change-mgmt-mock
# in another terminal:
promoter loadtest setup
promoter loadtest bump --interval 30s
```

It listens on `:8987` by default, matching `setup`'s default `--change-mgmt-base-url`. Change
either side together if you need a different address:

```bash
promoter loadtest change-mgmt-mock --addr :9000
promoter loadtest setup --change-mgmt-base-url http://localhost:9000/v1/change-management-service
```

Use `--approve-delay` to simulate a manual approval step instead of instant auto-approval (records
stay `PENDING` - and the approval gate stays pending - until the delay elapses).

## Tuning the TimedCommitStatus gate

`--timed-durations` (default `30s,1m,2m`) sets the bake-time duration for development, staging,
and production respectively - deliberately short so the gate actually flips to success during a
normal session:

```bash
promoter loadtest setup --timed-durations 10s,10s,10s
```

## Cleanup

`teardown` deletes every Kubernetes resource an instance's `setup` created (`PromotionStrategy`,
the commit-status gates, `GitRepository`/`ScmProvider`, the Secret, and - in argocd mode - the
Argo CD `Application`s/`ArgoCDCommitStatus`) and then deletes the GitHub repository itself. It's
safe to re-run (already-deleted resources are treated as success).

## What gets created, per instance

| Resource | Name |
|---|---|
| GitHub repo | `<name>-repo` |
| Secret | `<name>-scm-secret` |
| ScmProvider | `<name>-scm` |
| GitRepository | `<name>-repo` |
| PromotionStrategy | `<name>-ps` |
| TimedCommitStatus | `<name>-timer` |
| GitCommitStatus | `<name>-revert-check` |
| WebRequestCommitStatus (x3) | `<name>-change-management-{open,approval,close}` |
| ArgoCDCommitStatus (argocd mode only) | `<name>-argocd-health` |
| Argo CD Applications (argocd mode only) | `<name>-{development,staging,production}`, `<name>-promotion-app` |
