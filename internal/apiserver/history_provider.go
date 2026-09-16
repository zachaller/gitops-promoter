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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	viewv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/view/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/promotionhistory"
)

// HistoryProvider serves PromotionStrategyHistory from an in-memory snapshot rebuilt asynchronously.
type HistoryProvider struct {
	cache               cache.Cache
	reader              client.Reader
	client              client.Client
	controllerNamespace string
	maxHistoryEntries   int
	entryCache          *promotionhistory.EntryCache
	envStore            *promotionhistory.EnvStateStore

	queue workqueue.TypedRateLimitingInterface[types.NamespacedName]
	workers             int
	reconcileKeyLocks     keyedMutex
	eventsEnabled         atomic.Bool

	snapshot   map[types.NamespacedName]*viewv1alpha1.PromotionStrategyHistory
	snapshotMu sync.RWMutex

	watchers map[int]*historyWatcher
	known    map[types.NamespacedName]labels.Set

	mu     sync.RWMutex
	rv     atomic.Uint64
	nextID int
}

// NewHistoryProvider creates a HistoryProvider.
func NewHistoryProvider(c cache.Cache, cli client.Client, controllerNamespace string, maxHistoryEntries, workers int) *HistoryProvider {
	if maxHistoryEntries < 1 {
		maxHistoryEntries = 20
	}
	if workers < 1 {
		workers = 4
	}
	cacheSize := maxHistoryEntries * promoterv1alpha1.MaxEnvironments * 4
	p := &HistoryProvider{
		cache:               c,
		reader:              c,
		client:              cli,
		controllerNamespace: controllerNamespace,
		maxHistoryEntries:   maxHistoryEntries,
		workers:             workers,
		reconcileKeyLocks:   keyedMutex{locks: map[types.NamespacedName]*sync.Mutex{}},
		entryCache:          promotionhistory.NewEntryCache(cacheSize),
		envStore:            promotionhistory.NewEnvStateStore(),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[types.NamespacedName](),
			workqueue.TypedRateLimitingQueueConfig[types.NamespacedName]{Name: "promotion-strategy-history"},
		),
		snapshot: make(map[types.NamespacedName]*viewv1alpha1.PromotionStrategyHistory),
		watchers: map[int]*historyWatcher{},
		known:    map[types.NamespacedName]labels.Set{},
	}
	p.rv.Store(1)
	return p
}

// EnableInformerEvents allows informer handlers to enqueue history rebuilds. Call after the
// coordinated initial PromotionStrategy enqueue so the informer resync does not storm the queue.
func (p *HistoryProvider) EnableInformerEvents() {
	p.eventsEnabled.Store(true)
}

// EnqueueAllPromotionStrategies schedules one rebuild per PromotionStrategy in the cache.
func (p *HistoryProvider) EnqueueAllPromotionStrategies(ctx context.Context) error {
	psList := &promoterv1alpha1.PromotionStrategyList{}
	if err := p.reader.List(ctx, psList); err != nil {
		return fmt.Errorf("failed to list PromotionStrategies: %w", err)
	}
	for i := range psList.Items {
		ps := &psList.Items[i]
		p.queue.Add(types.NamespacedName{Namespace: ps.Namespace, Name: ps.Name})
	}
	historyLog.Info("enqueued initial PromotionStrategyHistory rebuilds", "count", len(psList.Items))
	return nil
}

func (p *HistoryProvider) nextResourceVersion() string {
	return strconv.FormatUint(p.rv.Add(1), 10)
}

func (p *HistoryProvider) currentResourceVersion() string {
	return strconv.FormatUint(p.rv.Load(), 10)
}

func (p *HistoryProvider) historyKindsWithoutSecret() []client.Object {
	return []client.Object{
		&promoterv1alpha1.PromotionStrategy{},
		&promoterv1alpha1.ChangeTransferPolicy{},
		&promoterv1alpha1.GitRepository{},
		&promoterv1alpha1.ScmProvider{},
		&promoterv1alpha1.ClusterScmProvider{},
	}
}

