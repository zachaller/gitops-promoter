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
	_ "embed"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	promoterConditions "github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

//go:embed testdata/ProgressiveOrderCommitStatus.yaml
var testProgressiveOrderCommitStatusYAML string

// progressEnvStatus builds an EnvironmentStatus with the given active dry SHA/commit time, proposed dry SHA,
// and a single active argocd-health commit status in the given phase.
func progressEnvStatus(branch, activeDry string, activeTime time.Time, proposedDry string, phase promoterv1alpha1.CommitStatusPhase) promoterv1alpha1.EnvironmentStatus {
	return promoterv1alpha1.EnvironmentStatus{
		Branch: branch,
		Active: promoterv1alpha1.CommitBranchState{
			Dry: promoterv1alpha1.CommitShaState{Sha: activeDry, CommitTime: metav1.NewTime(activeTime)},
			CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				{Key: "argocd-health", Phase: string(phase)},
			},
		},
		Proposed: promoterv1alpha1.CommitBranchState{
			Dry: promoterv1alpha1.CommitShaState{Sha: proposedDry},
		},
	}
}

func watermarkAt(sha string, t time.Time) *promoterv1alpha1.ProgressiveOrderCommitStatusWatermark {
	return &promoterv1alpha1.ProgressiveOrderCommitStatusWatermark{Sha: sha, CommitTime: metav1.NewTime(t)}
}

var _ = Describe("ProgressiveOrderCommitStatus Controller", func() {
	Context("When unmarshalling the test data", func() {
		It("should unmarshal the ProgressiveOrderCommitStatus resource", func() {
			err := unmarshalYamlStrict(testProgressiveOrderCommitStatusYAML, &promoterv1alpha1.ProgressiveOrderCommitStatus{})
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When the PromotionStrategy is missing", func() {
		var (
			ctx  context.Context
			pocs *promoterv1alpha1.ProgressiveOrderCommitStatus
		)

		BeforeEach(func() {
			ctx = context.Background()
			pocs = &promoterv1alpha1.ProgressiveOrderCommitStatus{
				Name:      "progressive-order-missing-ps",
				Namespace: "default",
				Spec: promoterv1alpha1.ProgressiveOrderCommitStatusSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{Name: "non-existent"},
					Key:                  promoterv1alpha1.ProgressiveOrderCommitStatusKey,
				},
			}
			Expect(k8sClient.Create(ctx, pocs)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, pocs)
		})

		It("should set Ready=False when the PromotionStrategy is not found", func() {
			Eventually(func(g Gomega) {
				updated := &promoterv1alpha1.ProgressiveOrderCommitStatus{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pocs), updated)).To(Succeed())
				readyCondition := meta.FindStatusCondition(updated.Status.Conditions, string(promoterConditions.Ready))
				g.Expect(readyCondition).ToNot(BeNil())
				g.Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
			}, constants.EventuallyTimeout).Should(Succeed())
		})
	})

	Context("When used as the PromotionStrategy ordering gate", func() {
		var (
			ctx               context.Context
			name              string
			scmSecret         *v1.Secret
			scmProvider       *promoterv1alpha1.ScmProvider
			gitRepo           *promoterv1alpha1.GitRepository
			promotionStrategy *promoterv1alpha1.PromotionStrategy
			pocs              *promoterv1alpha1.ProgressiveOrderCommitStatus
		)

		BeforeEach(func() {
			ctx = context.Background()

			name, scmSecret, scmProvider, gitRepo, _, _, promotionStrategy = promotionStrategyResource(ctx, "progressive-order-commit-status-controller-test", "default")
			promotionStrategy.Spec.ProposedCommitStatuses = nil
			promotionStrategy.Spec.ActiveCommitStatuses = nil
			promotionStrategy.Spec.OrderCommitStatusRef = promoterv1alpha1.OrderCommitStatusRef{
				Group: promoterv1alpha1.SchemeGroupVersion.Group,
				Kind:  promoterv1alpha1.ProgressiveOrderCommitStatusKind,
				Name:  name,
			}
			setupInitialTestGitRepoOnServer(ctx, gitRepo)

			pocs = &promoterv1alpha1.ProgressiveOrderCommitStatus{
				Name:      name,
				Namespace: "default",
				Spec: promoterv1alpha1.ProgressiveOrderCommitStatusSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{Name: name},
					Key:                  promoterv1alpha1.ProgressiveOrderCommitStatusKey,
				},
			}

			Expect(k8sClient.Create(ctx, scmSecret)).To(Succeed())
			Expect(k8sClient.Create(ctx, scmProvider)).To(Succeed())
			Expect(k8sClient.Create(ctx, gitRepo)).To(Succeed())
			Expect(k8sClient.Create(ctx, pocs)).To(Succeed())
			Expect(k8sClient.Create(ctx, promotionStrategy)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, promotionStrategy)
			_ = k8sClient.Delete(ctx, pocs)
			_ = k8sClient.Delete(ctx, gitRepo)
			_ = k8sClient.Delete(ctx, scmProvider)
			_ = k8sClient.Delete(ctx, scmSecret)
		})

		It("should inject its key onto ChangeTransferPolicies and promote a change through every environment", func() {
			By("Checking the PromotionStrategy injects the progressive order key onto each ChangeTransferPolicy")
			Eventually(func(g Gomega) {
				for _, branch := range []string{testBranchDevelopment, testBranchStaging, testBranchProduction} {
					ctp := &promoterv1alpha1.ChangeTransferPolicy{}
					ctpName := utils.ChangeTransferPolicyNameForEnvironment(name, branch)
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: ctpName}, ctp)).To(Succeed())
					g.Expect(ctp.Spec.ProposedCommitStatuses).To(ContainElement(promoterv1alpha1.CommitStatusSelector{Key: promoterv1alpha1.ProgressiveOrderCommitStatusKey}))
				}
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Creating a proposed change")
			gitPath, err := os.MkdirTemp("", "*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(gitPath) })
			makeChangeAndHydrateRepo(gitPath, gitRepo, "progressive order test change", "")

			By("Waiting for every environment to promote the change and record it as its progress watermark")
			Eventually(func(g Gomega) {
				ps := &promoterv1alpha1.PromotionStrategy{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(promotionStrategy), ps)).To(Succeed())
				g.Expect(ps.Status.Environments).To(HaveLen(3))
				targetDry := ps.Status.Environments[0].Proposed.Dry.Sha
				g.Expect(targetDry).NotTo(BeEmpty())

				updated := &promoterv1alpha1.ProgressiveOrderCommitStatus{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pocs), updated)).To(Succeed())
				g.Expect(updated.Status.Environments).To(HaveLen(3))
				for _, env := range updated.Status.Environments {
					g.Expect(env.LastHealthyDry).NotTo(BeNil(), "branch %s", env.Branch)
					g.Expect(env.LastHealthyDry.Sha).To(Equal(targetDry), "branch %s", env.Branch)
					g.Expect(env.Phase).To(Equal(promoterv1alpha1.CommitPhaseSuccess), "branch %s", env.Branch)
				}
			}, constants.EventuallyTimeout).Should(Succeed())
		})
	})
})

