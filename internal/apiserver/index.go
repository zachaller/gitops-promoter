/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package apiserver

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	viewv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/view/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/controller"
)

var log = ctrl.Log.WithName("dashboard-apiserver")

// debounceInterval coalesces bursts of child changes for the same PromotionStrategy.
const debounceInterval = 250 * time.Millisecond

// watcherBufferSize is the per-watcher headroom for delta events beyond the
// initial snapshot. A watcher whose buffer overflows is terminated (its result
// channel is closed) so the client re-lists, mirroring the apiserver cacher's
// handling of watchers that cannot keep up — deltas are never silently dropped.
const watcherBufferSize = 64

// defaultHistoryLimit is how many of the most recent events the provider remembers so that a
// watch can resume from a resourceVersion without missing what happened since. Only the
// object key and its previous labels are kept per event (the bundle is rebuilt on resume),
// so the window is cheap. A resume from further back than this gets 410 Gone and re-lists.
const defaultHistoryLimit = 4096

// BundleProvider builds PromotionStrategyDetails bundles from a read-only
// controller-runtime cache, and fans out watch events to registered watchers when
// any related resource changes.
type BundleProvider struct {
	cache  cache.Cache
	reader client.Reader
	queue  workqueue.TypedRateLimitingInterface[types.NamespacedName]
	// watchers holds the registered watch streams keyed by a unique id (assigned
	// from nextID), so broadcast can fan events out to matching watchers and a
	// watcher can be removed when its stream stops.
	watchers map[int]*bundleWatcher
	// known tracks the last-broadcast labels of PromotionStrategy keys that have
	// already emitted an ADDED event, so reconcileKey can distinguish ADDED from
	// MODIFIED, only emit a DELETED tombstone for keys it previously surfaced, and
	// give label-selector watchers correct ADDED/DELETED transitions when an
	// object's labels start or stop matching their selector.
	known map[types.NamespacedName]labels.Set
	// namespace, when set, is the only namespace the cache holds. Requests for other namespaces return nothing.
	namespace string
	// history holds the most recent events, oldest first, so a watch can resume from a
	// resourceVersion. It is bounded by historyLimit.
	history []historyEntry
	// clusterScmProviderEnabled controls whether ClusterScmProviders are watched and resolved.
	clusterScmProviderEnabled bool

	mu sync.RWMutex
	// rv is the resourceVersion of the most recently emitted event. It only advances under
	// mu, together with the history append and the fan-out to watchers, so "every event
	// after resourceVersion N" is well defined for a watcher registering under the same lock.
	rv atomic.Uint64
	// historyFloor is the oldest resourceVersion a watch can resume from: every event
	// emitted after it is still in history.
	historyFloor uint64
	// historyLimit is the maximum length of history.
	historyLimit int
	nextID       int
}

// historyEntry records that an event was emitted for a key. The bundle itself is not kept:
// a resuming watcher is sent the object's current state, which is all a level-triggered
// client needs, and that keeps the history small however large the bundles are.
type historyEntry struct {
	// prevLabels are the object's labels before the event (nil if it did not exist).
	prevLabels labels.Set
	key        types.NamespacedName
	rv         uint64
	// existed reports whether the object existed before the event.
	existed bool
}

// NewBundleProvider creates a provider backed by the given cache. namespace is the only namespace the cache
// holds, or empty for all namespaces.
func NewBundleProvider(c cache.Cache, namespace string, clusterScmProviderEnabled bool) *BundleProvider {
	p := newProviderWithReader(c)
	p.cache = c
	p.namespace = namespace
	p.clusterScmProviderEnabled = clusterScmProviderEnabled
	return p
}

// outOfScope reports whether namespace is outside the namespace the cache holds.
func (p *BundleProvider) outOfScope(namespace string) bool {
	return p.namespace != "" && namespace != "" && namespace != p.namespace
}

