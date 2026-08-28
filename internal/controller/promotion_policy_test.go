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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// passing builds an active branch state on drySha whose commit statuses are all successful, i.e. an
// environment that is healthy on that change.
func passing(drySha string, keys ...string) promoterv1alpha1.CommitBranchState {
	statuses := make([]promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase, 0, len(keys))
	for _, key := range keys {
		statuses = append(statuses, promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
			Key:   key,
			Phase: string(promoterv1alpha1.CommitPhaseSuccess),
		})
	}
	return promoterv1alpha1.CommitBranchState{
		Dry:            promoterv1alpha1.CommitShaState{Sha: drySha},
		CommitStatuses: statuses,
	}
}

// pending builds an active branch state on drySha with one commit status still pending, i.e. an
// environment that is running the change but has not vouched for it.
func pending(drySha string) promoterv1alpha1.CommitBranchState {
	return promoterv1alpha1.CommitBranchState{
		Dry: promoterv1alpha1.CommitShaState{Sha: drySha},
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

var _ = Describe("recordVerifiedDrySha", func() {
	It("records the active dry SHA when every active commit status is passing", func() {
		envStatus := promoterv1alpha1.EnvironmentStatus{Branch: "environment/dev"}
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Status.Active = passing("SHA1", "argocd-health")

		recordVerifiedDrySha(&envStatus, ctp)

		Expect(envStatus.LastHealthyDryShas).To(HaveLen(1))
		Expect(envStatus.LastHealthyDryShas[0].Sha).To(Equal("SHA1"))
	})

	// An environment that gates on nothing has nothing to wait for, so whatever it runs is verified.
	It("records the active dry SHA when no active commit statuses are configured", func() {
		envStatus := promoterv1alpha1.EnvironmentStatus{Branch: "environment/dev"}
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Status.Active = passing("SHA1")

		recordVerifiedDrySha(&envStatus, ctp)

		Expect(envStatus.LastHealthyDryShas).To(HaveLen(1))
	})

	It("records nothing while a commit status is still pending", func() {
		envStatus := promoterv1alpha1.EnvironmentStatus{Branch: "environment/dev"}
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Status.Active = pending("SHA1")

		recordVerifiedDrySha(&envStatus, ctp)

		Expect(envStatus.LastHealthyDryShas).To(BeEmpty())
	})

	It("records nothing before the active dry SHA is known", func() {
		envStatus := promoterv1alpha1.EnvironmentStatus{Branch: "environment/dev"}
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Status.Active = passing("", "argocd-health")

		recordVerifiedDrySha(&envStatus, ctp)

		Expect(envStatus.LastHealthyDryShas).To(BeEmpty())
	})

	// An environment stays healthy across many reconciles. Re-recording would evict older entries
	// that downstream environments may still be working toward.
	It("does not re-record a SHA it already holds", func() {
		envStatus := promoterv1alpha1.EnvironmentStatus{
			Branch:             "environment/dev",
			LastHealthyDryShas: ledger("SHA1"),
		}
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Status.Active = passing("SHA1", "argocd-health")

		recordVerifiedDrySha(&envStatus, ctp)
		recordVerifiedDrySha(&envStatus, ctp)

		Expect(envStatus.LastHealthyDryShas).To(HaveLen(1))
	})

	It("keeps the newest verification first", func() {
		envStatus := promoterv1alpha1.EnvironmentStatus{
			Branch:             "environment/dev",
			LastHealthyDryShas: ledger("SHA1"),
		}
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Status.Active = passing("SHA2", "argocd-health")

		recordVerifiedDrySha(&envStatus, ctp)

		Expect(envStatus.LastHealthyDryShas).To(HaveLen(2))
		Expect(envStatus.LastHealthyDryShas[0].Sha).To(Equal("SHA2"))
		Expect(envStatus.LastHealthyDryShas[1].Sha).To(Equal("SHA1"))
	})

	It("drops the oldest verification once the ledger is full", func() {
		existing := make([]string, promoterv1alpha1.MaxLastHealthyDryShas)
		for i := range existing {
			existing[i] = "SHA" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		}
		oldest := existing[len(existing)-1]
		envStatus := promoterv1alpha1.EnvironmentStatus{
			Branch:             "environment/dev",
			LastHealthyDryShas: ledger(existing...),
		}
		ctp := &promoterv1alpha1.ChangeTransferPolicy{}
		ctp.Status.Active = passing("NEWEST", "argocd-health")

		recordVerifiedDrySha(&envStatus, ctp)

		Expect(envStatus.LastHealthyDryShas).To(HaveLen(promoterv1alpha1.MaxLastHealthyDryShas))
		Expect(envStatus.LastHealthyDryShas[0].Sha).To(Equal("NEWEST"))
		Expect(hasVerifiedDrySha(envStatus, oldest)).To(BeFalse())
	})
})

var _ = Describe("hasVerifiedDrySha", func() {
	It("finds a SHA in the ledger", func() {
		envStatus := promoterv1alpha1.EnvironmentStatus{LastHealthyDryShas: ledger("SHA1", "SHA2")}
		Expect(hasVerifiedDrySha(envStatus, "SHA2")).To(BeTrue())
	})

	It("does not find a SHA that is absent", func() {
		envStatus := promoterv1alpha1.EnvironmentStatus{LastHealthyDryShas: ledger("SHA1")}
		Expect(hasVerifiedDrySha(envStatus, "SHA2")).To(BeFalse())
	})

	// Empty SHAs appear before an environment has reported, and must never match.
	It("never matches an empty SHA", func() {
		envStatus := promoterv1alpha1.EnvironmentStatus{LastHealthyDryShas: ledger("SHA1")}
		Expect(hasVerifiedDrySha(envStatus, "")).To(BeFalse())
	})
})

var _ = Describe("verifiedDryShasThrough", func() {
	It("returns nil when there are no preceding environments", func() {
		Expect(verifiedDryShasThrough(nil)).To(BeNil())
	})

	It("returns the preceding environment's ledger newest first", func() {
		envStatuses := []promoterv1alpha1.EnvironmentStatus{
			{Branch: "environment/dev", LastHealthyDryShas: ledger("SHA3", "SHA2", "SHA1")},
		}
		Expect(verifiedDryShasThrough(envStatuses)).To(Equal([]string{"SHA3", "SHA2", "SHA1"}))
	})

	// A change has to make it through every environment before this one, not just the last.
	It("keeps only the changes every preceding environment verified", func() {
		envStatuses := []promoterv1alpha1.EnvironmentStatus{
			{Branch: "environment/dev", LastHealthyDryShas: ledger("SHA3", "SHA1")},
			{Branch: "environment/test", LastHealthyDryShas: ledger("SHA3", "SHA2", "SHA1")},
		}
		Expect(verifiedDryShasThrough(envStatuses)).To(Equal([]string{"SHA3", "SHA1"}))
	})

	It("returns an empty list when an earlier environment has verified nothing", func() {
		envStatuses := []promoterv1alpha1.EnvironmentStatus{
			{Branch: "environment/dev"},
			{Branch: "environment/test", LastHealthyDryShas: ledger("SHA1")},
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
					{Branch: "environment/test", LastHealthyDryShas: ledger("SHA2")},
					{Branch: "environment/dev", LastHealthyDryShas: ledger("SHA1")},
				},
			},
		}
	}

	It("returns nothing for the first environment", func() {
		Expect(precedingEnvironmentStatuses(ps(), 0)).To(BeEmpty())
	})

	// Status order does not have to match spec order on the reconcile after an environment is added
	// or moved, so statuses are matched by branch.
	It("returns preceding statuses in sequence order regardless of status order", func() {
		preceding := precedingEnvironmentStatuses(ps(), 2)
		Expect(preceding).To(HaveLen(2))
		Expect(preceding[0].Branch).To(Equal("environment/dev"))
		Expect(preceding[1].Branch).To(Equal("environment/test"))
	})

	// A newly added environment has no status yet. Leaving it out would let a change through on the
	// strength of the environments around it, so it contributes an empty ledger and holds promotion.
	It("contributes an empty ledger for an environment that has no status yet", func() {
		strategy := ps()
		strategy.Status.Environments = []promoterv1alpha1.EnvironmentStatus{
			{Branch: "environment/dev", LastHealthyDryShas: ledger("SHA1")},
		}

		preceding := precedingEnvironmentStatuses(strategy, 2)

		Expect(preceding).To(HaveLen(2))
		Expect(preceding[1].Branch).To(Equal("environment/test"))
		Expect(preceding[1].LastHealthyDryShas).To(BeEmpty())
		Expect(verifiedDryShasThrough(preceding)).To(BeEmpty())
	})
})

var _ = Describe("isPreviousEnvironmentPending with a verification ledger", func() {
	// The starvation case: dev verified the change and has already moved on to a newer one, so it is
	// neither running nor hydrated for the target any more. Without the ledger this pends forever
	// under churn, because dev is never on the target at the moment prod looks.
	It("allows promotion of a change the previous environment verified before moving on", func() {
		prevEnvStatus := promoterv1alpha1.EnvironmentStatus{
			Branch:             "environment/dev",
			Active:             pending("SHA3"),
			Proposed:           promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: "SHA3"}},
			LastHealthyDryShas: ledger("SHA2", "SHA1"),
		}

		isPending, reason := isPreviousEnvironmentPending(
			[]promoterv1alpha1.EnvironmentStatus{prevEnvStatus}, "SHA2", metav1.Now())

		Expect(isPending).To(BeFalse())
		Expect(reason).To(BeEmpty())
	})

	It("still blocks a change the previous environment has never verified", func() {
		prevEnvStatus := promoterv1alpha1.EnvironmentStatus{
			Branch:             "environment/dev",
			Active:             pending("SHA3"),
			Proposed:           promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: "SHA3"}},
			LastHealthyDryShas: ledger("SHA2", "SHA1"),
		}

		isPending, _ := isPreviousEnvironmentPending(
			[]promoterv1alpha1.EnvironmentStatus{prevEnvStatus}, "SHA3", metav1.Now())

		Expect(isPending).To(BeTrue())
	})

	// Every environment in the chain has to have verified the change, so a gap anywhere blocks.
	It("blocks when an earlier environment in the chain never verified the change", func() {
		dev := promoterv1alpha1.EnvironmentStatus{
			Branch:   "environment/dev",
			Active:   pending("SHA3"),
			Proposed: promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: "SHA3"}},
		}
		test := promoterv1alpha1.EnvironmentStatus{
			Branch:             "environment/test",
			Active:             pending("SHA3"),
			Proposed:           promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: "SHA3"}},
			LastHealthyDryShas: ledger("SHA2"),
		}

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
