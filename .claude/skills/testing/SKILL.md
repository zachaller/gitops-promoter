---
name: testing
description: Write and run tests for GitOps Promoter — Ginkgo/Gomega envtest controller suites, e2e tests, fuzz targets, and flake checking. Use when adding tests, running the test suite, or deciding what kind of test a change needs.
---

# Testing GitOps Promoter

## Running tests

```bash
make test                    # manifests + generate + fmt + vet + envtest, then all unit/controller tests
make test-parallel           # ginkgo -p -procs=4 over internal/ (faster; what CI-style runs use)
make test-parallel-repeat3   # parallel suite repeated to catch flakes
make test-e2e                # e2e tests (needs a Kind cluster; go test ./test/e2e/ -v)
make fuzz-replay             # regression-replay fuzz seeds/corpus (packages in FUZZ_PACKAGES)
make fuzz-explore            # bounded exploratory fuzzing, FUZZ_TIME per target
```

Run a single spec:

```bash
go test -v ./internal/controller -ginkgo.focus="exact spec name" -ginkgo.v -timeout 5m 2>&1 | tail -50
```

If tests fail with "unable to find kubebuilder assets", run `make setup-envtest`. `ENVTEST_K8S_VERSION` is pinned in the Makefile.

## Test conventions

- **Framework**: Ginkgo/Gomega with `Describe`/`Context`/`It` blocks. Controller tests run against **envtest** (real API server, no kubelet) — see `internal/controller/suite_test.go`.
- **Async**: anything a reconciler does is asynchronous — assert with `Eventually(...)`, never a bare `Expect` right after creating/updating an object.
- Test files sit next to the code (`*_test.go`); controller tests are `internal/controller/<kind>_controller_controller_test.go` style, e2e lives in `test/e2e/`.
- Use table-driven tests for multiple similar cases; test both success and error paths; clean up in `AfterEach`.
- Mock external dependencies; mocks are generated with mockery (`make mockery-gen`), fake SCM provider lives in `internal/scms/fake/`.
- Fake clients used with `client.MatchingFields` must register the same field indexes as the manager — follow `newFakeClientBuilder()` in `internal/apiserver/builder_test.go` and `internal/controller/fieldindex.go`.

## Strict unmarshal tests for CRDs

Every CRD kind has an example manifest in `internal/controller/testdata/<Kind>.yaml` (PascalCase, realistic spec **and** status) that is `go:embed`-ed into the controller test and decoded with `unmarshalYamlStrict` (`DisallowUnknownFields`). When you add or change a CRD field, update the testdata YAML or CI fails. Pattern:

```go
//go:embed testdata/<Kind>.yaml
var test<Kind>YAML string

It("should unmarshal the <Kind> resource", func() {
    Expect(unmarshalYamlStrict(test<Kind>YAML, &promoterv1alpha1.<Kind>{})).To(Succeed())
})
```

## Debugging failures

Controller logs go to **stderr** — always capture with `2>&1` and use `-ginkgo.v`. See the `debugging` skill for the full log-capture and flake-investigation protocol.

## What a change needs

- New/changed CRD field → update `internal/controller/testdata/`, regen with `make build-installer`, strict unmarshal test passes, plus controller behavior tests.
- New reconciler logic → envtest specs covering success, error, and deletion (finalizer) paths.
- New SCM provider method → unit tests following an existing provider (e.g. `internal/scms/github/`), plus `metrics.RecordSCMCall` coverage.
- Pure functions in `internal/utils` with parsing/sanitizing behavior → consider a fuzz target (`Fuzz*` in `fuzz_test.go`, seeds via `f.Add`).