var _ = Describe("ProgressiveOrderCommitStatus progress logic", func() {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	t2 := t0.Add(2 * time.Hour)
	t3 := t0.Add(3 * time.Hour)
	now := metav1.NewTime(t3.Add(time.Minute))

	Describe("advanceProgressWatermark", func() {
		It("starts a watermark when the environment is healthy", func() {
			wm := advanceProgressWatermark(nil, progressEnvStatus("dev", "a", t1, "a", promoterv1alpha1.CommitPhaseSuccess), now)
			Expect(wm).NotTo(BeNil())
			Expect(wm.Sha).To(Equal("a"))
			Expect(wm.CommitTime.Time).To(Equal(t1))
			Expect(wm.ObservedAt).To(Equal(&now))
		})

		It("does not start a watermark when the environment is not healthy", func() {
			Expect(advanceProgressWatermark(nil, progressEnvStatus("dev", "a", t1, "a", promoterv1alpha1.CommitPhasePending), now)).To(BeNil())
		})

		It("keeps the previous watermark while a newer commit is still rolling out", func() {
			prev := watermarkAt("a", t1)
			wm := advanceProgressWatermark(prev, progressEnvStatus("dev", "b", t2, "c", promoterv1alpha1.CommitPhasePending), now)
			Expect(wm).To(BeIdenticalTo(prev))
		})

		It("keeps the previous watermark when the newer commit is failing", func() {
			prev := watermarkAt("a", t1)
			wm := advanceProgressWatermark(prev, progressEnvStatus("dev", "b", t2, "b", promoterv1alpha1.CommitPhaseFailure), now)
			Expect(wm).To(BeIdenticalTo(prev))
		})

		It("advances when the environment becomes healthy on a newer commit", func() {
			wm := advanceProgressWatermark(watermarkAt("a", t1), progressEnvStatus("dev", "b", t2, "b", promoterv1alpha1.CommitPhaseSuccess), now)
			Expect(wm.Sha).To(Equal("b"))
			Expect(wm.CommitTime.Time).To(Equal(t2))
		})

		It("keeps the original observedAt when the same commit stays healthy", func() {
			prev := watermarkAt("a", t1)
			wm := advanceProgressWatermark(prev, progressEnvStatus("dev", "a", t1, "a", promoterv1alpha1.CommitPhaseSuccess), now)
			Expect(wm).To(BeIdenticalTo(prev))
		})

		It("resets to the current commit after a rollback to an older healthy commit", func() {
			wm := advanceProgressWatermark(watermarkAt("b", t2), progressEnvStatus("dev", "a", t1, "a", promoterv1alpha1.CommitPhaseSuccess), now)
			Expect(wm.Sha).To(Equal("a"))
		})

		It("clears the watermark after a rollback to an older unhealthy commit", func() {
			Expect(advanceProgressWatermark(watermarkAt("b", t2), progressEnvStatus("dev", "a", t1, "a", promoterv1alpha1.CommitPhasePending), now)).To(BeNil())
		})

		It("keeps the previous watermark when the environment has no active commit", func() {
			prev := watermarkAt("a", t1)
			Expect(advanceProgressWatermark(prev, promoterv1alpha1.EnvironmentStatus{Branch: "dev"}, now)).To(BeIdenticalTo(prev))
		})
	})

	Describe("evaluateProgressUpstreams", func() {
		linear := func() *dag { g, _ := buildDAG(dagEnvs("dev", "", "stg", "dev", "prd", "stg")); return g }
		diamond := func() *dag {
			g, _ := buildDAG(dagEnvs("dev", "", "e2e", "dev", "perf", "dev", "prd", "e2e,perf"))
			return g
		}

		// The high-churn scenario this gate exists for: stg is always mid-rollout of the newest commit, so it
		// is never healthy on prd's target. A DependentsSuccessfulCommitStatus would hold prd forever; the
		// progressive gate lets prd move forward because stg has proven it is healthy past prd's active commit.
		It("lets a downstream promote while its upstream is rolling out a newer commit", func() {
			status := map[string]promoterv1alpha1.EnvironmentStatus{
				"stg": progressEnvStatus("stg", "c", t3, "c", promoterv1alpha1.CommitPhasePending),
				"prd": progressEnvStatus("prd", "a", t1, "c", promoterv1alpha1.CommitPhaseSuccess),
			}
			watermarks := map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark{"stg": watermarkAt("b", t2)}
			upstreams, pending, reason := evaluateProgressUpstreams(linear(), "prd", status["prd"].Active.Dry, status, watermarks)
			Expect(pending).To(BeFalse(), reason)
			Expect(upstreams).To(Equal([]promoterv1alpha1.ProgressiveOrderCommitStatusUpstreamStatus{{Branch: "stg", Satisfied: true}}))
		})

		It("holds pending when the upstream has not progressed beyond the downstream's active commit", func() {
			status := map[string]promoterv1alpha1.EnvironmentStatus{
				"stg": progressEnvStatus("stg", "c", t3, "c", promoterv1alpha1.CommitPhasePending),
				"prd": progressEnvStatus("prd", "b", t2, "c", promoterv1alpha1.CommitPhaseSuccess),
			}
			watermarks := map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark{"stg": watermarkAt("b", t2)}
			_, pending, reason := evaluateProgressUpstreams(linear(), "prd", status["prd"].Active.Dry, status, watermarks)
			Expect(pending).To(BeTrue())
			Expect(reason).To(Equal(`Waiting for "stg" to be healthy on a commit newer than b (last healthy on b)`))
		})

		It("holds pending when the upstream has never been healthy", func() {
			status := map[string]promoterv1alpha1.EnvironmentStatus{
				"stg": progressEnvStatus("stg", "c", t3, "c", promoterv1alpha1.CommitPhasePending),
			}
			_, pending, reason := evaluateProgressUpstreams(linear(), "prd", promoterv1alpha1.CommitShaState{}, status, nil)
			Expect(pending).To(BeTrue())
			Expect(reason).To(Equal(`Waiting for "stg" to be healthy on any commit`))
		})

		It("holds pending when the upstream is currently failing, even with a newer watermark", func() {
			status := map[string]promoterv1alpha1.EnvironmentStatus{
				"stg": progressEnvStatus("stg", "c", t3, "c", promoterv1alpha1.CommitPhaseFailure),
			}
			watermarks := map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark{"stg": watermarkAt("b", t2)}
			_, pending, reason := evaluateProgressUpstreams(linear(), "prd", promoterv1alpha1.CommitShaState{Sha: "a", CommitTime: metav1.NewTime(t1)}, status, watermarks)
			Expect(pending).To(BeTrue())
			Expect(reason).To(Equal(`"stg" environment's "argocd-health" commit status is failing`))
		})

		It("holds pending when upstream status is missing", func() {
			_, pending, reason := evaluateProgressUpstreams(linear(), "prd", promoterv1alpha1.CommitShaState{}, map[string]promoterv1alpha1.EnvironmentStatus{}, nil)
			Expect(pending).To(BeTrue())
			Expect(reason).To(Equal(`Waiting for "stg" environment status to be reported`))
		})

		It("lets a downstream with no active commit promote once the upstream has any watermark", func() {
			status := map[string]promoterv1alpha1.EnvironmentStatus{
				"stg": progressEnvStatus("stg", "a", t1, "a", promoterv1alpha1.CommitPhaseSuccess),
			}
			watermarks := map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark{"stg": watermarkAt("a", t1)}
			_, pending, _ := evaluateProgressUpstreams(linear(), "prd", promoterv1alpha1.CommitShaState{}, status, watermarks)
			Expect(pending).To(BeFalse())
		})

		It("is ready for a root that has no upstreams", func() {
			upstreams, pending, _ := evaluateProgressUpstreams(linear(), "dev", promoterv1alpha1.CommitShaState{}, nil, nil)
			Expect(pending).To(BeFalse())
			Expect(upstreams).To(BeEmpty())
		})

		It("fan-in: holds pending until every upstream has progressed", func() {
			prdActive := promoterv1alpha1.CommitShaState{Sha: "a", CommitTime: metav1.NewTime(t1)}
			status := map[string]promoterv1alpha1.EnvironmentStatus{
				"e2e":  progressEnvStatus("e2e", "c", t3, "c", promoterv1alpha1.CommitPhasePending),
				"perf": progressEnvStatus("perf", "c", t3, "c", promoterv1alpha1.CommitPhasePending),
			}
			watermarks := map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark{
				"e2e":  watermarkAt("b", t2),
				"perf": watermarkAt("a", t1),
			}
			upstreams, pending, reason := evaluateProgressUpstreams(diamond(), "prd", prdActive, status, watermarks)
			Expect(pending).To(BeTrue())
			Expect(reason).To(ContainSubstring(`"perf"`))
			Expect(upstreams).To(HaveLen(2))
			Expect(upstreams[0].Satisfied).To(BeTrue())
			Expect(upstreams[1].Satisfied).To(BeFalse())

			watermarks["perf"] = watermarkAt("b", t2)
			_, pending, _ = evaluateProgressUpstreams(diamond(), "prd", prdActive, status, watermarks)
			Expect(pending).To(BeFalse())
		})

		It("no-op: looks through a clean, healthy no-op upstream to its own upstream", func() {
			prdActive := promoterv1alpha1.CommitShaState{Sha: "a", CommitTime: metav1.NewTime(t1)}
			stg := progressEnvStatus("stg", "a", t1, "a", promoterv1alpha1.CommitPhaseSuccess)
			stg.Proposed.Note = &promoterv1alpha1.HydratorMetadata{DrySha: "c"}
			status := map[string]promoterv1alpha1.EnvironmentStatus{
				"dev": progressEnvStatus("dev", "c", t3, "c", promoterv1alpha1.CommitPhaseSuccess),
				"stg": stg,
			}
			watermarks := map[string]*promoterv1alpha1.ProgressiveOrderCommitStatusWatermark{
				"dev": watermarkAt("c", t3),
				"stg": watermarkAt("a", t1),
			}
			_, pending, reason := evaluateProgressUpstreams(linear(), "prd", prdActive, status, watermarks)
			Expect(pending).To(BeFalse(), reason)

			By("holding pending when the no-op upstream's own upstream has not progressed")
			watermarks["dev"] = watermarkAt("a", t1)
			_, pending, reason = evaluateProgressUpstreams(linear(), "prd", prdActive, status, watermarks)
			Expect(pending).To(BeTrue())
			Expect(reason).To(ContainSubstring(`"dev"`))
		})
	})

	Describe("watermarkIsAhead", func() {
		It("is false for the same SHA", func() {
			Expect(watermarkIsAhead(watermarkAt("a", t1), promoterv1alpha1.CommitShaState{Sha: "a", CommitTime: metav1.NewTime(t1)})).To(BeFalse())
		})
		It("is false for an older watermark", func() {
			Expect(watermarkIsAhead(watermarkAt("a", t0), promoterv1alpha1.CommitShaState{Sha: "b", CommitTime: metav1.NewTime(t1)})).To(BeFalse())
		})
		It("is true for a newer watermark", func() {
			Expect(watermarkIsAhead(watermarkAt("b", t2), promoterv1alpha1.CommitShaState{Sha: "a", CommitTime: metav1.NewTime(t1)})).To(BeTrue())
		})
		It("is false for a nil watermark", func() {
			Expect(watermarkIsAhead(nil, promoterv1alpha1.CommitShaState{})).To(BeFalse())
		})
	})
})
