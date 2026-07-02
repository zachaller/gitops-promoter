# PR #1705 review comments (ready to paste)

Each comment is anchored to a file/line that is part of the PR diff, so it can go
inline in a GitHub review. Bodies are ready to paste as-is.

---

## 1. `docs/multi-install.md` — line 120 (migration runbook, step 1)

> GitRepository and ScmProvider are in the partitioned cache list
> (`internal/cache/instanceid.go`), but they're user-created roots — nothing
> propagates the label onto them. Step 1 doesn't name them, and the "Confirm
> roots are labeled" example further down only checks the strategy + gate kinds.
> If someone follows this list as written, their PromotionStrategies get
> relabeled but the GitRepository stays unlabeled, and after the restart every
> CTP reconcile fails NotFound on a GitRepository that exists — promotions stop
> with a pretty confusing error. Can we name them explicitly?
>
> ```suggestion
> 1. **Label roots only** — add `promoter.argoproj.io/instance-id: <your-id>` to every Promoter CR this install should manage (`PromotionStrategy`, `GitRepository`, `ScmProvider`/`ClusterScmProvider`, `TimedCommitStatus`, `GitCommitStatus`, `WebRequestCommitStatus`, `ArgoCDCommitStatus`, and others as needed). Label referenced `Secret` objects (SCM, HTTP auth, kubeconfig) with the same value. Use metadata-only patches.
> ```
>
> The verification example around line 193 should get the same treatment:
>
> ```bash
> kubectl get promotionstrategy,gitrepository,scmprovider,timedcommitstatus,gitcommitstatus,webrequestcommitstatus,argocdcommitstatus \
>   -n team-a -l promoter.argoproj.io/instance-id=wave-0
> ```

---

## 2. `internal/cache/instanceid.go` — line 34 (`partitionedSecretObject`)

> Partitioning Secrets by label interacts badly with the ScmProvider secret
> finalizers. `removeSecretFinalizerForProvider` (finalizer_utils.go) treats a
> cache `NotFound` as "secret is gone, skip removal". With the migration steps
> in this PR's docs, that's reachable:
>
> 1. Default install adds `scmprovider…/secret-finalizer` to Secret S.
> 2. Operator labels S with `instance-id: wave-0` (runbook step 1) while the
>    ScmProvider is still in the default partition.
> 3. Operator deletes the ScmProvider and S. The default install's cached `Get`
>    returns NotFound → it skips finalizer removal → S is stuck in
>    `Terminating` forever, because wave-0 only removes the finalizer when one
>    of *its own* ScmProviders referencing S is deleted.
>
> The mirror image also holds: `ensureSecretFinalizer` no-ops on NotFound, so a
> wave-0 ScmProvider referencing a not-yet-labeled Secret silently gets no
> deletion protection at all.
>
> Since the finalizer helpers are always name-targeted single-object ops, could
> they go through the APIReader (uncached) instead of the partitioned cache?
> That keeps the partition for list/watch semantics but makes finalizer
> bookkeeping see the truth. Alternatively the docs need a hard warning that a
> Secret must not be relabeled while another partition's provider still holds a
> finalizer on it.

---

## 3. `internal/cache/instanceid.go` — line 62 (`instanceIDSelector`, `DoesNotExist` branch)