// newProviderWithReader creates a provider backed by an arbitrary read-only client.
// The cache is left nil (SetupInformers/Run require a real cache); this is primarily
// used by tests and by NewBundleProvider.
func newProviderWithReader(reader client.Reader) *BundleProvider {
	p := &BundleProvider{
		reader: reader,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[types.NamespacedName](),
			workqueue.TypedRateLimitingQueueConfig[types.NamespacedName]{Name: "dashboard-bundles"},
		),
		watchers:                  map[int]*bundleWatcher{},
		known:                     map[types.NamespacedName]labels.Set{},
		clusterScmProviderEnabled: true,
		historyLimit:              defaultHistoryLimit,
	}
	// resourceVersions are synthetic: a counter that lives in this process. Starting it at
	// the current time keeps the values of one process from colliding with those of the
	// process it replaced (or of another replica), so a client resuming with a
	// resourceVersion this process did not issue is recognised and told to re-list instead
	// of being treated as caught up.
	start := uint64(time.Now().UnixNano())
	p.rv.Store(start)
	p.historyFloor = start
	return p
}

// currentResourceVersion returns the resourceVersion of the most recently emitted event.
func (p *BundleProvider) currentResourceVersion() string {
	return strconv.FormatUint(p.rv.Load(), 10)
}

// childKinds are all resource kinds whose changes can affect a bundle.
func (p *BundleProvider) childKinds() []client.Object {
	kinds := []client.Object{
		&promoterv1alpha1.PromotionStrategy{},
		&promoterv1alpha1.ChangeTransferPolicyHistory{},
		&promoterv1alpha1.ChangeTransferPolicy{},
		&promoterv1alpha1.RestoreActiveCommit{},
		&promoterv1alpha1.PullRequest{},
		&promoterv1alpha1.CommitStatus{},
		&promoterv1alpha1.GitRepository{},
		&promoterv1alpha1.ScmProvider{},
	}
	if p.clusterScmProviderEnabled {
		kinds = append(kinds, &promoterv1alpha1.ClusterScmProvider{})
	}
	return append(kinds, controller.GateCommitStatusKinds()...)
}

// SetupInformers registers event handlers for all child kinds so that any change
// is mapped back to the owning PromotionStrategy key(s) and enqueued.
func (p *BundleProvider) SetupInformers(ctx context.Context) error {
	handler := toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			p.enqueueForObject(ctx, obj)
		},
		UpdateFunc: func(_, newObj any) {
			p.enqueueForObject(ctx, newObj)
		},
		DeleteFunc: func(obj any) {
			if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			p.enqueueForObject(ctx, obj)
		},
	}

	for _, kind := range p.childKinds() {
		informer, err := p.cache.GetInformer(ctx, kind)
		if err != nil {
			return fmt.Errorf("failed to get informer for %T: %w", kind, err)
		}
		if _, err := informer.AddEventHandler(handler); err != nil {
			return fmt.Errorf("failed to add event handler for %T: %w", kind, err)
		}
	}
	return nil
}

// Run drains the workqueue, rebuilding and broadcasting bundles until ctx is done.
func (p *BundleProvider) Run(ctx context.Context) {
	defer p.queue.ShutDown()
	go func() {
		<-ctx.Done()
		p.queue.ShutDown()
	}()
	for p.processNext(ctx) {
	}
}

func (p *BundleProvider) processNext(ctx context.Context) bool {
	key, shutdown := p.queue.Get()
	if shutdown {
		return false
	}
	defer p.queue.Done(key)
	if err := p.reconcileKey(ctx, key); err != nil {
		// Transient failure (e.g. a momentary cache hiccup): retry with backoff so
		// the delta is not silently lost — watchers would otherwise stay stale
		// until the next unrelated change to the same PromotionStrategy.
		log.Error(err, "failed to build bundle; requeuing", "namespace", key.Namespace, "name", key.Name)
		p.queue.AddRateLimited(key)
		return true
	}
	p.queue.Forget(key)
	return true
}

