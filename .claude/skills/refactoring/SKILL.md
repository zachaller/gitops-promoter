---
name: refactoring
description: Safely refactor GitOps Promoter code — what is generated vs hand-written, which invariants must survive a refactor, and the verification loop to run afterward. Use when restructuring code, renaming, extracting helpers, or cleaning up without changing behavior.
---

# Refactoring GitOps Promoter

## Never hand-edit generated code

Regenerate instead of editing:

| Files | Regenerate with |
| --- | --- |
| `api/v1alpha1/zz_generated.*.go`, `applyconfiguration/` | `make generate` |
| `config/crd/bases/*.yaml`, `config/rbac/role.yaml`, `dist/` | `make manifests` / `make build-installer` |
| Mocks (`internal/scms/mock/`, mockery output) | `make mockery-gen` |
| `test/external_crds/argoproj.io_applications.yaml` | moved there by `make manifests` |

`make build-installer` runs the full codegen chain; CI's "Check Codegen" job fails on any drift, so run it whenever you touch API types, kubebuilder markers, or RBAC markers.

## Invariants that must survive a refactor

- **Public strings are API**: finalizer names, label keys (`api/v1alpha1/constants.go`), CommitStatus `key` values, and field-owner names must not change — renaming a finalizer strands objects that still carry the old string; changing a field owner breaks SSA ownership of existing status.
- **Status flow**: keep the deferred `utils.HandleReconciliationResult` at the top of `Reconcile`, keep the early `meta.RemoveStatusCondition(..., Ready)` after fetching the object, and never introduce direct `r.Status().Update/Patch` calls.
- **Field-index lookups**: code that lists gates by strategy must keep using `client.MatchingFields` with the indexes in `internal/controller/fieldindex.go`; don't "simplify" to a namespace list + in-memory filter.
- **SCM metrics**: any moved/extracted SCM API call must keep its `metrics.RecordSCMCall` exactly once per logical request.
- **RBAC markers** (`// +kubebuilder:rbac`) live on the controller that needs them — moving code between controllers may require moving markers, then `make manifests`.
- Kubebuilder validation markers and CEL `XValidation` comments are load-bearing — don't reflow or drop doc comments on API types casually (they become CRD descriptions).

## Conventions to preserve

- Error handling: wrap with `fmt.Errorf("...: %w", err)`; never ignore errors; no naked returns.
- Structured logging with `logr`; keep existing log messages stable where tests or docs grep for them.
- Follow existing controllers/providers as templates rather than introducing a new style; match the surrounding code's comment density and idiom.
- Avoid adding dependencies; `hack/celcost` is deliberately its own Go module to keep CEL/apiserver deps out of the root module.

## Verification loop

```bash
make build-installer   # codegen up to date (catches marker/API drift)
make fmt vet           # formatting and vet
make lint              # golangci-lint (config in .golangci.toml); make lint-fix for autofixes
make test-parallel     # full envtest suite
make deadcode          # if you removed/moved exported funcs
make lint-ui           # only if you touched ui/
```

For behavior-sensitive areas, run `make test-parallel-repeat3` to catch introduced flakiness. Commit messages / PR titles follow Conventional Commits — use `refactor:` (optionally scoped, e.g. `refactor(controller): ...`).
