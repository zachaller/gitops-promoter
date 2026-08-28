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

package controller

import (
	"context"
	"fmt"
	"maps"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
)

// pendingOnSHA3 builds an active branch state on SHA3 with one commit status still pending, i.e. an
// environment that is running that change but has not vouched for it.
func pendingOnSHA3() promoterv1alpha1.CommitBranchState {
	return promoterv1alpha1.CommitBranchState{
		Dry: promoterv1alpha1.CommitShaState{Sha: "SHA3"},
		CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
			{Key: "argocd-health", Phase: string(promoterv1alpha1.CommitPhasePending)},
		},
	}
}

func ledger(shas ...string) []promoterv1alpha1.HealthyDryShas {
	entries := make([]promoterv1alpha1.HealthyDryShas, 0, len(shas))
	for _, sha := range shas {
		entries = append(entries, promoterv1alpha1.HealthyDryShas{Sha: sha, Time: metav1.Now()})
	}
	return entries
}

func envVerification(recorded ...string) *promoterv1alpha1.EnvironmentVerificationStatus {
	if len(recorded) == 0 {
		return nil
	}
	return &promoterv1alpha1.EnvironmentVerificationStatus{DryShas: ledger(recorded...)}
}

func envStatusWithVerification(branch string, verification *promoterv1alpha1.EnvironmentVerificationStatus) promoterv1alpha1.EnvironmentStatus {
	return promoterv1alpha1.EnvironmentStatus{Branch: branch, Verification: verification}
}

func activeStatusTrailers(key, phase string) map[string][]string {
	return map[string][]string{
		constants.TrailerCommitStatusActivePrefix + key + "-phase": {phase},
	}
}