// reconcileKey rebuilds the bundle for a PromotionStrategy key and broadcasts the
// resulting watch event (ADDED/MODIFIED/DELETED) to matching watchers. A non-nil
// error means the rebuild failed transiently and the caller should retry.
func (p *BundleProvider) reconcileKey(ctx context.Context, key types.NamespacedName) error {
	// The resourceVersion is assigned below, at the moment the event is emitted.
	bundle, err := buildBundle(ctx, p.reader, key.Namespace, key.Name, "", p.clusterScmProviderEnabled)
	notFound := apierrors.IsNotFound(err)
	if err != nil && !notFound {
		return err
	}

	// Assigning the resourceVersion, recording the event and fanning it out happen under
	// one lock, so a watcher registering concurrently either finds the event in the
	// history or receives it from the fan-out, never neither.
	p.mu.Lock()
	prevLabels, wasKnown := p.known[key]
	if notFound && !wasKnown {
		// Never surfaced to any watcher, so there is nothing to retract.
		p.mu.Unlock()
		return nil
	}

	rv := p.rv.Add(1)
	var ev watch.Event
	var curLabels labels.Set
	if notFound {
		delete(p.known, key)
		ev = watch.Event{Type: watch.Deleted, Object: &viewv1alpha1.PromotionStrategyDetails{
			Name:            key.Name,
			Namespace:       key.Namespace,
			Labels:          prevLabels,
			ResourceVersion: strconv.FormatUint(rv, 10),
		}}
	} else {
		curLabels = labels.Set(bundle.Labels)
		p.known[key] = curLabels
		bundle.ResourceVersion = strconv.FormatUint(rv, 10)
		ev = watch.Event{Type: watch.Modified, Object: bundle}
		if !wasKnown {
			ev.Type = watch.Added
		}
	}
	p.record(historyEntry{rv: rv, key: key, prevLabels: prevLabels, existed: wasKnown})
	overflowed := p.broadcast(ev, key, prevLabels, curLabels)
	p.mu.Unlock()

	// Stop takes the provider lock, so terminate overflowed watchers outside it.
	for _, w := range overflowed {
		log.Info("terminating watcher that cannot keep up; client will re-list",
			"namespace", key.Namespace, "name", key.Name)
		w.Stop()
	}
	return nil
}

// record appends an event to the history, dropping the oldest once it is full. The caller
// must hold the provider lock.
func (p *BundleProvider) record(entry historyEntry) {
	p.history = append(p.history, entry)
	if excess := len(p.history) - p.historyLimit; excess > 0 {
		p.historyFloor = p.history[excess-1].rv
		p.history = append(p.history[:0], p.history[excess:]...)
	}
}

