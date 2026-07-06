---
name: docs-authoring
description: Write and validate GitOps Promoter documentation — MkDocs structure, markdown_include embeds, crd-specs conventions, and lint-docs. Use when adding or editing anything under docs/ or mkdocs.yml.
---

# Documentation authoring

Docs are MkDocs (Material) sourced from `docs/`, published at https://gitops-promoter.readthedocs.io/.

## Rules

- **New page → add it to `mkdocs.yml`** nav, or it won't appear in the table of contents.
- **Validate with `make lint-docs`** — builds the docs and fails on any MkDocs warning (broken links, missing nav entries). CI runs this on every PR. Local setup if needed:
  ```bash
  python3 -m venv .venv && source .venv/bin/activate
  pip install -r docs/requirements.txt
  mkdocs serve   # live preview
  ```
- **Embeds**: `markdown_include` pulls files in at build time. `docs/crd-specs.md` embeds the example CRs from `internal/controller/testdata/` with `{!internal/controller/testdata/<Kind>.yaml!}` — edit the testdata YAML (which is also strict-unmarshal-tested), not a copy in the docs. `docs/contributing/writing-cel-rules.md` embeds `hack/celcost/report.md` (regenerate with `make cel-cost-report`).
- **Anchors are API**: `+kubebuilder:externalDocs` markers on API types point at `crd-specs` heading anchors (Material slugifies `### PromotionStrategy` → `#promotionstrategy`). Renaming a heading breaks the CRD's externalDocs URL.
- GitHub-style alerts (`> [!NOTE]`) are supported via plugins.
- Version references in manifests/docs are kept in sync by `hack/bump-docs-manifests.sh` (driven by the release workflow) — don't hand-bump versions across docs.

## Where content goes

| Content | Location |
| --- | --- |
| User-facing CR reference | `docs/crd-specs.md` (one `### <Kind>` section per CRD) |
| Setup / install changes | `docs/getting-started.md` |
| Architecture changes | `docs/architecture.md` |
| Promotion gate docs | `docs/gating-promotions/` |
| Contributor how-tos | `docs/contributing/` |
| Operator debugging guides | `docs/debugging/` |
| Metrics / logs / events | `docs/monitoring/` |

Behavior or API changes should land with their docs update in the same PR. PR title prefix `docs:` for docs-only changes.
