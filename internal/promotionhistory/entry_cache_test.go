package promotionhistory

import (
	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("EntryCache", func() {
	It("evicts oldest entries when over max size", func() {
		cache := NewEntryCache(2)
		cache.Put("a", promoterv1alpha1.History{PullRequest: &promoterv1alpha1.PullRequestCommonStatus{ID: "1"}})
		cache.Put("b", promoterv1alpha1.History{PullRequest: &promoterv1alpha1.PullRequestCommonStatus{ID: "2"}})
		cache.Put("c", promoterv1alpha1.History{PullRequest: &promoterv1alpha1.PullRequestCommonStatus{ID: "3"}})

		_, ok := cache.Get("a")
		Expect(ok).To(BeFalse())
		h, ok := cache.Get("b")
		Expect(ok).To(BeTrue())
		Expect(h.PullRequest.ID).To(Equal("2"))
		h, ok = cache.Get("c")
		Expect(ok).To(BeTrue())
		Expect(h.PullRequest.ID).To(Equal("3"))
	})

	It("updates an existing key without growing order", func() {
		cache := NewEntryCache(2)
		cache.Put("a", promoterv1alpha1.History{PullRequest: &promoterv1alpha1.PullRequestCommonStatus{ID: "1"}})
		cache.Put("b", promoterv1alpha1.History{PullRequest: &promoterv1alpha1.PullRequestCommonStatus{ID: "2"}})
		cache.Put("a", promoterv1alpha1.History{PullRequest: &promoterv1alpha1.PullRequestCommonStatus{ID: "1-updated"}})

		h, ok := cache.Get("a")
		Expect(ok).To(BeTrue())
		Expect(h.PullRequest.ID).To(Equal("1-updated"))
		_, ok = cache.Get("b")
		Expect(ok).To(BeTrue())
	})
})

var _ = Describe("shaListsOverlap", func() {
	DescribeTable("overlap detection",
		func(prev, next []string, expect bool) {
			Expect(shaListsOverlap(prev, next)).To(Equal(expect))
		},
		Entry("empty prev", []string{}, []string{"a"}, false),
		Entry("empty next", []string{"a"}, []string{}, false),
		Entry("no overlap", []string{"a", "b"}, []string{"c", "d"}, false),
		Entry("overlap", []string{"a", "b"}, []string{"b", "c"}, true),
		Entry("identical", []string{"x"}, []string{"x"}, true),
	)
})