> A resource whose label value matches no running install falls into a black
> hole. `instance-id: ""` is a valid label *value* to the API server (easy to
> produce from a Helm template that renders empty), but the default install's
> `DoesNotExist` selector excludes it (the key exists) and no multi-install can
> ever match it (`spec.instanceID` has minLength=1). Same for a typo'd value or
> a decommissioned install. Result: never reconciled, no status, no condition,
> no event — and a `PullRequest` carrying its finalizer becomes undeletable
> (`kubectl delete` hangs forever with no controller able to process it).
>
> We can't validate labels via CRD schema, so at minimum this deserves a
> troubleshooting entry in multi-install.md ("resource has an instance-id value
> no install serves — including the empty string"), and ideally something
> operational, e.g. a periodic check or kubectl one-liner in the docs:
>
> ```bash
> # CRs whose instance-id matches no known install (empty value included)
> kubectl get promotionstrategy,changetransferpolicy,commitstatus,pullrequest -A \
>   -l 'promoter.argoproj.io/instance-id,promoter.argoproj.io/instance-id notin (wave-0,wave-1)'
> ```

---

## 4. `internal/controller/gitcommitstatus_controller.go` — line 408 (`CopyInstanceIDLabelToMap` wrapping `CommitStatusStandardLabels`)

> This `CopyInstanceIDLabelToMap(parent, CommitStatusStandardLabels(parent, …))`
> two-step is repeated at four gate-controller call sites, and
> developing-a-commitstatus.md teaches the same dance. Since
> `CommitStatusStandardLabels` already takes the parent object, can we fold the
> copy in and make propagation impossible to forget?
>
> ```go
> // CommitStatusStandardLabels returns the labels gate controllers set on each CommitStatus:
> // parent gate, environment branch, commit-status key, and (when present) instance-id.
> func CommitStatusStandardLabels(parent client.Object, branch, commitStatusKey string) map[string]string {
> 	return CopyInstanceIDLabelToMap(parent, map[string]string{
> 		CommitStatusGateLabelKeyForParent(parent): KubeSafeLabel(parent.GetName()),
> 		promoterv1alpha1.EnvironmentLabel:         KubeSafeLabel(branch),
> 		promoterv1alpha1.CommitStatusLabel:        commitStatusKey,
> 	})
> }
> ```
>
> Then the four wrappers here, in timedcommitstatus, webrequestcommitstatus, and
> argocdcommitstatus collapse to a plain `CommitStatusStandardLabels` call, and
> the contributing doc loses a step. The failure mode this removes is nasty: a
> future gate controller that calls `CommitStatusStandardLabels` alone creates
> children the partitioned cache can't see — gates hang with no error anywhere.

---

## 5. `internal/cache/instanceid.go` — line 17 (`promotorCRDObjects`)

> The partitioning contract rests on this hand-maintained list, and
> `instanceid_test.go` builds its expectations by iterating
> `PartitionedObjects()` — the same slice `OptionsForInstanceID` consumes — so a
> type missing from the list can never fail CI. We add gate CRDs fairly
> regularly; the next one that's forgotten here gets an *unpartitioned*
> informer, and two installs reconcile it concurrently (duplicate SCM writes —
> exactly what this feature exists to prevent).
>
> Suggest making the test independent of the production slice, e.g.:
>
> ```go
> // Fails when a new promoter.argoproj.io kind is registered but not partitioned.
> func TestPartitionCoversAllPromoterKinds(t *testing.T) {
> 	partitioned := map[string]bool{}
> 	for _, obj := range cache.PartitionedObjects() {
> 		gvk, _ := apiutil.GVKForObject(obj, utils.GetScheme())
> 		partitioned[gvk.Kind] = true
> 	}
> 	for gvk := range utils.GetScheme().AllKnownTypes() {
> 		if gvk.Group != promoterv1alpha1.GroupVersion.Group || !isReconciledKind(gvk.Kind) {
> 			continue // skip List types, ControllerConfiguration, etc.
> 		}
> 		if !partitioned[gvk.Kind] {
> 			t.Errorf("kind %s is registered in the scheme but not instance-id partitioned", gvk.Kind)
> 		}
> 	}
> }
> ```
>
> (Or simply assert against an independent literal list of kind names.) That
> also lets the three `Exported for tests` accessors go away — with the current
> setup they only enable the tautology. Also `promotorCRDObjects` → typo,
> should be `promoterCRDObjects`.

---

## 6. `internal/controller/controllerconfiguration_controller.go` — line 86 (drift check)

> The drift check only runs when this reconciler gets an event, and only on the
> active leader. In HA, followers keep the boot-time cache partition; if
> `spec.instanceID` changes and a stale follower later wins the lease, *all* its
> controllers start with the wrong partition and reconcile resources now owned
> by another install until this reconcile happens to fire — duplicate PRs /
> hydration commits at the SCM, which SSA idempotency doesn't protect. The docs
> say "roll the deployment", but nothing enforces it.
>
> One cheap mitigation: a leader-gated runnable that re-verifies the instance ID
> at election time, before/alongside controller startup:
>
> ```go
> // cmd/main.go, after settingsMgr is created
> if err := localManager.Add(&instanceIDGuard{
> 	cfg: restConfig, namespace: controllerNamespace,
> 	startup: instanceID, shutdown: shutdown,
> }); err != nil { … }
>
> type instanceIDGuard struct {
> 	cfg       *rest.Config
> 	namespace string
> 	startup   *string
> 	shutdown  context.CancelFunc
> }
>
> func (g *instanceIDGuard) NeedLeaderElection() bool { return true }
>
> func (g *instanceIDGuard) Start(ctx context.Context) error {
> 	current, err := settings.ReadInstanceID(ctx, g.cfg, g.namespace)
> 	if err != nil {
> 		return fmt.Errorf("verifying instanceID on leadership acquisition: %w", err)
> 	}
> 	if !ptr.Equal(g.startup, current) {
> 		g.shutdown()
> 	}
> 	return nil
> }
> ```
>
> It doesn't fully close the race (the GET runs concurrently with the first
> reconciles), but it shrinks the exposure from "until the CC controller's first
> reconcile" to a single direct read at election time, and it covers the case
> where the CC event was consumed by the previous leader.

