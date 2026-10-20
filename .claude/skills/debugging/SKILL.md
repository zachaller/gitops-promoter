---
name: debugging
description: Debug failing or flaky tests, controller reconcile issues, stuck deletes, and promotion problems in GitOps Promoter. Use when investigating test failures, controller misbehavior, missing CommitStatuses, stuck finalizers, or unexplained promotion behavior.
---

# Debugging GitOps Promoter

## Critical rule: controller logs go to stderr

Always capture stderr when running tests, or you will miss all controller-runtime logs:

```bash
# ✅ CORRECT
go test -v ./internal/controller -ginkgo.focus="test name" -ginkgo.v -timeout 5m > /tmp/test.log 2>&1

# ❌ WRONG — loses controller logs
go test -v ./internal/controller -ginkgo.focus="test name" > /tmp/test.log
```

Validate the capture before drawing conclusions: `wc -l /tmp/test.log` — expect 500+ lines with `-ginkgo.v`. If under ~50 lines, the flags are wrong or logs weren't generated. Remember `grep` exit code 1 just means "no match", not an error.

## Investigation workflow

1. **Capture**: run the focused test with `-ginkgo.v 2>&1` to a file in /tmp (never in the repo).
2. **Find failure point**: `grep -A20 "FAILED" /tmp/test.log` and `grep -B20 "FAILED"` for what led up to it.
3. **Verify code paths executed**: grep for the log lines your code emits (e.g. `grep -c "Reconciling ChangeTransferPolicy" /tmp/test.log`). Zero matches on a *passing* test can mean a false positive — the code path never ran.
4. **Flakiness**: `for i in {1..5}; do go test ./internal/controller -ginkgo.focus="test" -timeout 5m || break; done`, or use `make test-parallel-repeat3`. Look for timing indicators: `grep -E "timeout|reconcile.*duration"`.

## Common failure causes

- **"unable to find kubebuilder assets"** → run `make setup-envtest` (or just `make test`, which sets `KUBEBUILDER_ASSETS`).
- **CRDs out of sync with Go types** → `make manifests`; missing deepcopy → `make generate`; CI "Check Codegen" drift → `make build-installer`.
- **Async assertions failing** → controller tests must use Gomega `Eventually` for anything a reconciler does; a bare `Expect` races the reconcile loop.
- **Status oscillating between values** → symptom of two controller replicas racing; check leader election.
- **`status.observedGeneration` < `metadata.generation`** → reconcile hasn't caught up, or the last full status SSA was rejected (the Ready condition then carries the error and its own `observedGeneration` shows the attempted generation). See `docs/contributing/updating-status.md`.

## Stuck deletes (finalizers)

Objects stuck `Terminating` almost always have a leftover finalizer. Finalizer name constants live in `api/v1alpha1/constants.go` (patterns: `<kind>.promoter.argoproj.io/finalizer` for self, `<controller-kind>.promoter.argoproj.io/<finalized-kind>-finalizer` for cross-resource). A classic bug: controller A watches B with generation-only predicates, so it never sees B's finalizer-count change during deletion and A stays stuck in `Terminating`. See `docs/debugging/finalizers.md` and `docs/contributing/using-finalizers.md`.

## Label-based correlation

Controllers correlate resources via labels (keys in `api/v1alpha1/constants.go`):

- `promoter.argoproj.io/promotion-strategy`, `promoter.argoproj.io/environment`, `promoter.argoproj.io/change-transfer-policy` link CTPs and PullRequests to their strategy/environment.
- Gate controllers stamp three standard labels on each CommitStatus via `utils.CommitStatusStandardLabels`, including `promoter.argoproj.io/commit-status` (must match the `key` in the PromotionStrategy's active/proposed commit statuses).

**Gotcha**: label values pass through `utils.KubeSafeLabel()` — slashes become hyphens and length is capped at 63 chars (`environment/development` → `environment-development`). Compare against the sanitized value, not the raw branch name. Full reference: `docs/debugging/labels.md`.

## SCM call visibility

Every SCM HTTP API call should be recorded via `metrics.RecordSCMCall` (`internal/metrics/metrics.go`), which also emits a structured "SCM API call" debug log line. Use `scm_calls_total` / `scm_calls_duration_seconds` metrics and those log lines to confirm whether the operator actually hit the provider API. See `docs/monitoring/logs.md` and `docs/monitoring/metrics.md`.