// eventsSince returns the events a watcher with the given filters has to be sent to catch
// up from resourceVersion to the present. The caller must hold the provider lock.
//
// Everything that happened to an object since resourceVersion is collapsed into one event
// carrying its current state: MODIFIED (or ADDED, if the watcher did not have it), or
// DELETED if it is gone or no longer matches the selector. A resourceVersion this provider
// cannot vouch for is answered with 410 Gone, which makes the client re-list. That covers
// one older than the history, and one this process never issued: after a restart the
// previous process's values are all below the floor, and another replica's may lie ahead.
func (p *BundleProvider) eventsSince(ctx context.Context, resourceVersion, namespace, name string, labelSelector labels.Selector) ([]watch.Event, error) {
	since, err := strconv.ParseUint(resourceVersion, 10, 64)
	if err != nil {
		//nolint:wrapcheck // StatusError must be returned unwrapped for apiserver recognition
		return nil, apierrors.NewBadRequest(fmt.Sprintf("invalid resourceVersion %q", resourceVersion))
	}
	current := p.rv.Load()
	if since < p.historyFloor || since > current {
		//nolint:wrapcheck // StatusError must be returned unwrapped for apiserver recognition
		return nil, apierrors.NewResourceExpired(fmt.Sprintf(
			"too old resource version: %d (oldest available %d, current %d)", since, p.historyFloor, current))
	}

	// The first event after resourceVersion for a key tells what the watcher last saw of it.
	type previous struct {
		labels  labels.Set
		existed bool
	}
	var keys []types.NamespacedName
	before := map[types.NamespacedName]previous{}
	filter := &bundleWatcher{namespace: namespace, name: name, labelSelector: labelSelector}
	for _, entry := range p.history {
		if entry.rv <= since || !filter.matchesKey(entry.key.Namespace, entry.key.Name) {
			continue
		}
		if _, seen := before[entry.key]; !seen {
			before[entry.key] = previous{labels: entry.prevLabels, existed: entry.existed}
			keys = append(keys, entry.key)
		}
	}

	currentRV := strconv.FormatUint(current, 10)
	var events []watch.Event
	for _, key := range keys {
		prev := before[key]
		curLabels, exists := p.known[key]
		hadIt := prev.existed && filter.matchesLabels(prev.labels)
		if !exists || !filter.matchesLabels(curLabels) {
			if hadIt {
				events = append(events, watch.Event{Type: watch.Deleted, Object: &viewv1alpha1.PromotionStrategyDetails{
					Name:            key.Name,
					Namespace:       key.Namespace,
					Labels:          prev.labels,
					ResourceVersion: currentRV,
				}})
			}
			continue
		}
		bundle, err := buildBundle(ctx, p.reader, key.Namespace, key.Name, currentRV, p.clusterScmProviderEnabled)
		if apierrors.IsNotFound(err) {
			// Deleted just now: its DELETED event has not been emitted yet and will reach
			// this watcher through the fan-out once it is.
			continue
		}
		if err != nil {
			return nil, err
		}
		eventType := watch.Modified
		if !hadIt {
			eventType = watch.Added
		}
		events = append(events, watch.Event{Type: eventType, Object: bundle})
	}
	return events, nil
}

// enqueueForObject maps a changed child object to the owning PromotionStrategy
// key(s) and schedules a debounced rebuild.
func (p *BundleProvider) enqueueForObject(ctx context.Context, obj any) {
	co, ok := obj.(client.Object)
	if !ok {
		return
	}
	for _, key := range p.mapObjectToPromotionStrategies(ctx, co) {
		p.queue.AddAfter(key, debounceInterval)
	}
}

// mapObjectToPromotionStrategies returns the PromotionStrategy keys affected by a
// change to the given object.
func (p *BundleProvider) mapObjectToPromotionStrategies(ctx context.Context, obj client.Object) []types.NamespacedName {
	switch o := obj.(type) {
	case *promoterv1alpha1.PromotionStrategy:
		return []types.NamespacedName{{Namespace: o.Namespace, Name: o.Name}}
	case *promoterv1alpha1.ChangeTransferPolicy:
		return keyFromLabel(o.Namespace, o.Labels)
	case *promoterv1alpha1.ChangeTransferPolicyHistory:
		return keyFromLabel(o.Namespace, o.Labels)
	case *promoterv1alpha1.RestoreActiveCommit:
		return keyFromRef(o.Namespace, o.Spec.PromotionStrategyRef.Name)
	case *promoterv1alpha1.PullRequest:
		return keyFromLabel(o.Namespace, o.Labels)
	case *promoterv1alpha1.CommitStatus:
		return keyFromLabel(o.Namespace, o.Labels)
	case *promoterv1alpha1.GitRepository:
		return p.keysReferencingGitRepository(ctx, o.Namespace, o.Name)
	case *promoterv1alpha1.ScmProvider:
		return p.keysReferencingScmProvider(ctx, "ScmProvider", o.Namespace, o.Name)
	case *promoterv1alpha1.ClusterScmProvider:
		return p.keysReferencingScmProvider(ctx, "ClusterScmProvider", "", o.Name)
	default:
		// PromotionStrategyRef gate managers (ArgoCD/Git/Timed/WebRequest/Scheduled
		// CommitStatus, and any future gate with Spec.PromotionStrategyRef).
		if name := controller.PromotionStrategyRefName(obj); name != "" {
			return keyFromRef(obj.GetNamespace(), name)
		}
		return nil
	}
}