---

## 7. `internal/cache/instanceid.go` — line 23 (`ClusterScmProvider` in the partition list)

> Cluster-scoped and shared resources can only carry **one** instance-id value,
> so putting `ClusterScmProvider` and `Secret` in the partition means they can
> never be shared across installs: labeling a shared ClusterScmProvider for
> wave-0 makes it NotFound to the default install, and relabeling a shared
> kubeconfig Secret shows up as a synthetic DELETE in the other install's
> kubeconfig-provider watch — its remote-cluster Application events silently
> stop and ArgoCDCommitStatus gates hang. That may be an acceptable constraint,
> but multi-install.md doesn't say it, and never mentions ClusterScmProvider at
> all. Can we document "shared credentials/providers across installs are not
> supported — duplicate them per install", or exclude the kubeconfig secrets
> from partitioning if sharing them is meant to work?

---

## 8. `internal/utils/utils.go` — line 479 (`obj.SetStatusInstanceID(InstanceIDStatusValue(obj))`)

> Right now `status.instanceID` is stamped from the object's **own** label, and
> the cache guarantees a controller only ever sees objects matching its
> partition — so status == label by construction and the field can't tell us
> anything the label doesn't. If we stamp the **controller's configured**
> instance ID instead, the field becomes a real cross-install-write detector: in
> the two partition-leak cases that can actually happen (a CRD missing from the
> partition list, a stale HA follower after failover), the leaking install
> writes *its* ID into status and the label/status mismatch flags the bug.
>
> ```go
> // internal/utils (value set once at startup, before the manager runs)
> var controllerInstanceID *string
>
> func SetControllerInstanceID(id *string) { controllerInstanceID = id }
>
> // HandleReconciliationResult
> obj.SetStatusInstanceID(controllerInstanceID)
> ```
>
> (Or thread it through settings.Manager if we'd rather avoid the package
> global.) The multi-install.md verification table would then read
> "`status.instanceID` — which install last wrote status; must equal the label,
> mismatch indicates a partitioning leak", which is a much stronger check than
> the current mirror.

---

## 9. `internal/utils/labels.go` — line 10 (`CopyInstanceIDLabel`)

> nit: this variant has no production callers — all eight propagation sites use
> `CopyInstanceIDLabelToMap`. Suggest dropping it (a one-line wrapper can come
> back if an object-based call site ever appears). Related test dedupe: the
> Describe blocks for these helpers in `internal/utils/commitstatus_test.go`
> duplicate the ones in `internal/utils/labels_test.go`, and the
> `OptionsForInstanceID` Describe in
> `internal/controller/controllerconfiguration_controller_test.go` duplicates
> `internal/cache/instanceid_test.go` inside the slow envtest suite — one copy
> of each is enough.

---

## 10. `internal/settings/manager.go` — line 47 (`InstanceIDsEqual`)

> nit: this is exactly `ptr.Equal` from `k8s.io/utils/ptr`, which we already
> import elsewhere (changetransferpolicy_controller.go). The single call site in
> controllerconfiguration_controller.go can use `ptr.Equal(r.StartupInstanceID,
> cc.Spec.InstanceID)` and this helper plus its table test can go.

---

## 11. `internal/controller/promotionstrategy_controller_test.go` — line 5910 (`TestNoInstanceIDPredicateOnControllers`)

> nit: this greps controller sources for `promoterpredicate.InstanceID`, but
> that symbol doesn't exist anywhere in the codebase, so the test can never
> fail — and anyone building the same anti-pattern under a different name walks
> right past it. The design decision ("cache ByObject is the partition
> boundary, no per-controller predicates") reads better as the doc comment on
> `cache.OptionsForInstanceID`, which already says it. Suggest deleting the
> test.
