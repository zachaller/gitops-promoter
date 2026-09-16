package promotionhistory

import (
	"context"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ShouldSkipHistoryRecalculation", func() {
	DescribeTable("shouldSkipHistoryRecalculation",
		func(history []promoterv1alpha1.History, activeSha string, expected bool) {
			Expect(ShouldSkipHistoryRecalculation(history, activeSha)).To(Equal(expected))
		},
		Entry("skips when newest history entry describes the active tip",
			[]promoterv1alpha1.History{{
				Active: promoterv1alpha1.CommitBranchState{Hydrated: promoterv1alpha1.CommitShaState{Sha: "abc"}},
				PullRequest: &promoterv1alpha1.PullRequestCommonStatus{
					ID:              "5",
					MergedTargetSha: "abc",
				},
			}},
			"abc",
			true,
		),
		Entry("recalculates when newest history entry has no pull request ID",
			[]promoterv1alpha1.History{{
				Active: promoterv1alpha1.CommitBranchState{Hydrated: promoterv1alpha1.CommitShaState{Sha: "abc"}},
				PullRequest: &promoterv1alpha1.PullRequestCommonStatus{
					MergedTargetSha: "abc",
				},
			}},
			"abc",
			false,
		),
	)
})

var _ = Describe("DecodeTrailerDescription", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("round-trips plain text", func() {
		original := "Waiting for approval"
		encoded := `"` + original + `"`
		Expect(DecodeTrailerDescription(ctx, encoded)).To(Equal(original))
	})
})
