# Gating Promotions

Most environment promotion strategies will involve enforcing some kind of "gates" between promotions.

GitOps promoter uses the [PromotionStrategy API](crd-specs.md#promotionstrategy) to configure checks that must pass
between environments. It uses the [CommitStatus API](crd-specs.md#commitstatus) to understand the state of the checks.

A "proposed commit status" is a check which must be passing on a proposed change before it can be merged. To set a 
CommitStatus to be used as a proposed commit status, set the `spec.sha` field to the commit hash of the proposed change
in the proposed (`-next`) environment branch.

An "active commit status" is a check which must be passing on an active (already merged) change before the change can be
merged for the next environment. To set a CommitStatus to be used as an active commit status, set the `spec.sha` field 
to the commit hash of the active change in the live environment branch.

## Example

The following example demonstrates how to configure a PromotionStrategy to use CommitStatuses for both a proposed and
an active commit status check.

```yaml
kind: PromotionStrategy
spec:
  activeCommitStatuses:
    - key: healthy
  environments:
    - branch: environment/dev
    - branch: environment/test
    - branch: environment/prod
      proposedCommitStatuses:
        - key: deployment-freeze
```

In this example, the PromotionStrategy has three environments: `environment/dev`, `environment/test`, and `environment/prod`. All environments
have a `healthy` active commit status check. The `environment/prod` environment has an additional `deployment-freeze` proposed
commit status check.

Suppose the environment branches have been hydrated from the `main` branch and that the branches have the following
commit SHAs:

| Branch                  | SHA      |
|-------------------------|----------|
| `main`                  | `b5d8f7` |
| `environment/dev`       | `a1b2c3` |
| `environment/dev-next`  | `d4e5f6` |
| `environment/test`      | `a7b8c9` |
| `environment/test-next` | `d0e1f2` |
| `environment/prod`      | `a3b4c5` |
| `environment/prod-next` | `d6e7f8` |

For a change to be promoted through all environments, the following CommitStatuses must exist:

```yaml
kind: CommitStatus
metadata:
  labels:
    promoter.argoproj.io/commit-status: healthy
spec:
  sha: a1b2c3  # environment/dev
  phase: success
---
kind: CommitStatus
metadata:
  labels:
    promoter.argoproj.io/commit-status: healthy
spec:
  sha: a7b8c9  # environment/test
  phase: success
---
kind: CommitStatus
metadata:
  labels:
    promoter.argoproj.io/commit-status: healthy
spec:
  sha: a3b4c5  # environment/prod
  phase: success
---
kind: CommitStatus
metadata:
  labels:
    promoter.argoproj.io/commit-status: deployment-freeze
spec:
  sha: d6e7f8  # environment/prod-next
  phase: success
```

Note that all the active commit statuses have SHAs corresponding to the active environment branches, and the proposed
commit status has a SHA corresponding to the proposed (`-next`) environment branch.

Any tool wanting to gate an active commit status must create and update CommitStatuses with the appropriate SHAs for 
the respective environments' live environment branches.

Any tool wanting to gate a proposed commit status must create and update CommitStatuses with the appropriate SHAs for
the respective environments' proposed (`-next`) environment branches.

### How Active Commit Statuses Work (Implementation Details)

Active commit statuses are not enforced by the `ChangeTransferPolicy` directly. Instead, a separate
[`DagCommitStatus`](crd-specs.md#dagcommitstatus) resource declares the dependency graph between environments and produces
a per-environment "previous environment" gate that aggregates the active commit statuses of upstream environments. Users
opt their environments into the gate by listing its key in `proposedCommitStatuses`.

> **Breaking change (v0.x → v0.y):** Earlier versions of GitOps Promoter automatically synthesised this gate inside the
> `PromotionStrategy` controller. That auto-injection has been removed. To preserve the previous-environment gate after
> upgrading, create a `DagCommitStatus` per `PromotionStrategy` (linear `dependsOn` for the existing array order, or a real
> DAG if you want parallel environments) and add `proposedCommitStatuses: [{key: promoter-previous-environment}]` to each
> non-root environment in your `PromotionStrategy.spec.environments`.

For the example above, the linear gate is configured like this:

```yaml
kind: DagCommitStatus
spec:
  promotionStrategyRef:
    name: example-promotion-strategy
  # key defaults to "promoter-previous-environment"; override it if you want a different gate name.
  environments:
    - branch: environment/dev
    - branch: environment/test
      dependsOn: [environment/dev]
    - branch: environment/prod
      dependsOn: [environment/test]
---
kind: PromotionStrategy
spec:
  environments:
    - branch: environment/dev
    - branch: environment/test
      proposedCommitStatuses:
        - key: promoter-previous-environment
    - branch: environment/prod
      proposedCommitStatuses:
        - key: promoter-previous-environment
        - key: deployment-freeze
```

The DAG controller produces one `CommitStatus` per non-root environment whose `spec.sha` is that environment's
proposed (`-next`) hydrated SHA, and whose phase aggregates the active commit statuses of every `dependsOn` parent.
With multiple parents (a true DAG) the phase is `success` only when **every** parent chain is healthy.

So for `environment/test`, the produced `CommitStatus` looks like this:

```yaml
kind: CommitStatus
metadata:
  labels:
    promoter.argoproj.io/commit-status: promoter-previous-environment
    promoter.argoproj.io/dag-commit-status: example-promotion-strategy-dag
spec:
  sha: d0e1f2  # environment/test-next
  phase: success
```

The disaggregated per-parent contributing statuses are mirrored on `DagCommitStatus.status.environments[].parents[]` for
observability; the legacy `promoter.argoproj.io/previous-environment-statuses` annotation on the produced `CommitStatus`
is preserved as a flat key→phase map for back-compat readers.

#### Previous Environment CommitStatus URL

Since the previous environment CommitStatus aggregates the active commit status checks of the parent environment(s), it
is nontrivial to determine what URL to use for the aggregate CommitStatus.

If exactly one parent contributes exactly one commit status, the gate's URL is set to that status's URL. Otherwise, no
URL is set. This behavior may change in the future.

## Built-in CommitStatus Controllers

GitOps Promoter provides several built-in controllers that automatically create and manage CommitStatus resources based on various criteria:

### Argo CD Health Status

The [ArgoCDCommitStatus](commit-status-controllers/argocd.md) controller monitors Argo CD Applications and creates CommitStatus resources based on application health. This enables gating promotions based on whether applications are healthy in their current environment.

Key features:

- Monitors Argo CD Applications with specific labels
- Creates CommitStatus resources with key `argocd-health`
- Reports application health status (Healthy, Progressing, Degraded, etc.)

### Time-Based Gating

The [TimedCommitStatus](commit-status-controllers/timed.md) controller implements "soak time" or "bake time" requirements, ensuring changes run in lower environments for a minimum duration before being promoted.

Key features:

- Monitors how long commits have been running in each environment
- Creates CommitStatus resources with key `timer`
- Reports pending until the required duration is met
- Prevents promotions when there are pending changes in lower environments

### Web Request (HTTP) Validation

The [WebRequestCommitStatus](commit-status-controllers/web-request.md) controller gates promotions on external HTTP/HTTPS APIs. It calls configurable endpoints, evaluates the response with expressions, and creates CommitStatus resources so the SCM shows success or pending.

Key features:

- **Polling or trigger mode:** Poll at an interval or only when a trigger expression fires (e.g. when SHA changes)
- **Validation expression:** Uses the [expr](https://github.com/expr-lang/expr) language; `true` means validation passed (CommitStatus phase success), `false` means pending
- **Optional response expression:** Extract a subset of the HTTP response into `ResponseOutput` for use in the next trigger evaluation and in description/URL templates
- **TriggerOutput:** Trigger when.output expression can return extra fields that are stored and available on the next run and in templates
- **SuccessOutput:** Success when.output expression can return extra fields that are stored and available on the next run in trigger, success expressions, and templates
- **Shared expr (`when.variables`):** Optional map expression whose result is available as **`Variables`** to `when.expression` and `when.output.expression` on the same `when` block (trigger and success); see [Web Request Commit Status](commit-status-controllers/web-request.md#shared-trigger-and-success-expr-whenvariables)
- **Templated URL, headers, body:** Go templates with `Branch`, `Phase`, `PromotionStrategy`, `WebRequestCommitStatus`, `TriggerOutput`, `ResponseOutput`, `SuccessOutput`, namespace metadata, etc.
- **Authentication:** Basic, Bearer, OAuth2, or mutual TLS via Secrets
- **reportOn:** Report on the proposed commit (default) or the active (deployed) commit

### Custom Controllers

You can also create your own controllers that manage CommitStatus resources. Any system that can create Kubernetes resources can participate in the gating logic by creating CommitStatus resources with the appropriate SHAs and phases.

