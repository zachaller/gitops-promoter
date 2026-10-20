---
name: code-review
description: Review GitOps Promoter pull requests and diffs against this repo's specific conventions — status/SSA rules, CRD validation markers, codegen drift, SCM metrics, finalizers, and test coverage. Use when reviewing a PR, a branch diff, or pre-commit changes.
---

# Reviewing GitOps Promoter changes

Review in this order: correctness bugs first, then repo-convention violations, then style. Cite `file:line` for every finding.

## Repo-specific checklist

### API / CRD changes (`api/v1alpha1/`)

- Generated files in sync? A diff touching Go types **must** include regenerated `zz_generated.*`, `config/crd/bases/`, `applyconfiguration/`, and `dist/` (CI "Check Codegen" runs `make build-installer`).
- Validation markers present: required strings get `MinLength=1`; max lengths where sensible; regex for restricted charsets; non-negative numbers get `Minimum=0`; time/duration fields use `metav1` types.
- Immutability: `// +k8s:immutable` **only** when the field has no other `XValidation`; otherwise explicit `XValidation:rule="self == oldSelf"`. Never mix `+k8s:` markers with `XValidation` on one field (controller-tools#1429 — nondeterministic rule order flaps CI).
- `internal/controller/testdata/<Kind>.yaml` updated for both spec and status changes (strict-unmarshal tests fail otherwise); `docs/crd-specs.md` section updated; `+kubebuilder:externalDocs` on new root types.
- New `ControllerConfiguration` fields: default set explicitly in `config/config/controllerconfiguration.yaml` (defaults live in manifests, not code).

### Controller changes (`internal/controller/`)

- No direct `r.Status().Update()` / `r.Status().Patch()` — status flows only through the deferred `utils.HandleReconciliationResult`.
- Ready condition cleared right after fetch (`meta.RemoveStatusCondition(..., Ready)`).
- NotFound handled gracefully; requeues via `ctrl.Result{RequeueAfter: ...}`; structured `logr` logging.
- Finalizer names come from `api/v1alpha1/constants.go` and follow the naming patterns; cross-resource finalizer updates use SSA, not `Update` (Secrets are the documented exception). Watches on dependencies whose finalizers matter during deletion must not be generation-only-filtered.
- Gate lookups use field indexes (`client.MatchingFields` + `fieldindex.go`), not list-and-filter.
- RBAC changes are `// +kubebuilder:rbac` markers + regenerated role.yaml, not hand edits.

### SCM changes (`internal/scms/`)

- Every provider HTTP API call records `metrics.RecordSCMCall` exactly once per logical request, with a real or sensible fallback status code (`500` when no response), after the call completes.
- Errors wrapped with `%w`; follows the structure of an existing provider (e.g. `internal/scms/github/`).

### Tests

- Behavior changes come with envtest specs; async assertions use `Eventually`, not bare `Expect`.
- Both success and error paths covered; cleanup in `AfterEach`.
- Fake clients that use `MatchingFields` register the matching field indexes.

### Docs & hygiene

- Behavior/API changes update `docs/` (and `mkdocs.yml` for new pages); `make lint-docs` clean.
- PR title follows Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`, `test:`, `refactor:`, `perf:`, `ci:`, `build:`, `style:`, optional scope).
- No new dependencies without justification; no edits to generated files; minimal diff for the stated goal.

## Verification commands for a review

```bash
make build-installer && git status --porcelain   # codegen drift check
make lint
make test-parallel
```