var _ = Describe("verifiedDryShaFromTrailers", func() {
	ctx := context.Background()

	It("accepts a change whose active statuses all passed at promotion time", func() {
		trailers := map[string][]string{constants.TrailerShaDryActive: {"SHA1"}}
		maps.Copy(trailers, activeStatusTrailers("argocd-health", "success"))

		drySha, ok := verifiedDryShaFromTrailers(ctx, trailers)
		Expect(ok).To(BeTrue())
		Expect(drySha).To(Equal("SHA1"))
	})

	It("rejects a change with any status not passing", func() {
		trailers := map[string][]string{constants.TrailerShaDryActive: {"SHA1"}}
		maps.Copy(trailers, activeStatusTrailers("argocd-health", "success"))
		maps.Copy(trailers, activeStatusTrailers("perf-test", "pending"))

		_, ok := verifiedDryShaFromTrailers(ctx, trailers)
		Expect(ok).To(BeFalse())
	})

	// Every configured active status is written as a trailer, pending when nothing has reported, so
	// no status trailers means no gates were configured.
	It("accepts a change from an environment that gates on nothing", func() {
		trailers := map[string][]string{constants.TrailerShaDryActive: {"SHA1"}}

		drySha, ok := verifiedDryShaFromTrailers(ctx, trailers)
		Expect(ok).To(BeTrue())
		Expect(drySha).To(Equal("SHA1"))
	})

	// A commit the controller did not author the message for is evidence of nothing, in either
	// direction. The caller skips it and keeps walking rather than stopping there.
	It("reports unknown for a commit with no promoter trailers", func() {
		_, ok := verifiedDryShaFromTrailers(ctx, map[string][]string{})
		Expect(ok).To(BeFalse())
	})

	It("reports unknown for a commit carrying unrelated trailers only", func() {
		_, ok := verifiedDryShaFromTrailers(ctx, map[string][]string{"Signed-off-by": {"Someone <a@b.c>"}})
		Expect(ok).To(BeFalse())
	})

	// Status trailers without a dry SHA cannot be attributed to a change.
	It("reports unknown when the dry SHA trailer is missing", func() {
		_, ok := verifiedDryShaFromTrailers(ctx, activeStatusTrailers("argocd-health", "success"))
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("dedupeVerifiedDryShas", func() {
	// The live latch can name a change the git walk also found.
	It("keeps the newest entry for a repeated change", func() {
		entries := dedupeVerifiedDryShas(ledger("SHA2", "SHA1", "SHA2"))
		Expect(entries).To(HaveLen(2))
		Expect(entries[0].Sha).To(Equal("SHA2"))
		Expect(entries[1].Sha).To(Equal("SHA1"))
	})

	It("leaves a record with no repeats alone", func() {
		Expect(dedupeVerifiedDryShas(ledger("SHA2", "SHA1"))).To(HaveLen(2))
	})
})

var _ = Describe("environmentVerificationStatus", func() {
	ctpWith := func(activeDrySha string, statuses []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase, recorded ...string) *promoterv1alpha1.ChangeTransferPolicy {
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Status.Active = promoterv1alpha1.CommitBranchState{
			Dry:            promoterv1alpha1.CommitShaState{Sha: activeDrySha},
			CommitStatuses: statuses,
		}
		if len(recorded) > 0 {
			ctp.Status.Verification = &promoterv1alpha1.VerificationState{DryShas: ledger(recorded...)}
		}
		return ctp
	}

	green := []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
		{Key: "argocd-health", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
	}
	notGreen := []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
		{Key: "argocd-health", Phase: string(promoterv1alpha1.CommitPhasePending)},
	}

	// The live half: the change being run right now has no merge commit yet, so without this a later
	// environment would only ever see changes that are already one promotion stale.
	It("sets current when the environment is healthy on its active change", func() {
		verification := environmentVerificationStatus(ctpWith("SHA3", green, "SHA2", "SHA1"))
		Expect(verification.Current).NotTo(BeNil())
		Expect(verification.Current.Sha).To(Equal("SHA3"))
		Expect(verification.DryShas).To(HaveLen(2))
		Expect(verification.DryShas[0].Sha).To(Equal("SHA2"))
	})

	// Composed rather than stored: an environment that goes unhealthy stops vouching for what it runs.
	It("omits current while the environment is not healthy on its active change", func() {
		verification := environmentVerificationStatus(ctpWith("SHA3", notGreen, "SHA2", "SHA1"))
		Expect(verification.Current).To(BeNil())
		Expect(verification.DryShas).To(HaveLen(2))
		Expect(verification.DryShas[0].Sha).To(Equal("SHA2"))
	})

	It("sets current for an environment that gates on nothing", func() {
		verification := environmentVerificationStatus(ctpWith("SHA3", nil, "SHA2"))
		Expect(verification.Current).NotTo(BeNil())
		Expect(verification.Current.Sha).To(Equal("SHA3"))
	})

	It("returns only the branch record before anything is active", func() {
		verification := environmentVerificationStatus(ctpWith("", green, "SHA2"))
		Expect(verification.Current).To(BeNil())
		Expect(verification.DryShas).To(HaveLen(1))
	})

	It("returns nothing when there is no record and nothing healthy", func() {
		Expect(environmentVerificationStatus(ctpWith("SHA3", notGreen))).To(BeNil())
	})

	// A change promoted, reverted, and promoted again is already in the branch record.
	It("does not duplicate a current change the branch record already holds", func() {
		verification := environmentVerificationStatus(ctpWith("SHA2", green, "SHA2", "SHA1"))
		Expect(verification.Current).To(BeNil())
		Expect(verification.DryShas).To(HaveLen(2))
	})

	// Timed by the active commit so a healthy environment does not rewrite status every reconcile.
	It("times current by its active commit, not by now", func() {
		ctp := ctpWith("SHA3", green, "SHA2")
		activeTime := metav1.NewTime(metav1.Now().Add(-24 * time.Hour).Truncate(time.Second))
		ctp.Status.Active.Hydrated.CommitTime = activeTime

		Expect(environmentVerificationStatus(ctp).Current.Time).To(Equal(activeTime))
	})

	It("keeps dryShas capped separately from current", func() {
		recordedShas := make([]string, promoterv1alpha1.MaxLastHealthyDryShas)
		for i := range recordedShas {
			recordedShas[i] = fmt.Sprintf("REC%02d", i)
		}

		verification := environmentVerificationStatus(ctpWith("SHA-LIVE", green, recordedShas...))
		Expect(verification.DryShas).To(HaveLen(promoterv1alpha1.MaxLastHealthyDryShas))
		Expect(verification.Current).NotTo(BeNil())
		Expect(verification.Current.Sha).To(Equal("SHA-LIVE"))
	})
})

var _ = Describe("hasVerifiedDrySha", func() {
	It("finds a SHA in the ledger", func() {
		envStatus := envStatusWithVerification("environment/dev", envVerification("SHA1", "SHA2"))
		Expect(hasVerifiedDrySha(envStatus, "SHA2")).To(BeTrue())
	})

	It("finds a SHA in current", func() {
		envStatus := envStatusWithVerification("environment/dev", &promoterv1alpha1.EnvironmentVerificationStatus{
			Current: &promoterv1alpha1.HealthyDryShas{Sha: "SHA3"},
		})
		Expect(hasVerifiedDrySha(envStatus, "SHA3")).To(BeTrue())
	})

	It("does not find a SHA that is absent", func() {
		envStatus := envStatusWithVerification("environment/dev", envVerification("SHA1"))
		Expect(hasVerifiedDrySha(envStatus, "SHA2")).To(BeFalse())
	})

	// Empty SHAs appear before an environment has reported, and must never match.
	It("never matches an empty SHA", func() {
		envStatus := envStatusWithVerification("environment/dev", envVerification("SHA1"))
		Expect(hasVerifiedDrySha(envStatus, "")).To(BeFalse())
	})
})

var _ = Describe("verifiedDryShasThrough", func() {
	It("returns nil when there are no preceding environments", func() {
		Expect(verifiedDryShasThrough(nil)).To(BeNil())
	})

	It("returns the preceding environment's ledger newest first", func() {
		envStatuses := []promoterv1alpha1.EnvironmentStatus{
			envStatusWithVerification("environment/dev", envVerification("SHA3", "SHA2", "SHA1")),
		}
		Expect(verifiedDryShasThrough(envStatuses)).To(Equal([]string{"SHA3", "SHA2", "SHA1"}))
	})

	// A change has to make it through every environment before this one, not just the last.
	It("keeps only the changes every preceding environment verified", func() {
		envStatuses := []promoterv1alpha1.EnvironmentStatus{
			envStatusWithVerification("environment/dev", envVerification("SHA3", "SHA1")),
			envStatusWithVerification("environment/test", envVerification("SHA3", "SHA2", "SHA1")),
		}
		Expect(verifiedDryShasThrough(envStatuses)).To(Equal([]string{"SHA3", "SHA1"}))
	})

	It("includes current when computing the effective ledger", func() {
		envStatuses := []promoterv1alpha1.EnvironmentStatus{
			envStatusWithVerification("environment/dev", &promoterv1alpha1.EnvironmentVerificationStatus{
				DryShas: ledger("SHA2", "SHA1"),
				Current: &promoterv1alpha1.HealthyDryShas{Sha: "SHA3"},
			}),
		}
		Expect(verifiedDryShasThrough(envStatuses)).To(Equal([]string{"SHA3", "SHA2", "SHA1"}))
	})

	It("returns an empty list when an earlier environment has verified nothing", func() {
		envStatuses := []promoterv1alpha1.EnvironmentStatus{
			{Branch: "environment/dev"},
			envStatusWithVerification("environment/test", envVerification("SHA1")),
		}
		Expect(verifiedDryShasThrough(envStatuses)).To(BeEmpty())
	})
})

var _ = Describe("precedingEnvironmentStatuses", func() {
	ps := func() *promoterv1alpha1.PromotionStrategy {
		return &promoterv1alpha1.PromotionStrategy{
			Spec: promoterv1alpha1.PromotionStrategySpec{
				Environments: []promoterv1alpha1.Environment{
					{Branch: "environment/dev"},
					{Branch: "environment/test"},
					{Branch: "environment/prod"},
				},
			},
			Status: promoterv1alpha1.PromotionStrategyStatus{
				Environments: []promoterv1alpha1.EnvironmentStatus{
					envStatusWithVerification("environment/test", envVerification("SHA2")),
					envStatusWithVerification("environment/dev", envVerification("SHA1")),
				},
			},
		}
	}

	It("returns nothing for the first environment", func() {
		Expect(precedingEnvironmentStatuses(ps(), nil, 0)).To(BeEmpty())
	})

	// Status order does not have to match spec order on the reconcile after an environment is added
	// or moved, so statuses are matched by branch.
	It("returns preceding statuses in sequence order regardless of status order", func() {
		preceding := precedingEnvironmentStatuses(ps(), nil, 2)
		Expect(preceding).To(HaveLen(2))
		Expect(preceding[0].Branch).To(Equal("environment/dev"))
		Expect(preceding[1].Branch).To(Equal("environment/test"))
	})

	// A newly added environment has no status yet. Leaving it out would let a change through on the
	// strength of the environments around it, so it contributes an empty ledger and holds promotion.
	It("contributes an empty ledger for an environment that has no status yet", func() {
		strategy := ps()
		strategy.Status.Environments = []promoterv1alpha1.EnvironmentStatus{
			envStatusWithVerification("environment/dev", envVerification("SHA1")),
		}

		preceding := precedingEnvironmentStatuses(strategy, nil, 2)

		Expect(preceding).To(HaveLen(2))
		Expect(preceding[1].Branch).To(Equal("environment/test"))
		Expect(preceding[1].Verification).To(BeNil())
		Expect(verifiedDryShasThrough(preceding)).To(BeEmpty())
	})

	// PS status.verification is written after CTP upserts, so during upsert we compose verification
	// from the preceding CTPs upserted earlier in the same reconcile pass.
	It("prefers verification from preceding CTPs over stale PS status", func() {
		strategy := ps()
		strategy.Status.Environments = []promoterv1alpha1.EnvironmentStatus{
			{Branch: "environment/dev"},
			{Branch: "environment/test"},
		}

		devCTP := &promoterv1alpha1.ChangeTransferPolicy{}
		devCTP.Status.Verification = &promoterv1alpha1.VerificationState{DryShas: ledger("SHA1", "SHA2")}

		preceding := precedingEnvironmentStatuses(strategy, []*promoterv1alpha1.ChangeTransferPolicy{devCTP}, 2)

		Expect(preceding).To(HaveLen(2))
		Expect(preceding[0].Verification).NotTo(BeNil())
		Expect(preceding[0].Verification.DryShas).To(HaveLen(2))
		Expect(preceding[0].Verification.DryShas[0].Sha).To(Equal("SHA1"))
		Expect(preceding[0].Verification.DryShas[1].Sha).To(Equal("SHA2"))
		Expect(verifiedDryShasThrough(preceding[:1])).To(Equal([]string{"SHA1", "SHA2"}))
	})
})

var _ = Describe("promotionCandidatesApplyConfiguration", func() {
	It("returns an empty dryShas list for nil input", func() {
		candidates := promotionCandidatesApplyConfiguration(nil)
		Expect(candidates).NotTo(BeNil())
		Expect(candidates.DryShas).To(BeEmpty())
	})

	It("caps dryShas at MaxLastHealthyDryShas", func() {
		verified := make([]string, promoterv1alpha1.MaxLastHealthyDryShas+1)
		for i := range verified {
			verified[i] = fmt.Sprintf("SHA%02d", i)
		}

		candidates := promotionCandidatesApplyConfiguration(verified)
		Expect(candidates.DryShas).To(HaveLen(promoterv1alpha1.MaxLastHealthyDryShas))
		Expect(candidates.DryShas[0]).To(Equal("SHA00"))
	})
})

var _ = Describe("isPreviousEnvironmentPending with a verification ledger", func() {
	// The starvation case: dev verified the change and has already moved on to a newer one, so it is
	// neither running nor hydrated for the target any more. Without the ledger this pends forever
	// under churn, because dev is never on the target at the moment prod looks.
	It("allows promotion of a change the previous environment verified before moving on", func() {
		prevEnvStatus := envStatusWithVerification("environment/dev", envVerification("SHA2", "SHA1"))
		prevEnvStatus.Active = pendingOnSHA3()
		prevEnvStatus.Proposed = promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: "SHA3"}}

		isPending, reason := isPreviousEnvironmentPending(
			[]promoterv1alpha1.EnvironmentStatus{prevEnvStatus}, "SHA2", metav1.Now())

		Expect(isPending).To(BeFalse())
		Expect(reason).To(BeEmpty())
	})

	It("still blocks a change the previous environment has never verified", func() {
		prevEnvStatus := envStatusWithVerification("environment/dev", envVerification("SHA2", "SHA1"))
		prevEnvStatus.Active = pendingOnSHA3()
		prevEnvStatus.Proposed = promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: "SHA3"}}

		isPending, _ := isPreviousEnvironmentPending(
			[]promoterv1alpha1.EnvironmentStatus{prevEnvStatus}, "SHA3", metav1.Now())

		Expect(isPending).To(BeTrue())
	})

	// Every environment in the chain has to have verified the change, so a gap anywhere blocks.
	It("blocks when an earlier environment in the chain never verified the change", func() {
		dev := promoterv1alpha1.EnvironmentStatus{
			Branch:   "environment/dev",
			Active:   pendingOnSHA3(),
			Proposed: promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: "SHA3"}},
		}
		test := envStatusWithVerification("environment/test", envVerification("SHA2"))
		test.Active = pendingOnSHA3()
		test.Proposed = promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: "SHA3"}}

		// test verified SHA2, so the chain check stops there and allows it; dev's own gap only matters
		// through the candidate list the PromotionStrategy controller computes.
		isPending, _ := isPreviousEnvironmentPending(
			[]promoterv1alpha1.EnvironmentStatus{dev, test}, "SHA2", metav1.Now())
		Expect(isPending).To(BeFalse())

		Expect(verifiedDryShasThrough([]promoterv1alpha1.EnvironmentStatus{dev, test})).To(BeEmpty())
	})
})