func keyFromLabel(namespace string, labels map[string]string) []types.NamespacedName {
	if labels == nil {
		return nil
	}
	if name := labels[promoterv1alpha1.PromotionStrategyLabel]; name != "" {
		return []types.NamespacedName{{Namespace: namespace, Name: name}}
	}
	return nil
}

func keyFromRef(namespace, name string) []types.NamespacedName {
	if name == "" {
		return nil
	}
	return []types.NamespacedName{{Namespace: namespace, Name: name}}
}

// keysReferencingGitRepository returns PromotionStrategy keys in a namespace whose
// repositoryReference points at the given GitRepository.
func (p *BundleProvider) keysReferencingGitRepository(ctx context.Context, namespace, repoName string) []types.NamespacedName {
	psList := &promoterv1alpha1.PromotionStrategyList{}
	if err := p.reader.List(ctx, psList, client.InNamespace(namespace)); err != nil {
		log.Error(err, "failed to list PromotionStrategies for GitRepository mapping", "namespace", namespace)
		return nil
	}
	var keys []types.NamespacedName
	for i := range psList.Items {
		if psList.Items[i].Spec.RepositoryReference.Name == repoName {
			keys = append(keys, types.NamespacedName{Namespace: namespace, Name: psList.Items[i].Name})
		}
	}
	return keys
}

// keysReferencingScmProvider returns PromotionStrategy keys for GitRepositories
// that use the given ScmProvider or ClusterScmProvider (cluster-scoped providers
// fan out across all namespaces).
func (p *BundleProvider) keysReferencingScmProvider(ctx context.Context, providerKind, providerNamespace, providerName string) []types.NamespacedName {
	repoList := &promoterv1alpha1.GitRepositoryList{}
	var listOpts []client.ListOption
	if providerKind == "ScmProvider" {
		listOpts = append(listOpts, client.InNamespace(providerNamespace))
	}
	if err := p.reader.List(ctx, repoList, listOpts...); err != nil {
		log.Error(err, "failed to list GitRepositories for ScmProvider mapping", "kind", providerKind, "name", providerName)
		return nil
	}

	var keys []types.NamespacedName
	for i := range repoList.Items {
		repo := &repoList.Items[i]
		if repo.Spec.ScmProviderRef.Kind == providerKind && repo.Spec.ScmProviderRef.Name == providerName {
			keys = append(keys, p.keysReferencingGitRepository(ctx, repo.Namespace, repo.Name)...)
		}
	}
	return keys
}

// Get builds the bundle for a single PromotionStrategy.
func (p *BundleProvider) Get(ctx context.Context, namespace, name string) (*viewv1alpha1.PromotionStrategyDetails, error) {
	if p.outOfScope(namespace) {
		//nolint:wrapcheck // StatusError must be returned unwrapped for apiserver recognition
		return nil, apierrors.NewNotFound(viewv1alpha1.Resource("promotionstrategydetails"), name)
	}
	return buildBundle(ctx, p.reader, namespace, name, p.currentResourceVersion(), p.clusterScmProviderEnabled)
}

// List builds bundles for the PromotionStrategies in the given namespace (all
// namespaces when namespace is empty), filtered by the optional exact name and
// label selector (nil selects everything). The bundle's labels are the source
// PromotionStrategy's labels, so the selector is applied to the PS before the
// (more expensive) bundle is built.
func (p *BundleProvider) List(ctx context.Context, namespace, name string, labelSelector labels.Selector) (*viewv1alpha1.PromotionStrategyDetailsList, error) {
	rv := p.currentResourceVersion()
	out := &viewv1alpha1.PromotionStrategyDetailsList{
		ResourceVersion: rv,
	}
	if p.outOfScope(namespace) {
		return out, nil
	}

	if name != "" {
		// A name filter resolves to at most one bundle, so Get it directly instead
		// of listing (and building) every PromotionStrategy in the namespace.
		bundle, err := p.Get(ctx, namespace, name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return out, nil
			}
			return nil, err
		}
		if matchesLabels(labelSelector, bundle.Labels) {
			out.Items = append(out.Items, *bundle)
		}
		return out, nil
	}

	psList := &promoterv1alpha1.PromotionStrategyList{}
	var listOpts []client.ListOption
	if namespace != "" {
		listOpts = append(listOpts, client.InNamespace(namespace))
	}
	if err := p.reader.List(ctx, psList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list PromotionStrategies: %w", err)
	}

	for i := range psList.Items {
		ps := &psList.Items[i]
		if !matchesLabels(labelSelector, ps.Labels) {
			continue
		}
		bundle, err := buildBundle(ctx, p.reader, ps.Namespace, ps.Name, rv, p.clusterScmProviderEnabled)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		out.Items = append(out.Items, *bundle)
	}
	return out, nil
}