// SetupInformers registers handlers that enqueue PromotionStrategy keys for history rebuilds.
func (p *HistoryProvider) SetupInformers(ctx context.Context) error {
	handler := toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if !p.eventsEnabled.Load() {
				return
			}
			p.enqueueHistoryForObject(ctx, obj)
		},
		UpdateFunc: func(oldObj, newObj any) {
			if !p.eventsEnabled.Load() {
				return
			}
			oldCTP, okOld := oldObj.(*promoterv1alpha1.ChangeTransferPolicy)
			newCTP, okNew := newObj.(*promoterv1alpha1.ChangeTransferPolicy)
			if okOld && okNew {
				if oldCTP.Status.Active.Hydrated.Sha != newCTP.Status.Active.Hydrated.Sha {
					p.enqueueHistoryKeys(ctx, keyFromLabel(newCTP.Namespace, newCTP.Labels))
				}
				return
			}
			p.enqueueHistoryForObject(ctx, newObj)
		},
		DeleteFunc: func(obj any) {
			if !p.eventsEnabled.Load() {
				return
			}
			if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			p.enqueueHistoryForObject(ctx, obj)
		},
	}

	for _, kind := range p.historyKindsWithoutSecret() {
		informer, err := p.cache.GetInformer(ctx, kind)
		if err != nil {
			return fmt.Errorf("failed to get informer for %T: %w", kind, err)
		}
		if _, err := informer.AddEventHandler(handler); err != nil {
			return fmt.Errorf("failed to add event handler for %T: %w", kind, err)
		}
	}

	secretHandler := toolscache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj any) {
			if !p.eventsEnabled.Load() {
				return
			}
			oldSec, okOld := oldObj.(*corev1.Secret)
			newSec, okNew := newObj.(*corev1.Secret)
			if !okOld || !okNew {
				return
			}
			if secretCredentialDataEqual(oldSec, newSec) {
				return
			}
			p.enqueueHistoryForObject(ctx, newSec)
		},
		DeleteFunc: func(obj any) {
			if !p.eventsEnabled.Load() {
				return
			}
			if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			p.enqueueHistoryForObject(ctx, obj)
		},
	}
	secretInformer, err := p.cache.GetInformer(ctx, &corev1.Secret{})
	if err != nil {
		return fmt.Errorf("failed to get informer for Secret: %w", err)
	}
	if _, err := secretInformer.AddEventHandler(secretHandler); err != nil {
		return fmt.Errorf("failed to add event handler for Secret: %w", err)
	}
	return nil
}

func (p *HistoryProvider) Run(ctx context.Context) {
	defer p.queue.ShutDown()
	go func() {
		<-ctx.Done()
		p.queue.ShutDown()
	}()

	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p.processNext(ctx) {
			}
		}()
	}
	wg.Wait()
}

func (p *HistoryProvider) processNext(ctx context.Context) bool {
	key, shutdown := p.queue.Get()
	if shutdown {
		return false
	}
	defer p.queue.Done(key)
	if err := p.reconcileKey(ctx, key); err != nil {
		log.Error(err, "failed to build promotion strategy history; requeuing", "namespace", key.Namespace, "name", key.Name)
		p.queue.AddRateLimited(key)
		return true
	}
	p.queue.Forget(key)
	return true
}