var _ = Describe("isPromotionCandidateAllowed", func() {
	// The first environment in a sequence has nothing upstream to wait for.
	It("allows anything when there are no candidate constraints", func() {
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		Expect(isPromotionCandidateAllowed(ctp, "SHA1")).To(BeTrue())
	})

	It("allows a change that is in the candidate list", func() {
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Spec.Candidates = &promoterv1alpha1.PromotionCandidates{DryShas: []string{"SHA1", "SHA2"}}
		Expect(isPromotionCandidateAllowed(ctp, "SHA2")).To(BeTrue())
	})

	It("rejects a change that is not in the candidate list", func() {
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Spec.Candidates = &promoterv1alpha1.PromotionCandidates{DryShas: []string{"SHA1"}}
		Expect(isPromotionCandidateAllowed(ctp, "SHA2")).To(BeFalse())
	})

	// An empty list means upstream has verified nothing yet, which is different from no constraint.
	It("rejects everything when the candidate list is empty", func() {
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Spec.Candidates = &promoterv1alpha1.PromotionCandidates{}
		Expect(isPromotionCandidateAllowed(ctp, "SHA1")).To(BeFalse())
	})
})

var _ = Describe("promotion policy resolution", func() {
	It("defaults to Latest when nothing is set", func() {
		spec := promoterv1alpha1.PromotionStrategySpec{}
		Expect(spec.GetPromotionPolicy(promoterv1alpha1.Environment{})).To(Equal(promoterv1alpha1.PromotionPolicyLatest))
	})

	It("uses the strategy-level policy when the environment does not set one", func() {
		spec := promoterv1alpha1.PromotionStrategySpec{PromotionPolicy: promoterv1alpha1.PromotionPolicyLatestVerified}
		Expect(spec.GetPromotionPolicy(promoterv1alpha1.Environment{})).To(Equal(promoterv1alpha1.PromotionPolicyLatestVerified))
	})

	It("lets the environment override the strategy-level policy", func() {
		spec := promoterv1alpha1.PromotionStrategySpec{PromotionPolicy: promoterv1alpha1.PromotionPolicyLatestVerified}
		env := promoterv1alpha1.Environment{PromotionPolicy: promoterv1alpha1.PromotionPolicyLatest}
		Expect(spec.GetPromotionPolicy(env)).To(Equal(promoterv1alpha1.PromotionPolicyLatest))
	})
})

