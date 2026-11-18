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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	viewv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/view/v1alpha1"
)

// mustDetailsList asserts the runtime.Object returned by List is the typed list.
func mustDetailsList(obj runtime.Object) *viewv1alpha1.PromotionStrategyDetailsList {
	GinkgoHelper()
	list, ok := obj.(*viewv1alpha1.PromotionStrategyDetailsList)
	Expect(ok).To(BeTrue(), "List must return a *PromotionStrategyDetailsList")
	return list
}

// watchTestNamespace isolates makePS fixtures from the shared seedObjects namespace (test-ns).
const watchTestNamespace = "watch-test-ns"

// makePS returns a minimal PromotionStrategy in watchTestNamespace (no repository
// reference, so buildBundle resolves no git config) with the given labels.
func makePS(name string, lbls map[string]string) *promoterv1alpha1.PromotionStrategy {
	return &promoterv1alpha1.PromotionStrategy{
		Name: name, Namespace: watchTestNamespace, Labels: lbls,
	}
}

var _ = Describe("Watch fan-out behavior", func() {
	nsContext := func(ns string) context.Context {
		return genericapirequest.WithNamespace(context.Background(), ns)
	}

	Describe("watch-list initial snapshot", func() {
		It("delivers every bundle plus the terminating bookmark even past the delta buffer size", func() {
			// More PromotionStrategies than the per-watcher delta buffer (64). The
			// initial watch-list snapshot must never be truncated by that buffer,
			// and the initial-events-end bookmark must always arrive — a reflector
			// blocks forever without it.
			const total = 70
			objs := make([]client.Object, 0, total)
			for i := range total {
				objs = append(objs, makePS(fmt.Sprintf("ps-%03d", i), nil))
			}
			store := NewREST(newProviderWithReader(newFakeReader(objs...)))

			sendInitialEvents := true
			w, err := store.Watch(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				SendInitialEvents:    &sendInitialEvents,
				AllowWatchBookmarks:  true,
				ResourceVersionMatch: metav1.ResourceVersionMatchNotOlderThan,
			})
			Expect(err).NotTo(HaveOccurred())
			defer w.Stop()

			added := 0
			deadline := time.After(10 * time.Second)
		recv:
			for {
				select {
				case ev, ok := <-w.ResultChan():
					Expect(ok).To(BeTrue(), "watch ended before the initial snapshot completed")
					switch ev.Type {
					case watch.Added:
						added++
					case watch.Bookmark:
						break recv
					default:
						Fail(fmt.Sprintf("unexpected event type %q during initial snapshot", ev.Type))
					}
				case <-deadline:
					Fail(fmt.Sprintf("timed out after %d ADDED events without the initial-events-end bookmark", added))
				}
			}
			Expect(added).To(Equal(total), "initial snapshot must not be truncated")
		})
	})

	Describe("slow watcher", func() {
		It("is terminated on buffer overflow instead of silently losing deltas", func() {
			provider := newProviderWithReader(newFakeReader(seedObjects()...))
			store := NewREST(provider)

			// A watch at the current resourceVersion gets no initial snapshot and has
			// nothing to catch up on; every event it sees is a delta.
			w, err := store.Watch(nsContext(testNamespace), &metainternalversion.ListOptions{
				ResourceVersion: provider.currentResourceVersion(),
			})
			Expect(err).NotTo(HaveOccurred())
			defer w.Stop()

			// Broadcast far more deltas than the watcher buffer can hold while the
			// consumer reads nothing. Dropping deltas silently would leave the client
			// stale forever with no signal; the watcher must be closed instead so the
			// client re-lists.
			key := types.NamespacedName{Namespace: testNamespace, Name: testPSName}
			for range 200 {
				Expect(provider.reconcileKey(context.Background(), key)).To(Succeed())
			}

			Eventually(func() bool {
				for {
					select {
					case _, ok := <-w.ResultChan():
						if !ok {
							return true
						}
					default:
						return false
					}
				}
			}, 10*time.Second, 50*time.Millisecond).Should(BeTrue(),
				"overflowing watcher must have its result channel closed to force a client re-list")
		})
	})

	// client-go ends every watch after a few minutes and starts a new one from the last
	// resourceVersion it saw. Whatever is emitted between the two must not be lost, and a
	// resourceVersion that cannot be resumed from must be refused so the client re-lists:
	// accepting it silently would leave the client stale with no way to find out.
	Describe("resuming from a resourceVersion", func() {
		var (
			cl       client.WithWatch
			provider *BundleProvider
			store    *REST
		)
		ctx := context.Background()
		key := func(name string) types.NamespacedName {
			return types.NamespacedName{Namespace: watchTestNamespace, Name: name}
		}
		reconcile := func(name string) {
			GinkgoHelper()
			Expect(provider.reconcileKey(ctx, key(name))).To(Succeed())
		}
		setLabels := func(name string, lbls map[string]string) {
			GinkgoHelper()
			ps := &promoterv1alpha1.PromotionStrategy{}
			Expect(cl.Get(ctx, key(name), ps)).To(Succeed())
			ps.Labels = lbls
			Expect(cl.Update(ctx, ps)).To(Succeed())
			reconcile(name)
		}
		deletePS := func(name string) {
			GinkgoHelper()
			Expect(cl.Delete(ctx, makePS(name, nil))).To(Succeed())
			reconcile(name)
		}
		// resume starts a watch from resourceVersion and returns what it is sent to catch
		// up, as "TYPE name".
		resume := func(resourceVersion string, selector labels.Selector) []string {
			GinkgoHelper()
			w, err := store.Watch(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				ResourceVersion: resourceVersion,
				LabelSelector:   selector,
			})
			Expect(err).NotTo(HaveOccurred())
			defer w.Stop()
			var got []string
			for {
				select {
				case ev := <-w.ResultChan():
					obj, err := meta.Accessor(ev.Object)
					Expect(err).NotTo(HaveOccurred())
					Expect(obj.GetResourceVersion()).To(Equal(provider.currentResourceVersion()),
						"a caught-up client must be left at the current resourceVersion")
					got = append(got, fmt.Sprintf("%s %s", ev.Type, obj.GetName()))
				case <-time.After(200 * time.Millisecond):
					return got
				}
			}
		}

		BeforeEach(func() {
			cl = newFakeClientBuilder().WithObjects(
				makePS("ps-a", map[string]string{"env": "prod"}),
				makePS("ps-b", map[string]string{"env": "prod"}),
			).Build()
			provider = newProviderWithReader(cl)
			store = NewREST(provider)
			// What a client that has already synced knows about.
			reconcile("ps-a")
			reconcile("ps-b")
		})

		It("delivers nothing when nothing changed", func() {
			Expect(resume(provider.currentResourceVersion(), nil)).To(BeEmpty())
		})

		It("delivers what changed while no watch was open", func() {
			seen := provider.currentResourceVersion()

			reconcile("ps-a")
			deletePS("ps-b")
			Expect(cl.Create(ctx, makePS("ps-c", nil))).To(Succeed())
			reconcile("ps-c")

			Expect(resume(seen, nil)).To(Equal([]string{"MODIFIED ps-a", "DELETED ps-b", "ADDED ps-c"}))
		})

		It("delivers one event with the current state for an object that changed repeatedly", func() {
			seen := provider.currentResourceVersion()

			for range 5 {
				reconcile("ps-a")
			}

			Expect(resume(seen, nil)).To(Equal([]string{"MODIFIED ps-a"}))
		})

		It("delivers nothing for an object that was created and deleted in between", func() {
			seen := provider.currentResourceVersion()

			Expect(cl.Create(ctx, makePS("ps-c", nil))).To(Succeed())
			reconcile("ps-c")
			deletePS("ps-c")

			Expect(resume(seen, nil)).To(BeEmpty())
		})

		It("only delivers events newer than the resourceVersion", func() {
			reconcile("ps-a")
			seen := provider.currentResourceVersion()
			reconcile("ps-b")

			Expect(resume(seen, nil)).To(Equal([]string{"MODIFIED ps-b"}))
		})

		It("applies label selector transitions across the gap", func() {
			prod := labels.SelectorFromSet(labels.Set{"env": "prod"})
			seen := provider.currentResourceVersion()

			setLabels("ps-a", map[string]string{"env": "dev"})
			Expect(cl.Create(ctx, makePS("ps-c", map[string]string{"env": "dev"}))).To(Succeed())
			reconcile("ps-c")
			setLabels("ps-c", map[string]string{"env": "prod"})

			Expect(resume(seen, prod)).To(Equal([]string{"DELETED ps-a", "ADDED ps-c"}),
				"ps-a stopped matching and ps-c started matching while no watch was open")
		})

		It("does not deliver changes from other namespaces", func() {
			other := &promoterv1alpha1.PromotionStrategy{Name: "elsewhere", Namespace: "other-ns"}
			Expect(cl.Create(ctx, other)).To(Succeed())
			seen := provider.currentResourceVersion()

			Expect(provider.reconcileKey(ctx, client.ObjectKeyFromObject(other))).To(Succeed())

			Expect(resume(seen, nil)).To(BeEmpty())
		})

		It("gives a deletion its own resourceVersion", func() {
			before := provider.currentResourceVersion()
			deletePS("ps-b")
			Expect(provider.currentResourceVersion()).NotTo(Equal(before))
			Expect(resume(provider.currentResourceVersion(), nil)).To(BeEmpty())
		})

		It("does not advance the resourceVersion for a reconcile that emits nothing", func() {
			before := provider.currentResourceVersion()
			reconcile("never-existed")
			Expect(provider.currentResourceVersion()).To(Equal(before))
		})

		expectGone := func(resourceVersion string) {
			GinkgoHelper()
			_, err := store.Watch(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				ResourceVersion: resourceVersion,
			})
			Expect(apierrors.IsResourceExpired(err)).To(BeTrue(), "want 410 Gone so the client re-lists, got %v", err)
		}

		It("refuses a resourceVersion issued by an earlier process", func() {
			// A provider that has since restarted: its client still holds a resourceVersion
			// from before, when different things may have existed.
			seen := provider.currentResourceVersion()
			provider = newProviderWithReader(cl)
			store = NewREST(provider)

			expectGone(seen)
		})

		It("refuses a resourceVersion ahead of its own", func() {
			// Issued by another replica, or by a process started later.
			current, err := strconv.ParseUint(provider.currentResourceVersion(), 10, 64)
			Expect(err).NotTo(HaveOccurred())

			expectGone(strconv.FormatUint(current+1000, 10))
		})

		It("refuses a resourceVersion older than its history", func() {
			provider.historyLimit = 3
			seen := provider.currentResourceVersion()

			for range 3 {
				reconcile("ps-a")
			}
			Expect(resume(seen, nil)).To(Equal([]string{"MODIFIED ps-a"}), "still within the history")

			reconcile("ps-a")
			expectGone(seen)
		})

		It("rejects a resourceVersion that is not a number", func() {
			_, err := store.Watch(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				ResourceVersion: "not-a-number",
			})
			Expect(apierrors.IsBadRequest(err)).To(BeTrue())
		})

		It("delivers an event emitted while the watch is being resumed exactly once", func() {
			seen := provider.currentResourceVersion()
			reconcile("ps-a")

			w, err := store.Watch(nsContext(watchTestNamespace), &metainternalversion.ListOptions{ResourceVersion: seen})
			Expect(err).NotTo(HaveOccurred())
			defer w.Stop()
			reconcile("ps-b")

			var first, second watch.Event
			Eventually(w.ResultChan()).Should(Receive(&first))
			Eventually(w.ResultChan()).Should(Receive(&second))
			firstObj, err := meta.Accessor(first.Object)
			Expect(err).NotTo(HaveOccurred())
			secondObj, err := meta.Accessor(second.Object)
			Expect(err).NotTo(HaveOccurred())
			Expect([]string{firstObj.GetName(), secondObj.GetName()}).To(Equal([]string{"ps-a", "ps-b"}),
				"the missed change is replayed first, then the live one follows")
			Consistently(w.ResultChan(), 200*time.Millisecond).ShouldNot(Receive())
		})
	})

	Describe("label selectors", func() {
		var store *REST

		BeforeEach(func() {
			store = NewREST(newProviderWithReader(newFakeReader(
				makePS("prod-ps", map[string]string{"env": "prod"}),
				makePS("dev-ps", map[string]string{"env": "dev"}),
			)))
		})

		It("filters List results", func() {
			obj, err := store.List(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				LabelSelector: labels.SelectorFromSet(labels.Set{"env": "prod"}),
			})
			Expect(err).NotTo(HaveOccurred())
			list := mustDetailsList(obj)
			Expect(list.Items).To(HaveLen(1))
			Expect(list.Items[0].Name).To(Equal("prod-ps"))
		})

		It("suppresses non-matching initial watch events", func() {
			w, err := store.Watch(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				LabelSelector: labels.SelectorFromSet(labels.Set{"env": "prod"}),
			})
			Expect(err).NotTo(HaveOccurred())
			defer w.Stop()

			var ev watch.Event
			Eventually(w.ResultChan()).Should(Receive(&ev))
			Expect(ev.Type).To(Equal(watch.Added))
			obj, err := meta.Accessor(ev.Object)
			Expect(err).NotTo(HaveOccurred())
			Expect(obj.GetName()).To(Equal("prod-ps"))

			Consistently(w.ResultChan(), 200*time.Millisecond).ShouldNot(Receive(),
				"the non-matching bundle must not be delivered")
		})

		It("sends DELETED to a selector watcher when the object stops matching", func() {
			cl := newFakeClientBuilder().
				WithObjects(makePS("prod-ps", map[string]string{"env": "prod"})).
				Build()
			provider := newProviderWithReader(cl)
			store := NewREST(provider)

			w, err := store.Watch(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				LabelSelector: labels.SelectorFromSet(labels.Set{"env": "prod"}),
			})
			Expect(err).NotTo(HaveOccurred())
			defer w.Stop()

			var added watch.Event
			Eventually(w.ResultChan()).Should(Receive(&added))
			Expect(added.Type).To(Equal(watch.Added))

			// Make the broadcast path aware of the object's current labels.
			key := types.NamespacedName{Namespace: watchTestNamespace, Name: "prod-ps"}
			Expect(provider.reconcileKey(context.Background(), key)).To(Succeed())

			By("relabeling the PromotionStrategy so it no longer matches the selector")
			ps := &promoterv1alpha1.PromotionStrategy{}
			Expect(cl.Get(context.Background(), key, ps)).To(Succeed())
			ps.Labels = map[string]string{"env": "dev"}
			Expect(cl.Update(context.Background(), ps)).To(Succeed())
			Expect(provider.reconcileKey(context.Background(), key)).To(Succeed())

			Eventually(func(g Gomega) {
				var ev watch.Event
				g.Expect(w.ResultChan()).To(Receive(&ev))
				g.Expect(ev.Type).To(Equal(watch.Deleted))
			}, 5*time.Second, 50*time.Millisecond).Should(Succeed(),
				"a selector watcher must see DELETED when the object stops matching")
		})
	})

	Describe("field selectors", func() {
		var store *REST

		BeforeEach(func() {
			store = NewREST(newProviderWithReader(newFakeReader(
				makePS("prod-ps", nil),
				makePS("dev-ps", nil),
			)))
		})

		It("supports metadata.name equality on List", func() {
			obj, err := store.List(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				FieldSelector: fields.OneTermEqualSelector("metadata.name", "dev-ps"),
			})
			Expect(err).NotTo(HaveOccurred())
			list := mustDetailsList(obj)
			Expect(list.Items).To(HaveLen(1))
			Expect(list.Items[0].Name).To(Equal("dev-ps"))
		})

		It("rejects unsupported field selectors instead of silently ignoring them", func() {
			_, err := store.List(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				FieldSelector: fields.OneTermEqualSelector("spec.unsupported", "x"),
			})
			Expect(err).To(HaveOccurred())

			_, err = store.Watch(nsContext(watchTestNamespace), &metainternalversion.ListOptions{
				FieldSelector: fields.OneTermEqualSelector("spec.unsupported", "x"),
			})
			Expect(err).To(HaveOccurred())
		})
	})
})