func (p *HistoryProvider) reconcileKey(ctx context.Context, key types.NamespacedName) error {
	p.reconcileKeyLocks.Lock(key)
	defer p.reconcileKeyLocks.Unlock(key)

	start := time.Now()
	historyLog.V(1).Info("PromotionStrategyHistory rebuild started",
		"namespace", key.Namespace, "name", key.Name)

	obj, err := buildHistory(ctx, p.client, p.controllerNamespace, key.Namespace, key.Name, p.nextResourceVersion(), p.maxHistoryEntries, p.entryCache, p.envStore)
	if apierrors.IsNotFound(err) {
		p.snapshotMu.Lock()
		delete(p.snapshot, key)
		p.snapshotMu.Unlock()

		p.mu.Lock()
		lastLabels, wasKnown := p.known[key]
		delete(p.known, key)
		p.mu.Unlock()
		if wasKnown {
			tombstone := &viewv1alpha1.PromotionStrategyHistory{
				Name:            key.Name,
				Namespace:       key.Namespace,
				Labels:          lastLabels,
				ResourceVersion: p.currentResourceVersion(),
			}
			p.broadcastHistory(watch.Event{Type: watch.Deleted, Object: tombstone}, key, lastLabels, nil)
		}
		return nil
	}
	if err != nil {
		return err
	}

	p.snapshotMu.Lock()
	p.snapshot[key] = obj
	p.snapshotMu.Unlock()

	curLabels := labels.Set(obj.Labels)
	p.mu.Lock()
	prevLabels, wasKnown := p.known[key]
	eventType := watch.Modified
	if !wasKnown {
		eventType = watch.Added
	}
	p.known[key] = curLabels
	p.mu.Unlock()

	p.broadcastHistory(watch.Event{Type: eventType, Object: obj}, key, prevLabels, curLabels)

	historyLog.Info("PromotionStrategyHistory rebuild finished",
		"namespace", key.Namespace,
		"name", key.Name,
		"duration", durationField(time.Since(start)),
		"environments", len(obj.Environments),
		"historyEntries", countHistoryEntries(obj),
	)
	return nil
}

func (p *HistoryProvider) enqueueHistoryForObject(ctx context.Context, obj any) {
	co, ok := obj.(client.Object)
	if !ok {
		return
	}
	p.enqueueHistoryKeys(ctx, p.mapObjectToPromotionStrategiesForHistory(ctx, co))
}

func (p *HistoryProvider) enqueueHistoryKeys(ctx context.Context, keys []types.NamespacedName) {
	for _, key := range keys {
		p.queue.AddAfter(key, debounceInterval)
	}
}

func (p *HistoryProvider) mapObjectToPromotionStrategiesForHistory(ctx context.Context, obj client.Object) []types.NamespacedName {
	switch o := obj.(type) {
	case *promoterv1alpha1.PromotionStrategy:
		return []types.NamespacedName{{Namespace: o.Namespace, Name: o.Name}}
	case *promoterv1alpha1.ChangeTransferPolicy:
		return keyFromLabel(o.Namespace, o.Labels)
	case *promoterv1alpha1.GitRepository:
		return p.keysReferencingGitRepository(ctx, o.Namespace, o.Name)
	case *promoterv1alpha1.ScmProvider:
		return p.keysReferencingScmProvider(ctx, "ScmProvider", o.Namespace, o.Name)
	case *promoterv1alpha1.ClusterScmProvider:
		return p.keysReferencingScmProvider(ctx, "ClusterScmProvider", "", o.Name)
	case *corev1.Secret:
		return p.keysReferencingSecret(ctx, o)
	default:
		return nil
	}
}

func (p *HistoryProvider) keysReferencingSecret(ctx context.Context, secret *corev1.Secret) []types.NamespacedName {
	var keys []types.NamespacedName

	spList := &promoterv1alpha1.ScmProviderList{}
	if err := p.reader.List(ctx, spList, client.InNamespace(secret.Namespace)); err != nil {
		log.Error(err, "failed to list ScmProviders for Secret mapping")
	} else {
		for i := range spList.Items {
			sp := &spList.Items[i]
			if sp.Spec.SecretRef.Name == secret.Name {
				keys = append(keys, p.keysReferencingScmProvider(ctx, "ScmProvider", sp.Namespace, sp.Name)...)
			}
		}
	}

	if secret.Namespace == p.controllerNamespace {
		cspList := &promoterv1alpha1.ClusterScmProviderList{}
		if err := p.reader.List(ctx, cspList); err != nil {
			log.Error(err, "failed to list ClusterScmProviders for Secret mapping")
		} else {
			for i := range cspList.Items {
				csp := &cspList.Items[i]
				if csp.Spec.SecretRef.Name == secret.Name {
					keys = append(keys, p.keysReferencingScmProvider(ctx, "ClusterScmProvider", "", csp.Name)...)
				}
			}
		}
	}
	return keys
}