// Watch registers a watcher for the given namespace (and optional exact-name and
// label-selector filters). When sendInitial is true, the current set of bundles is
// delivered as ADDED events before subsequent deltas stream in. When
// sendInitialEventsBookmark is also true (client-go "watch list" protocol), a
// terminating Bookmark event annotated with k8s.io/initial-events-end is sent after
// the snapshot so reflectors know the initial list is complete.
//
// When sendInitial is false the watch resumes from resourceVersion: whatever changed
// since then is delivered first (see eventsSince), so nothing is lost between a client's
// previous watch ending and this one starting. client-go ends and resumes every watch
// after a few minutes, so this is the common case, not a rare one. If the provider cannot
// tell what changed since resourceVersion, a 410 Gone error is returned and the client
// re-lists.
//
// The snapshot and the watcher registration happen atomically with respect to
// broadcast (under the provider lock), so a delta can never be delivered ahead of
// the snapshot it is newer than, and the snapshot itself can never be interleaved
// with concurrent events. Initial events are buffered in full on the watcher's
// input queue — they are never dropped, no matter how many bundles exist — and a
// per-watcher goroutine forwards them to the result channel as the watch handler
// drains it.
func (p *BundleProvider) Watch(ctx context.Context, namespace, name string, labelSelector labels.Selector, resourceVersion string, sendInitial, sendInitialEventsBookmark bool) (watch.Interface, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var initial []watch.Event
	if sendInitial {
		list, err := p.List(ctx, namespace, name, labelSelector)
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			initial = append(initial, watch.Event{Type: watch.Added, Object: &list.Items[i]})
		}
		if sendInitialEventsBookmark {
			bookmark := &viewv1alpha1.PromotionStrategyDetails{}
			bookmark.SetResourceVersion(list.ResourceVersion)
			bookmark.SetAnnotations(map[string]string{metav1.InitialEventsAnnotationKey: "true"})
			initial = append(initial, watch.Event{Type: watch.Bookmark, Object: bookmark})
		}
	} else {
		missed, err := p.eventsSince(ctx, resourceVersion, namespace, name, labelSelector)
		if err != nil {
			return nil, err
		}
		initial = missed
	}

	w := &bundleWatcher{
		provider:      p,
		namespace:     namespace,
		name:          name,
		labelSelector: labelSelector,
		// Size the input queue to hold the entire initial snapshot (or the events being
		// replayed for a resume) plus headroom for deltas, so the queueing below can
		// never block or drop.
		input:  make(chan watch.Event, len(initial)+watcherBufferSize),
		result: make(chan watch.Event),
		done:   make(chan struct{}),
	}
	for _, ev := range initial {
		w.input <- ev
	}

	p.nextID++
	w.id = p.nextID
	p.watchers[w.id] = w
	go w.process()

	return w, nil
}

