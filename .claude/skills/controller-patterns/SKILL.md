---
name: controller-patterns
description: Write or modify GitOps Promoter reconcilers correctly — status via SSA chokepoint, Ready conditions, observedGeneration, finalizer conventions, watches, and field indexes. Use when writing, editing, or wiring up controller code in internal/controller/.
---

# Controller patterns

Authoritative docs: `docs/contributing/updating-status.md` and `docs/contributing/using-finalizers.md`. Follow an existing reconciler (e.g. `promotionstrategy_controller.go`) as a template.

## Status: one chokepoint, SSA only

1. Start every `Reconcile` with the deferred helper:
   ```go
   defer utils.HandleReconciliationResult(ctx, startTime, &obj,
       r.Client, r.Recorder,
       constants.<Kind>ControllerFieldOwner,
       &result, &err)
   ```
2. Immediately after fetching the object, **clear the stale Ready condition**:
   ```go
   meta.RemoveStatusCondition(obj.GetConditions(), string(promoterConditions.Ready))
   ```
   The deferred helper can't tell "set this reconcile" from "left over from last time" — skipping this bleeds stale state forward.
3. Mutate `obj.Status` in memory during reconciliation. **Never call `r.Status().Update()` or `r.Status().Patch()` yourself.**
4. The helper sets Ready from `*err`, stamps `status.observedGeneration = metadata.generation`, and applies the whole status via SSA with `ForceOwnership` under the controller's field owner. If validation rejects the full apply, it retries conditions-only under a `<owner>-fallback` field owner without advancing `observedGeneration` (that's the "stored status is stale" signal).

`observedGeneration` semantics: `== generation` means status is current; `< generation` means not caught up or last apply rejected; oscillating values mean two replicas are racing (check leader election).

## Finalizers

- Define strings as constants in `api/v1alpha1/constants.go`. Patterns:
  - Own lifecycle: `<kind>.promoter.argoproj.io/finalizer`
  - Placed by another controller: `<controller-kind>.promoter.argoproj.io/<finalized-kind>-finalizer`
- Names are API — never change a shipped finalizer string (strands objects).
- The controller that owns the cleanup adds *and* removes its finalizer. Standard flow: add on reconcile when not deleting (`controllerutil.AddFinalizer` + `Update`); on `deletionTimestamp`, run cleanup then remove.
- **Cross-resource** finalizer add/remove uses SSA (`ApplyPatchType`, typed apply config, stable `client.FieldOwner`), no `RetryOnConflict` needed. Exception: Secret finalizers for ScmProvider still use `controllerutil` + `Update` + `RetryOnConflict`.
- Deletion-ordering trap: if controller A waits on B's finalizers but watches B with generation-only predicates, A gets stuck `Terminating`. Add a predicate whose Update path returns true when `B.DeletionTimestamp != nil` and `len(B.Finalizers)` changed.

## Watches, indexes, and lookups

- Gate controllers watch `PromotionStrategy` and list their kind via `client.MatchingFields{controller.PromotionStrategyRefField: ps.Name}` — never namespace-list and filter in memory. Indexes are registered in `internal/controller/fieldindex.go` and must be registered on every cache that uses them (manager, dashboard read cache in `internal/apiserver/run.go`, fake clients in tests).
- Requeue with `ctrl.Result{RequeueAfter: duration}`; handle NotFound gracefully (`client.IgnoreNotFound` pattern).
- Emit events via the shared helpers in `event_utils.go`; use structured `logr` logging.

## Labels

Set correlation labels using constants from `api/v1alpha1/constants.go`; gate controllers stamp the three standard CommitStatus labels via `utils.CommitStatusStandardLabels(parent, branch, key)`. Always sanitize free-form values (branch names) with `utils.KubeSafeLabel()`. Reference table: `docs/debugging/labels.md`.

## RBAC

Declare permissions as `// +kubebuilder:rbac:...` markers on the reconciler, then `make manifests` — never edit `config/rbac/role.yaml` directly.
