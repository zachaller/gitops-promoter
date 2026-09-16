# Changelog

## Unreleased

### Promotion history moves to the view API (breaking)

- Added aggregated kind `PromotionStrategyHistory` (`view.promoter.argoproj.io/v1alpha1`).
  History is computed by the dashboard apiserver from git notes/trailers (default 20
  entries per environment via `--max-history-entries`).
- Removed `status.history` from `ChangeTransferPolicy` and from
  `PromotionStrategy.status.environments[]`. Installations without the dashboard
  apiserver no longer expose promotion history via CRD status.
- Dashboard and Argo CD extension History views read `PromotionStrategyHistory`.

### Install methods have changed (breaking)

The dashboard is now served through a Kubernetes **aggregation APIService**
(`view.promoter.argoproj.io/v1alpha1`, kind `PromotionStrategyDetails`), which needs a TLS
serving cert. To support this, the single `install.yaml` release asset has been **removed and
replaced by three install bundles** ([#1512](https://github.com/argoproj-labs/gitops-promoter/pull/1512)).

> [!WARNING]
> **Action required:** `install.yaml` no longer exists. Any automation or docs pinned to
> `…/releases/download/<version>/install.yaml` will break. Switch to one of the bundles below.

#### New install bundles

| Bundle | What you get |
| --- | --- |
| `install-with-dashboard-cert-manager.yaml` | **Recommended.** Controller + dashboard API. Requires [cert-manager](https://cert-manager.io/); it issues and rotates the serving cert and keeps the `caBundle` injected automatically. |
| `install-with-dashboard-byo-cert.yaml` | Controller + dashboard API, no cert-manager dependency. You supply the `promoter-apiserver-serving-cert` Secret and patch the `APIService` `caBundle` yourself. |
| `install-without-ui.yaml` | Controller only (closest equivalent to the old `install.yaml`). No dashboard API. |

#### How to install the new way

Recommended (cert-manager in the cluster):

```bash
kubectl apply -f https://github.com/argoproj-labs/gitops-promoter/releases/download/v0.31.1/install-with-dashboard-cert-manager.yaml
```

Bring your own cert (no cert-manager):

```bash
kubectl apply -f https://github.com/argoproj-labs/gitops-promoter/releases/download/v0.31.1/install-with-dashboard-byo-cert.yaml
```

Controller only / no UI:

```bash
kubectl apply -f https://github.com/argoproj-labs/gitops-promoter/releases/download/v0.31.1/install-without-ui.yaml
```

Helm installs are unchanged — see the [ArtifactHub page](https://artifacthub.io/packages/helm/gitops-promoter/gitops-promoter).

#### Notes

- Already using one of the `install-with-dashboard-*` bundles? The `PromotionStrategyDetails`
  APIService is included — nothing else to do.
- Installed `install-without-ui.yaml` but want the UI later? Apply one of the dashboard bundles
  to add the APIService.
- For scripted/manual cert setup, the dev "skip TLS verify" fallback, and cert rotation, see the
  [Dashboard Aggregation API](docs/dashboard-apiserver.md) docs and the
  [Getting Started](docs/getting-started.md#installation) guide.