var _ = Describe("promotion source branch", func() {
	It("promotes the proposed branch under the Latest policy", func() {
		spec := promoterv1alpha1.ChangeTransferPolicySpec{ProposedBranch: "environment/prod-next"}
		Expect(spec.SelectsCandidates()).To(BeFalse())
		Expect(spec.GetPromotionSourceBranch()).To(Equal("environment/prod-next"))
	})

	It("promotes the promotion branch under a selecting policy", func() {
		spec := promoterv1alpha1.ChangeTransferPolicySpec{
			ProposedBranch:  "environment/prod-next",
			PromotionBranch: "environment/prod-promote",
			PromotionPolicy: promoterv1alpha1.PromotionPolicyLatestVerified,
		}
		Expect(spec.SelectsCandidates()).To(BeTrue())
		Expect(spec.GetPromotionSourceBranch()).To(Equal("environment/prod-promote"))
	})

	// A selecting policy without a branch to select into cannot select; falling back to the proposed
	// branch keeps it promoting the tip rather than failing to promote at all.
	It("falls back to the proposed branch when no promotion branch is set", func() {
		spec := promoterv1alpha1.ChangeTransferPolicySpec{
			ProposedBranch:  "environment/prod-next",
			PromotionPolicy: promoterv1alpha1.PromotionPolicyLatestVerified,
		}
		Expect(spec.SelectsCandidates()).To(BeFalse())
		Expect(spec.GetPromotionSourceBranch()).To(Equal("environment/prod-next"))
	})

	// Latest never selects, so a promotion branch left over from a previous policy must be ignored.
	It("ignores a stale promotion branch when the policy is Latest", func() {
		spec := promoterv1alpha1.ChangeTransferPolicySpec{
			ProposedBranch:  "environment/prod-next",
			PromotionBranch: "environment/prod-promote",
			PromotionPolicy: promoterv1alpha1.PromotionPolicyLatest,
		}
		Expect(spec.SelectsCandidates()).To(BeFalse())
		Expect(spec.GetPromotionSourceBranch()).To(Equal("environment/prod-next"))
	})
})