func (p *HistoryProvider) keysReferencingGitRepository(ctx context.Context, namespace, repoName string) []types.NamespacedName {
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

func (p *HistoryProvider) keysReferencingScmProvider(ctx context.Context, providerKind, providerNamespace, providerName string) []types.NamespacedName {
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

func (p *HistoryProvider) scheduleRebuild(key types.NamespacedName) {
	p.queue.AddAfter(key, debounceInterval)
}

func (p *HistoryProvider) Get(ctx context.Context, namespace, name string) (*viewv1alpha1.PromotionStrategyHistory, error) {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	p.snapshotMu.RLock()
	if obj, ok := p.snapshot[key]; ok {
		p.snapshotMu.RUnlock()
		return obj, nil
	}
	p.snapshotMu.RUnlock()

	p.scheduleRebuild(key)
	return buildHistoryShell(ctx, p.reader, namespace, name, p.currentResourceVersion())
}

func (p *HistoryProvider) List(ctx context.Context, namespace, name string, labelSelector labels.Selector) (*viewv1alpha1.PromotionStrategyHistoryList, error) {
	rv := p.currentResourceVersion()
	out := &viewv1alpha1.PromotionStrategyHistoryList{ResourceVersion: rv}

	if name != "" {
		obj, err := p.Get(ctx, namespace, name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return out, nil
			}
			return nil, err
		}
		if matchesLabels(labelSelector, obj.Labels) {
			out.Items = append(out.Items, *obj)
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
		obj, err := p.Get(ctx, ps.Namespace, ps.Name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		out.Items = append(out.Items, *obj)
	}
	return out, nil
}

func (p *HistoryProvider) Watch(ctx context.Context, namespace, name string, labelSelector labels.Selector, sendInitial, sendInitialEventsBookmark bool) (watch.Interface, error) {
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
			bookmark := &viewv1alpha1.PromotionStrategyHistory{}
			bookmark.SetResourceVersion(list.ResourceVersion)
			bookmark.SetAnnotations(map[string]string{metav1.InitialEventsAnnotationKey: "true"})
			initial = append(initial, watch.Event{Type: watch.Bookmark, Object: bookmark})
		}
	}

	w := &historyWatcher{
		provider:      p,
		namespace:     namespace,
		name:          name,
		labelSelector: labelSelector,
		input:         make(chan watch.Event, len(initial)+watcherBufferSize),
		result:        make(chan watch.Event),
		done:          make(chan struct{}),
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

func (p *HistoryProvider) broadcastHistory(ev watch.Event, key types.NamespacedName, prevLabels, curLabels labels.Set) {
	var overflowed []*historyWatcher
	p.mu.RLock()
	for _, w := range p.watchers {
		if !w.matchesKey(key.Namespace, key.Name) {
			continue
		}
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
		evCopy := watch.Event{Type: sendType, Object: ev.Object.DeepCopyObject()}
		select {
		case w.input <- evCopy:
		default:
			overflowed = append(overflowed, w)
		}
	}
	p.mu.RUnlock()
	for _, w := range overflowed {
		w.Stop()
	}
}

func (p *HistoryProvider) removeHistoryWatcher(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.watchers, id)
}

type historyWatcher struct {
	provider      *HistoryProvider
	input         chan watch.Event
	result        chan watch.Event
	done          chan struct{}
	labelSelector labels.Selector
	namespace     string
	name          string
	stopOnce      sync.Once
	id            int
}

func (w *historyWatcher) process() {
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

func (w *historyWatcher) matchesKey(namespace, name string) bool {
	if w.namespace != "" && w.namespace != namespace {
		return false
	}
	if w.name != "" && w.name != name {
		return false
	}
	return true
}

func (w *historyWatcher) matchesLabels(lbls labels.Set) bool {
	return w.labelSelector == nil || w.labelSelector.Matches(lbls)
}

func (w *historyWatcher) Stop() {
	w.stopOnce.Do(func() {
		w.provider.removeHistoryWatcher(w.id)
		close(w.done)
	})
}

func (w *historyWatcher) ResultChan() <-chan watch.Event {
	return w.result
}
