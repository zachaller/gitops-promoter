# Kubernetes Resource Commit Status Controller

The Kubernetes Resource Commit Status controller gates promotions on a Kubernetes resource that it creates for
you, for example a `batch/v1` Job that runs a smoke test against the proposed change, or an Argo Workflow that runs
an integration suite. You describe the resource once in a **wrapped template**, the controller creates a real
resource per environment and commit SHA, and an [expr](https://github.com/expr-lang/expr) expression maps the live
resource's status to the commit status phase.

## Overview

Three pieces work together:

1. **`ControllerConfiguration.spec.kubernetesResourceCommitStatus.templates`** lists kinds that are already
   installed in the cluster. For each entry the controller generates a namespaced CRD
   `<Kind>Template.templates.promoter.argoproj.io` (for example `JobTemplate`).
2. A **template resource** of that generated kind holds a manifest of the real kind under `spec.template`. Because
   the wrapper is a different kind, the real controller (the Job controller, Argo Workflows, …) never reconciles it.
   Only the resources GitOps Promoter creates from it run.
3. A **`KubernetesResourceCommitStatus`** references the template, picks the SHA to report on, and carries the
   phase expression. It creates one real resource per environment and SHA and one `CommitStatus` per environment.

### How It Works

For each environment of the referenced PromotionStrategy whose `proposedCommitStatuses` (or
`activeCommitStatuses` when `reportOn: active`) includes `spec.key`:

1. The controller resolves the reported SHA: the proposed hydrated SHA by default, or the active hydrated SHA
   with `reportOn: active`.
2. It derives a deterministic resource name from the gate name, the branch, and the SHA, and reads the resource
   with that name.
3. If it does not exist, the controller renders `spec.template` of the referenced template (every string is a Go
   template with Sprig functions) and creates the resource in the gate's namespace, owned by the
   `KubernetesResourceCommitStatus`, with the standard gate labels plus `promoter.argoproj.io/reported-sha`.
4. It evaluates `spec.phase.expression` against the live resource and reports the result on a `CommitStatus`
   attached to the reported SHA.
5. While any environment is `pending`, it re-reads the resources every `spec.pollInterval` (default 30s). The
   resource is **polled**, not watched, so a phase change shows up at most one interval later.
6. When the reported SHA changes, a new resource is created for the new SHA and the resources of superseded SHAs
   are deleted, except for the `spec.historyLimit` most recent per environment.

A resource is created once per SHA. While an environment is `pending` or `failure`, a resource that disappears is
created again on the next reconcile, so deleting a failed Job re-runs it and the gate goes back to `pending`. Once an
environment reported `success` for a SHA, a missing resource (for example a Job removed by
`ttlSecondsAfterFinished`) keeps the last report and is **not** recreated, so an open gate stays open. A reconcile
happens every `pollInterval` while pending, when the PromotionStrategy or the gate spec changes, and at the
controller's configured requeue duration otherwise.

## Configuring template kinds

Add the kinds you want to wrap to the ControllerConfiguration:

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: ControllerConfiguration
metadata:
  name: promoter-controller-configuration
  namespace: promoter-system
spec:
  kubernetesResourceCommitStatus:
    workQueue: # ...
    templates:
      - group: batch
        version: v1
        kind: Job
      - group: argoproj.io
        version: v1alpha1
        kind: Workflow
        # Optional. Defaults to <Kind>Template; set it when two groups share a Kind name.
        templateKind: ArgoWorkflowTemplate
```

The CRDs are generated at controller startup and again whenever the list changes. The template schema under
`spec.template` is:

- copied from the source CustomResourceDefinition when the kind is a CRD (`promoter.argoproj.io/template-schema-source: crd`),
- otherwise derived from the API server's OpenAPI v3 document, which covers built-in kinds such as Job
  (`template-schema-source: openapi`),
- otherwise left unvalidated (`template-schema-source: none`).

`spec.template.metadata` is intentionally left open so labels and annotations can contain Go templates; the
rendered resource is validated by the API server when the controller creates it, and the controller rejects a
template whose `apiVersion` or `kind` differ from the source kind. With a derived schema,
`kubectl explain jobtemplates.templates.promoter.argoproj.io.spec.template` documents the fields.

Removing an entry does **not** delete its CRD, because that would delete every template written against it.
Delete the CRD by hand once nothing references it:

```bash
kubectl get crd -l promoter.argoproj.io/generated-template=true
kubectl delete crd jobtemplates.templates.promoter.argoproj.io
```

### RBAC

The generated CRDs and the created resources are not covered by the kubebuilder-generated role beyond CRD
management and read access to the template kinds. The controller's ServiceAccount additionally needs **create,
get, list, watch, and delete** on every source kind you configure. For `batch/v1` Jobs:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: promoter-kubernetes-resource-jobs
rules:
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["create", "get", "list", "watch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: promoter-kubernetes-resource-jobs
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: promoter-kubernetes-resource-jobs
subjects:
  - kind: ServiceAccount
    name: promoter-controller-manager
    namespace: promoter-system
```

Users who write templates need the usual verbs on the generated template kinds, for example
`jobtemplates.templates.promoter.argoproj.io`.

> [!WARNING]
> Anyone who can create a template and a `KubernetesResourceCommitStatus` in a namespace makes the controller create
> the wrapped kind in that namespace with the controller's permissions. Only wrap kinds you are comfortable letting
> every namespace author create, and scope the RBAC above to those kinds.

The fully isolated (`-namespaced`) install bundles replace the controller's ClusterRole with a namespaced Role, which
cannot manage cluster-scoped CRDs. In that mode a cluster administrator must grant CRD permissions separately or leave
`templates` empty.

## Writing a template

A template is a plain manifest of the real kind wrapped under `spec.template`. Copy an existing manifest in:

```yaml
apiVersion: templates.promoter.argoproj.io/v1alpha1
kind: JobTemplate
metadata:
  name: smoke-test
  namespace: my-app
spec:
  template:
    apiVersion: batch/v1
    kind: Job
    metadata:
      labels:
        app.kubernetes.io/component: smoke-test
    spec:
      backoffLimit: 2
      ttlSecondsAfterFinished: 86400
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: smoke-test
              image: curlimages/curl:8.5.0
              command: ["sh", "-c"]
              args:
                - curl -fsS "https://{{ .Branch | replace "environment/" "" }}.example.com/healthz?sha={{ .ShortSha }}"
```

Every string value is rendered as a Go template with [Sprig](https://masterminds.github.io/sprig/) functions
(except `env`, `expandenv`, and `getHostByName`; `urlQueryEscape` is also available). Map keys and non-string
values are copied as they are. Available variables:

| Variable | Description |
| --- | --- |
| `.Branch` | The environment branch, for example `environment/staging` |
| `.Sha` / `.ShortSha` | The reported hydrated SHA and its first 7 characters |
| `.Key` / `.ReportOn` | `spec.key` and `spec.reportOn` of the gate |
| `.ResourceName` / `.Namespace` | The name and namespace the controller assigns to the created resource |
| `.Environment` | The PromotionStrategy status entry for the branch, for example `.Environment.Proposed.Dry.Sha` |
| `.PromotionStrategy` | The full PromotionStrategy |
| `.KubernetesResourceCommitStatus` | The gate resource |

The controller replaces `metadata.name`, `metadata.namespace`, and `metadata.ownerReferences` of the template and
drops `status`. Labels and annotations in the template are kept, and the controller adds
`promoter.argoproj.io/commit-status`, `promoter.argoproj.io/environment`, `promoter.argoproj.io/reported-sha`, and
`promoter.argoproj.io/kubernetes-resource-commit-status`.

> [!NOTE]
> `spec.historyLimit` deletes the resources of superseded SHAs for you, so `ttlSecondsAfterFinished` is optional.
> If you set it, remember that a failed Job deleted by the TTL is recreated, and therefore re-run, on the next
> reconcile. Give the resource enough retries or a deadline so a stuck run eventually reaches a state your phase
> expression treats as `failure`.

## Example Configurations

### Smoke-test Job before promotion

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: KubernetesResourceCommitStatus
metadata:
  name: smoke-test
  namespace: my-app
spec:
  promotionStrategyRef:
    name: my-app
  key: smoke-test
  reportOn: proposed
  templateRef:
    kind: JobTemplate
    name: smoke-test
  phase:
    expression: >-
      Resource == nil ? 'pending'
        : (Resource.status?.succeeded ?? 0) > 0 ? 'success'
        : (Resource.status?.failed ?? 0) > 0 ? 'failure'
        : 'pending'
  descriptionTemplate: "Smoke test for {{ .Branch }} ({{ .ShortSha }}) is {{ .Phase }}"
  urlTemplate: "https://dashboard.example.com/jobs/{{ .Namespace }}/{{ .ResourceName }}"
  pollInterval: 30s
  historyLimit: 1
```

Reference the key in the PromotionStrategy:

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: PromotionStrategy
metadata:
  name: my-app
spec:
  gitRepositoryRef:
    name: my-app
  proposedCommitStatuses:
    - key: smoke-test
  environments:
    - branch: environment/development
    - branch: environment/staging
    - branch: environment/production
```

Each environment's proposed SHA gets its own Job. The PR for that environment merges only once the Job succeeds.

### Integration tests against the deployed commit (`reportOn: active`)

```yaml
apiVersion: promoter.argoproj.io/v1alpha1
kind: KubernetesResourceCommitStatus
metadata:
  name: integration-tests
spec:
  promotionStrategyRef:
    name: my-app
  key: integration-tests
  reportOn: active
  templateRef:
    kind: ArgoWorkflowTemplate
    name: integration-tests
  phase:
    expression: >-
      Resource?.status?.phase == 'Succeeded' ? 'success'
        : Resource?.status?.phase in ['Failed', 'Error'] ? 'failure'
        : 'pending'
```

With `activeCommitStatuses: [{key: integration-tests}]` on the PromotionStrategy, the next environment is promoted
only after the workflow created for the currently deployed SHA succeeds.

### Any resource with a Ready condition

```yaml
phase:
  expression: "any(Resource?.status?.conditions ?? [], {.type == 'Ready' && .status == 'True'})"
```

A boolean result maps `true` to `success` and `false` to `pending`.

## Phase expression

`spec.phase.expression` is evaluated on every reconcile. It may return:

- a boolean: `true` is `success`, `false` is `pending`
- a string: `success`, `pending`, or `failure`
- an object `{ phase }` with one of those strings

Variables:

| Variable | Description |
| --- | --- |
| `Resource` | The live resource as returned by the API server (the resource is created before the expression runs) |
| `Branch`, `Sha` | The environment branch and reported SHA |
| `Phase` | The phase reported for the environment by the previous reconcile (`""` on the first) |
| `Environment` | The PromotionStrategy status entry for the branch |
| `PromotionStrategy` | The full PromotionStrategy |
| `KubernetesResourceCommitStatus` | The gate (snapshot from the previous reconcile) |

Use optional chaining (`?.`) and `??` for fields that may not exist yet. An expression that fails to compile,
errors at runtime, or returns anything else fails the reconcile and sets `Ready=False` on the gate.

## Description and URL templates

`spec.descriptionTemplate` and `spec.urlTemplate` render the SCM commit status text and link. They receive the same
variables as the template plus `.Phase` (the phase computed this reconcile) and `.Resource` (the live resource, `nil`
when missing). When `descriptionTemplate` is empty the controller writes `Waiting for Job <name>`,
`Job <name> is successful`, or `Job <name> is failing`.

### `spec.key`

`spec.key` is the gate name your PromotionStrategy checks in `proposedCommitStatuses` or `activeCommitStatuses`.
This field is required.

### Keep the status out of the SCM (`publishTo`)

Set **`spec.publishTo: []`** to keep the generated CommitStatus in Kubernetes only. The default, `[scm]`, posts it to
the SCM. Omitting the field uses the default. See
[Where a status is published](../index.md#where-a-status-is-published-publishto).

## Status

```yaml
status:
  environments:
    - branch: environment/development
      phase: success
      description: Smoke test for environment/development (abc1234) is success
      url: https://dashboard.example.com/jobs/my-app/smoke-test-environment-development-abc1234-2f0a8c1e
      reportedSha: abc1234567890abc1234567890abc1234567890a
      resource:
        apiVersion: batch/v1
        kind: Job
        namespace: my-app
        name: smoke-test-environment-development-abc1234-2f0a8c1e
      resourceExists: true
      lastEvaluationTime: "2026-07-07T10:00:00Z"
```

`resource` names the created resource for the reported SHA, and `resourceExists` is false when the resource of a
succeeded SHA has been deleted and its last report is being kept. See the [CRD reference](../../crd-specs.md#kubernetesresourcecommitstatus) for every field.

## Events

| Event Type | Event Reason | Description |
| --- | --- | --- |
| Normal | KubernetesResourceCreated | A resource was created from the template for an environment and SHA. |
| Normal | KubernetesResourceDeleted | A resource of a superseded SHA was deleted. |
| Normal/Warning | CommitStatusPhaseChanged | The phase for an environment changed. Warning when the new phase is `failure`. |
| Warning | CommitStatusesNotReady | One or more managed CommitStatuses is not Ready. |
| Normal | OrphanedCommitStatusDeleted | A CommitStatus of an environment that no longer applies was deleted. |

The ControllerConfiguration emits `TemplateCRDSyncFailed` and reports `Ready=False` when a template CRD cannot be
generated, for example because the source kind is not installed.

## Multi-install

In [multiple controller installs](../../multi-install.md), label `KubernetesResourceCommitStatus` resources and the
template resources they reference with `promoter.argoproj.io/instance-id`, like every other Promoter CR. The created
resources and CommitStatuses are stamped with the install's instance ID automatically. Generated template CRDs are
cluster-scoped and shared by every install; configure the same `templates` list on each install that uses them.