// broadcast fans an event out to all watchers matching the affected key. For
// label-selector watchers it applies the upstream cacher's transition semantics
// using the previous and current label sets: an object that starts matching is
// delivered as ADDED, one that stops matching is delivered as DELETED, and one
// that matches throughout is delivered with the original event type. Watchers
// whose input buffer is full are returned: the caller terminates them (closing their
// result channel) so the client re-lists instead of silently missing deltas.
//
// The caller must hold the provider lock.
func (p *BundleProvider) broadcast(ev watch.Event, key types.NamespacedName, prevLabels, curLabels labels.Set) []*bundleWatcher {
	var overflowed []*bundleWatcher

	for _, w := range p.watchers {
		if !w.matchesKey(key.Namespace, key.Name) {
			continue
		}
		// prev only exists for MODIFIED/DELETED events; cur only for ADDED/MODIFIED.
		matchesPrev := ev.Type != watch.Added && w.matchesLabels(prevLabels)
		matchesCur := ev.Type != watch.Deleted && w.matchesLabels(curLabels)

		var sendType watch.EventType
		switch {
		case matchesPrev && matchesCur:
			sendType = ev.Type
		case matchesCur:
			sendType = watch.Added
		case matchesPrev:
			sendType = watch.Deleted
		default:
			continue
		}

		// Each watcher is served by its own goroutine in the apiserver watch
		// handler, whose versioning codec mutates the object's TypeMeta (GVK) in
		// place during serialization (SetGroupVersionKind, restored via defer).
		// For the default JSON/YAML watch the serving path does NOT copy the
		// embedded object before encoding; the only layer that copies for this
		// reason is the storage cacher's cachingObject, which deep-copies the
		// wrapped object before encoding (see cachingObject.GetObject:
		// https://github.com/kubernetes/kubernetes/blob/v1.36.1/staging/src/k8s.io/apiserver/pkg/storage/cacher/caching_object.go#L159-L165)
		// but only wraps etcd-backed resources. This virtual REST bypasses the
		// cacher, so nothing copies on our behalf. Give every watcher its own
		// copy so concurrent encodes of a shared object don't race on TypeMeta.
		evCopy := watch.Event{Type: sendType, Object: ev.Object.DeepCopyObject()}
		select {
		case w.input <- evCopy:
		default:
			overflowed = append(overflowed, w)
		}
	}
	return overflowed
}

func (p *BundleProvider) removeWatcher(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.watchers, id)
}

// matchesLabels reports whether the given label set matches the selector (a nil
// selector matches everything).
func matchesLabels(selector labels.Selector, lbls map[string]string) bool {
	return selector == nil || selector.Matches(labels.Set(lbls))
}

// bundleWatcher implements watch.Interface for a single registered watcher.
// Events are queued on input (initial snapshot first, then deltas) and forwarded
// to result by the process goroutine; result is closed when the watcher stops,
// which the apiserver watch handler surfaces to the client as the end of the
// watch (triggering a re-list).
type bundleWatcher struct {
	provider      *BundleProvider
	input         chan watch.Event
	result        chan watch.Event
	done          chan struct{}
	labelSelector labels.Selector
	namespace     string
	name          string
	stopOnce      sync.Once
	id            int
}

// process forwards queued events to the result channel until the watcher stops.
// It is the only sender on (and closer of) result.
func (w *bundleWatcher) process() {
	defer close(w.result)
	for {
		select {
		case ev := <-w.input:
			select {
			case w.result <- ev:
			case <-w.done:
				return
			}
		case <-w.done:
			return
		}
	}
}

func (w *bundleWatcher) matchesKey(namespace, name string) bool {
	if w.namespace != "" && w.namespace != namespace {
		return false
	}
	if w.name != "" && w.name != name {
		return false
	}
	return true
}

func (w *bundleWatcher) matchesLabels(lbls labels.Set) bool {
	return w.labelSelector == nil || w.labelSelector.Matches(lbls)
}

// Stop implements watch.Interface. It is idempotent and safe to call from the
// watch handler, the broadcaster (on overflow), or both concurrently.
func (w *bundleWatcher) Stop() {
	w.stopOnce.Do(func() {
		w.provider.removeWatcher(w.id)
		close(w.done)
	})
}

// ResultChan implements watch.Interface.
func (w *bundleWatcher) ResultChan() <-chan watch.Event {
	return w.result
}
