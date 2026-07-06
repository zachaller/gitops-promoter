---
name: crd-changes
description: Add or modify a CRD field or API type in GitOps Promoter — kubebuilder validation markers, CEL rules, codegen, testdata, docs, and field indexes. Use whenever touching api/v1alpha1/ types or CRD schemas.
---

# Changing CRDs / API types

Full checklist lives in `docs/contributing/maintaining-crds.md`; CEL specifics in `docs/contributing/writing-cel-rules.md`. This is the condensed workflow.

## Workflow for a new/changed field

1. **Edit the Go type** in `api/v1alpha1/<kind>_types.go` with validation markers:
   - Required strings: `+kubebuilder:validation:MinLength=1` (and a MaxLength when sensible).
   - Regex validation for restricted character sets.
   - Numbers that can't be negative: `+kubebuilder:validation:Minimum=0`.
   - Time/duration: use `metav1.Time` / `metav1.Duration`, not custom types.
   - Doc comments become CRD field descriptions — write them for users.
2. **Immutability / CEL** — the one sharp edge:
   - Use `// +k8s:immutable` **only** if the field has no other `XValidation` rules.
   - If the field has any custom CEL, write immutability as explicit `XValidation:rule="self == oldSelf",message="field is immutable"` too.
   - **Never mix `+k8s:` markers with `XValidation` on the same field** — controller-gen emits nondeterministic rule order and CRD bases flap in CI (controller-tools#1429). In-tree example: `PullRequest` `sourceBranch`/`targetBranch`.
   - `// +k8s:enum` on a named string type only if *every* field using that type shares the same allowed values (status fields allowing `""` break this). Otherwise use field-level `Enum` markers.
   - Check CEL cost with `make cel-cost-report` if adding expensive rules.
3. **Regenerate**: `make build-installer` (CRD bases, deepcopy, applyconfiguration, dist bundles). `go mod tidy` if deps changed. Never hand-edit `config/crd/bases/`.
4. **Update testdata**: `internal/controller/testdata/<Kind>.yaml` (PascalCase filename) — realistic spec *and* status values; the strict-unmarshal test (`DisallowUnknownFields`) fails CI on drift.
5. **Update controller logic and tests** (envtest specs for new behavior).
6. **Docs**: update the `### <Kind>` section in `docs/crd-specs.md` (the testdata YAML is embedded via `markdown_include`); run `make lint-docs`.

## Extra steps for a brand-new kind

- Root type gets `//+kubebuilder:object:root=true` plus `+kubebuilder:externalDocs:url="https://gitops-promoter.readthedocs.io/en/stable/crd-specs/#<kind-anchor>",description="CRD reference (examples and behavior)"` — anchor must match the docs heading.
- Add the strict unmarshal test (`//go:embed testdata/<Kind>.yaml` in the controller test).
- If it's a commit-status gate with `spec.promotionStrategyRef`: extend `PromotionStrategyRefIndexValues` and `RegisterGatePromotionStrategyRefFieldIndexes` in `internal/controller/fieldindex.go`; list with `client.MatchingFields{controller.PromotionStrategyRefField: ps.Name}`; register the index on every cache that lists it (manager setup, dashboard read cache in `internal/apiserver/run.go`) and on fake clients in tests.
- Reconciler RBAC via `// +kubebuilder:rbac` markers, then `make manifests`.
- The view aggregation API (`api/view/...`) is served by an extension apiserver, **not** CRDs — it is deliberately excluded from CRD codegen paths in the Makefile.

## ControllerConfiguration fields

Defaults live in **manifests**, not code: add the field, set the default explicitly in `config/config/controllerconfiguration.yaml`, then follow the normal validation/testing steps.

## Verify

```bash
make build-installer && git status --porcelain  # must be clean (CI Check Codegen)
make test-parallel
make lint-docs   # if docs changed
```

While in v1alpha1 breaking changes are allowed but should be avoided when possible.
