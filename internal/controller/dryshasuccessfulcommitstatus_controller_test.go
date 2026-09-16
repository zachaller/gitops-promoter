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
	"encoding/json"
	"os"
	"path"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/git"
	promoterConditions "github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

//go:embed testdata/DryShaSuccessfulCommitStatus.yaml
var testDryShaSuccessfulCommitStatusYAML string

// drySha builds a syntactically valid 40-character dry SHA from a short label, so tests can refer to
// commits as "a", "b", "c" while still satisfying the CRD's SHA pattern.
func drySha(label string) string {
	return strings.Repeat(label, 40)[:40]
}

// dryShaRecord builds a git-derived record entry for the satisfaction-rule tests.
func dryShaRecord(sha string, successful bool) promoterv1alpha1.DryShaRecord {
	return promoterv1alpha1.DryShaRecord{
		Sha:        sha,
		MergeSha:   drySha("f"),
		Source:     promoterv1alpha1.DryShaRecordSourceNote,
		Successful: successful,
	}
}

// dryShaGate builds a gate CR for the unit tests, with allowNewerDrySha set explicitly.
func dryShaGate(allowNewer bool, historyDepth int32) *promoterv1alpha1.DryShaSuccessfulCommitStatus {
	return &promoterv1alpha1.DryShaSuccessfulCommitStatus{
		Spec: promoterv1alpha1.DryShaSuccessfulCommitStatusSpec{
			Key:              promoterv1alpha1.DryShaSuccessfulCommitStatusKey,
			AllowNewerDrySha: &allowNewer,
			HistoryDepth:     historyDepth,
		},
	}
}

// reportedEnvStatus builds the live PromotionStrategy environment status the gate reads.
func reportedEnvStatus(activeDry string, healthy bool) promoterv1alpha1.EnvironmentStatus {
	const branch = "dev"

	phase := "success"
	if !healthy {
		phase = "pending"
	}
	return promoterv1alpha1.EnvironmentStatus{
		Branch: branch,
		Active: promoterv1alpha1.CommitBranchState{
			Dry: promoterv1alpha1.CommitShaState{Sha: activeDry},
			CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				{Key: "argocd-health", Phase: phase},
			},
		},
	}
}

var _ = Describe("DryShaSuccessfulCommitStatus Controller", func() {
	Context("When unmarshalling the test data", func() {
		It("should unmarshal the DryShaSuccessfulCommitStatus resource", func() {
			err := unmarshalYamlStrict(testDryShaSuccessfulCommitStatusYAML, &promoterv1alpha1.DryShaSuccessfulCommitStatus{})
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When the PromotionStrategy is missing", func() {
		var (
			ctx  context.Context
			gate *promoterv1alpha1.DryShaSuccessfulCommitStatus
		)

		BeforeEach(func() {
			ctx = context.Background()
			By("Creating a DryShaSuccessfulCommitStatus that references a non-existent PromotionStrategy")
			gate = &promoterv1alpha1.DryShaSuccessfulCommitStatus{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dry-sha-missing-ps",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.DryShaSuccessfulCommitStatusSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{Name: "non-existent"},
					Key:                  promoterv1alpha1.DryShaSuccessfulCommitStatusKey,
				},
			}
			Expect(k8sClient.Create(ctx, gate)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, gate)
		})

		It("should set Ready=False when the PromotionStrategy is not found", func() {
			Eventually(func(g Gomega) {
				updated := &promoterv1alpha1.DryShaSuccessfulCommitStatus{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(gate), updated)).To(Succeed())
				readyCondition := meta.FindStatusCondition(updated.Status.Conditions, string(promoterConditions.Ready))
				g.Expect(readyCondition).ToNot(BeNil())
				g.Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
			}, constants.EventuallyTimeout).Should(Succeed())
		})
	})

	Context("When reconciling against a PromotionStrategy", func() {
		var (
			ctx               context.Context
			name              string
			scmSecret         *v1.Secret
			scmProvider       *promoterv1alpha1.ScmProvider
			gitRepo           *promoterv1alpha1.GitRepository
			promotionStrategy *promoterv1alpha1.PromotionStrategy
			gate              *promoterv1alpha1.DryShaSuccessfulCommitStatus
		)

		BeforeEach(func() {
			ctx = context.Background()

			By("Setting up test git repository and PromotionStrategy")
			name, scmSecret, scmProvider, gitRepo, _, _, promotionStrategy = promotionStrategyResource(ctx, "dry-sha-successful-commit-status-controller-test", "default")

			promotionStrategy.Spec.ProposedCommitStatuses = nil
			// Point the PromotionStrategy at this gate instead of the default ordering gate, so the
			// PromotionStrategy controller resolves spec.key through the typed branch in
			// ordercommitstatusgate.Resolve and injects it onto every ChangeTransferPolicy.
			promotionStrategy.Spec.OrderCommitStatusRef = promoterv1alpha1.OrderCommitStatusRef{
				Group: promoterv1alpha1.SchemeGroupVersion.Group,
				Kind:  promoterv1alpha1.OrderCommitStatusKindDryShaSuccessful,
				Name:  name,
			}
			setupInitialTestGitRepoOnServer(ctx, gitRepo)

			Expect(k8sClient.Create(ctx, scmSecret)).To(Succeed())
			Expect(k8sClient.Create(ctx, scmProvider)).To(Succeed())
			Expect(k8sClient.Create(ctx, gitRepo)).To(Succeed())

			// Create the gate before the PromotionStrategy so its first reconcile can resolve the
			// ordering gate rather than failing and waiting out the rate limiter.
			gate = &promoterv1alpha1.DryShaSuccessfulCommitStatus{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: promoterv1alpha1.DryShaSuccessfulCommitStatusSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{Name: name},
					Key:                  promoterv1alpha1.DryShaSuccessfulCommitStatusKey,
					URL: promoterv1alpha1.URLConfig{
						Template: "https://example.com/ui?env={{ .Environment }}",
					},
				},
			}
			Expect(k8sClient.Create(ctx, gate)).To(Succeed())
			Expect(k8sClient.Create(ctx, promotionStrategy)).To(Succeed())
		})

		AfterEach(func() {
			By("Cleaning up test resources")
			if gate != nil {
				_ = k8sClient.Delete(ctx, gate)
			}
			if promotionStrategy != nil {
				_ = k8sClient.Delete(ctx, promotionStrategy)
			}
			if gitRepo != nil {
				_ = k8sClient.Delete(ctx, gitRepo)
			}
			if scmProvider != nil {
				_ = k8sClient.Delete(ctx, scmProvider)
			}
			if scmSecret != nil {
				_ = k8sClient.Delete(ctx, scmSecret)
			}
		})

		It("should mirror gate fields on status.environments from child CommitStatuses", func() {
			By("Wiring explicit dependsOn on the PromotionStrategy")
			Eventually(func(g Gomega) {
				ps := &promoterv1alpha1.PromotionStrategy{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(promotionStrategy), ps)).To(Succeed())
				ps.Spec.Environments = psEnvsWithLinearDependsOn(ps.Spec.Environments)
				g.Expect(k8sClient.Update(ctx, ps)).To(Succeed())
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Waiting for the DryShaSuccessfulCommitStatus to become Ready")
			Eventually(func(g Gomega) {
				updated := &promoterv1alpha1.DryShaSuccessfulCommitStatus{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(gate), updated)).To(Succeed())
				readyCondition := meta.FindStatusCondition(updated.Status.Conditions, string(promoterConditions.Ready))
				g.Expect(readyCondition).ToNot(BeNil())
				g.Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Creating a proposed change so the gate writes CommitStatuses and status.environments")
			gitPath, err := os.MkdirTemp("", "*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(gitPath) })
			makeChangeAndHydrateRepo(gitPath, gitRepo, "dry sha status environments test change", "")

			By("Checking status.environments gate fields mirror child CommitStatus spec")
			Eventually(func(g Gomega) {
				updated := &promoterv1alpha1.DryShaSuccessfulCommitStatus{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(gate), updated)).To(Succeed())
				g.Expect(updated.Status.Environments).ToNot(BeEmpty())

				var eligible int
				for i := range updated.Status.Environments {
					envStatus := updated.Status.Environments[i]
					cs := &promoterv1alpha1.CommitStatus{}
					csName := utils.CommitStatusResourceName(ctx, gate, envStatus.Branch)
					err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: csName}, cs)
					if k8serrors.IsNotFound(err) {
						// No in-flight change for this branch, so the gate writes no child.
						continue
					}
					g.Expect(err).NotTo(HaveOccurred())
					eligible++
					g.Expect(cs.Spec.Name).To(Equal(promoterv1alpha1.DryShaSuccessfulCommitStatusKey),
						"spec.name must be the bare key so branch protection can reference one context")
					g.Expect(cs.Labels[promoterv1alpha1.CommitStatusLabel]).To(Equal(promoterv1alpha1.DryShaSuccessfulCommitStatusKey))
					g.Expect(cs.Labels[promoterv1alpha1.EnvironmentLabel]).To(Equal(utils.KubeSafeLabel(envStatus.Branch)))
					g.Expect(envStatus.Phase).NotTo(BeEmpty(), "branch %s has a child CommitStatus but no gate fields on status.environments", envStatus.Branch)
					g.Expect(envStatus.Phase).To(Equal(cs.Spec.Phase), "branch %s", envStatus.Branch)
					g.Expect(envStatus.Description).To(Equal(cs.Spec.Description), "branch %s", envStatus.Branch)
					g.Expect(envStatus.Url).To(Equal(cs.Spec.Url), "branch %s", envStatus.Branch)
					g.Expect(envStatus.ReportedSha).To(Equal(cs.Spec.Sha), "branch %s", envStatus.Branch)
				}
				g.Expect(eligible).To(BeNumerically(">=", 1), "expected at least one child CommitStatus before checking status.environments mirrors")
			}, constants.EventuallyTimeout).Should(Succeed())
		})

		It("should rebuild the dry SHA record from the active branch and cache it on status", func() {
			By("Creating a proposed change so the upstream environments have records to walk")
			gitPath, err := os.MkdirTemp("", "*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(gitPath) })
			makeChangeAndHydrateRepo(gitPath, gitRepo, "dry sha record rebuild test change", "")

			By("Checking the cache records the environment's running dry commit and its walk origin")
			Eventually(func(g Gomega) {
				updated := &promoterv1alpha1.DryShaSuccessfulCommitStatus{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(gate), updated)).To(Succeed())
				g.Expect(updated.Status.Environments).ToNot(BeEmpty())

				ps := &promoterv1alpha1.PromotionStrategy{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(promotionStrategy), ps)).To(Succeed())

				var withRecords int
				for _, envStatus := range updated.Status.Environments {
					if len(envStatus.DryShaHistory) == 0 {
						continue
					}
					withRecords++
					g.Expect(envStatus.DryShaHistory[0].Source).To(Equal(promoterv1alpha1.DryShaRecordSourceLive),
						"the currently running dry commit has no note yet and must come from live status")
					for _, psEnv := range ps.Status.Environments {
						if psEnv.Branch != envStatus.Branch {
							continue
						}
						g.Expect(envStatus.DryShaHistory[0].Sha).To(Equal(psEnv.Active.Dry.Sha), "branch %s", envStatus.Branch)
						if envStatus.RebuiltFromSha != "" {
							g.Expect(envStatus.RebuiltFromSha).To(Equal(psEnv.Active.Hydrated.Sha),
								"rebuiltFromSha is the cache key and must be the active branch tip, branch %s", envStatus.Branch)
						}
					}
				}
				g.Expect(withRecords).To(BeNumerically(">=", 1), "expected at least one environment with a dry SHA record")
			}, constants.EventuallyTimeout).Should(Succeed())
		})

		It("should set Ready=False when dependsOn references an unknown branch", func() {
			By("Pointing the first environment at a branch that does not exist")
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(promotionStrategy), promotionStrategy)).To(Succeed())
			promotionStrategy.Spec.Environments[0].DependsOn = []string{"environment/does-not-exist"}
			Expect(k8sClient.Update(ctx, promotionStrategy)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &promoterv1alpha1.DryShaSuccessfulCommitStatus{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(gate), updated)).To(Succeed())
				readyCondition := meta.FindStatusCondition(updated.Status.Conditions, string(promoterConditions.Ready))
				g.Expect(readyCondition).ToNot(BeNil())
				g.Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCondition.Message).To(ContainSubstring("does-not-exist"))
			}, constants.EventuallyTimeout).Should(Succeed())
		})
	})
})

var _ = Describe("evaluateDryShaUpstream", func() {
	const branch = "dev"

	var (
		shaA = drySha("a")
		shaB = drySha("b")
		shaC = drySha("c")
	)

	It("is satisfied when the target dry commit itself was successful", func() {
		records := []promoterv1alpha1.DryShaRecord{dryShaRecord(shaB, true), dryShaRecord(shaA, true)}
		result := evaluateDryShaUpstream(dryShaGate(true, 20), branch, shaA, reportedEnvStatus(shaC, true), records)
		Expect(result.Satisfied).To(BeTrue())
		Expect(result.SatisfiedBySha).To(Equal(shaA))
	})

	It("stays satisfied after the upstream has promoted past the target", func() {
		// This is the case DependentsSuccessfulCommitStatus cannot pass: the upstream is no longer sitting
		// on the target, but it ran it successfully on the way past.
		records := []promoterv1alpha1.DryShaRecord{
			dryShaRecord(shaC, true),
			dryShaRecord(shaB, true),
			dryShaRecord(shaA, true),
		}
		result := evaluateDryShaUpstream(dryShaGate(true, 20), branch, shaA, reportedEnvStatus(shaC, true), records)
		Expect(result.Satisfied).To(BeTrue())
		Expect(result.SatisfiedBySha).To(Equal(shaA))
	})

	It("is satisfied by a descendant that was successful when the target itself was not", func() {
		records := []promoterv1alpha1.DryShaRecord{dryShaRecord(shaB, true), dryShaRecord(shaA, false)}
		result := evaluateDryShaUpstream(dryShaGate(true, 20), branch, shaA, reportedEnvStatus(shaC, true), records)
		Expect(result.Satisfied).To(BeTrue())
		Expect(result.SatisfiedBySha).To(Equal(shaB),
			"a later first-parent entry necessarily carries the target's content")
	})

	It("is not satisfied by a descendant when allowNewerDrySha is false", func() {
		records := []promoterv1alpha1.DryShaRecord{dryShaRecord(shaB, true), dryShaRecord(shaA, false)}
		result := evaluateDryShaUpstream(dryShaGate(false, 20), branch, shaA, reportedEnvStatus(shaC, true), records)
		Expect(result.Satisfied).To(BeFalse())
		Expect(result.Reason).To(ContainSubstring("successful"))
	})

	It("is not satisfied when the target was unsuccessful and nothing newer succeeded", func() {
		records := []promoterv1alpha1.DryShaRecord{dryShaRecord(shaB, false), dryShaRecord(shaA, false)}
		result := evaluateDryShaUpstream(dryShaGate(true, 20), branch, shaA, reportedEnvStatus(shaB, false), records)
		Expect(result.Satisfied).To(BeFalse())
	})

	It("does not count an older success as satisfying a newer target", func() {
		// shaA succeeded before shaB was promoted, so it says nothing about shaB.
		records := []promoterv1alpha1.DryShaRecord{dryShaRecord(shaB, false), dryShaRecord(shaA, true)}
		result := evaluateDryShaUpstream(dryShaGate(true, 20), branch, shaB, reportedEnvStatus(shaB, false), records)
		Expect(result.Satisfied).To(BeFalse())
	})

	It("reports a waiting-for-promotion reason when the target never reached the upstream", func() {
		records := []promoterv1alpha1.DryShaRecord{dryShaRecord(shaA, true)}
		result := evaluateDryShaUpstream(dryShaGate(true, 20), branch, shaC, reportedEnvStatus(shaA, true), records)
		Expect(result.Satisfied).To(BeFalse())
		Expect(result.Reason).To(ContainSubstring("to be promoted"))
	})

	It("names the history depth when the record filled the whole walk without the target", func() {
		records := []promoterv1alpha1.DryShaRecord{dryShaRecord(shaA, true), dryShaRecord(shaB, true)}
		result := evaluateDryShaUpstream(dryShaGate(true, 2), branch, shaC, reportedEnvStatus(shaA, true), records)
		Expect(result.Satisfied).To(BeFalse())
		Expect(result.Reason).To(ContainSubstring("historyDepth"))
	})

	It("waits for the hydrator when the target dry SHA is not yet known", func() {
		result := evaluateDryShaUpstream(dryShaGate(true, 20), branch, "", reportedEnvStatus(shaA, true), nil)
		Expect(result.Satisfied).To(BeFalse())
		Expect(result.Reason).To(ContainSubstring("hydrator"))
	})

	It("waits when the upstream has not reported any environment status", func() {
		result := evaluateDryShaUpstream(dryShaGate(true, 20), branch, shaA, promoterv1alpha1.EnvironmentStatus{}, nil)
		Expect(result.Satisfied).To(BeFalse())
		Expect(result.Reason).To(ContainSubstring("environment status to be reported"))
	})
})

var _ = Describe("withLiveHeadRecord", func() {
	var (
		shaA = drySha("a")
		shaB = drySha("b")
	)

	It("prepends the currently running dry commit, which has no note yet", func() {
		records := withLiveHeadRecord(
			[]promoterv1alpha1.DryShaRecord{dryShaRecord(shaA, true)},
			reportedEnvStatus(shaB, true),
		)
		Expect(records).To(HaveLen(2))
		Expect(records[0].Sha).To(Equal(shaB))
		Expect(records[0].Source).To(Equal(promoterv1alpha1.DryShaRecordSourceLive))
		Expect(records[0].Successful).To(BeTrue())
		Expect(records[1].Sha).To(Equal(shaA))
	})

	It("tracks live health without the branch tip moving", func() {
		records := withLiveHeadRecord(nil, reportedEnvStatus(shaB, false))
		Expect(records).To(HaveLen(1))
		Expect(records[0].Successful).To(BeFalse())
	})

	It("replaces a stale live entry from a previous reconcile", func() {
		previous := withLiveHeadRecord(nil, reportedEnvStatus(shaA, true))
		updated := withLiveHeadRecord(previous, reportedEnvStatus(shaB, true))
		Expect(updated).To(HaveLen(1), "the old live entry must not linger once the environment moves on")
		Expect(updated[0].Sha).To(Equal(shaB))
	})

	It("returns the record unchanged when no active dry commit is reported", func() {
		records := withLiveHeadRecord(
			[]promoterv1alpha1.DryShaRecord{dryShaRecord(shaA, true)},
			promoterv1alpha1.EnvironmentStatus{Branch: "dev"},
		)
		Expect(records).To(HaveLen(1))
		Expect(records[0].Sha).To(Equal(shaA))
	})
})

var _ = Describe("branchesNeedingRecords", func() {
	// inFlight builds an environment status whose active dry commit is "a"; a proposedDry other than
	// "a" means the environment has a change in flight.
	inFlight := func(branch, proposedDry string) promoterv1alpha1.EnvironmentStatus {
		return promoterv1alpha1.EnvironmentStatus{
			Branch:   branch,
			Active:   promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: "a"}},
			Proposed: promoterv1alpha1.CommitBranchState{Dry: promoterv1alpha1.CommitShaState{Sha: proposedDry}},
		}
	}

	It("returns only the ancestors of environments with an in-flight change", func() {
		graph, err := buildDAG(dagEnvs("dev", "", "stg", "dev", "prod", "stg"))
		Expect(err).NotTo(HaveOccurred())

		statusByBranch := map[string]promoterv1alpha1.EnvironmentStatus{
			"dev":  inFlight("dev", "a"),
			"stg":  inFlight("stg", "b"),
			"prod": inFlight("prod", "a"),
		}

		Expect(branchesNeedingRecords(graph, statusByBranch)).To(Equal([]string{"dev"}),
			"only stg has a change in flight, so only its ancestors are walked")
	})

	It("returns nothing when no environment has a change in flight", func() {
		graph, err := buildDAG(dagEnvs("dev", "", "stg", "dev"))
		Expect(err).NotTo(HaveOccurred())

		statusByBranch := map[string]promoterv1alpha1.EnvironmentStatus{
			"dev": inFlight("dev", "a"),
			"stg": inFlight("stg", "a"),
		}

		Expect(branchesNeedingRecords(graph, statusByBranch)).To(BeEmpty(),
			"a quiet reconcile must not touch git at all")
	})

	It("returns ancestors in graph order", func() {
		graph, err := buildDAG(dagEnvs("dev", "", "stg", "dev", "prod", "dev,stg"))
		Expect(err).NotTo(HaveOccurred())

		statusByBranch := map[string]promoterv1alpha1.EnvironmentStatus{
			"dev":  inFlight("dev", "a"),
			"stg":  inFlight("stg", "a"),
			"prod": inFlight("prod", "b"),
		}

		Expect(branchesNeedingRecords(graph, statusByBranch)).To(Equal([]string{"dev", "stg"}))
	})
})

var _ = Describe("dryShaRecordForCommit", func() {
	var (
		workDir string
		bareDir string
		branch  string
		gitOps  *git.EnvironmentOperations
	)

	const (
		activeDry = "1111111111111111111111111111111111111111"
		olderDry  = "2222222222222222222222222222222222222222"
	)

	mustRunGit := func(dir string, args ...string) string {
		GinkgoHelper()
		out, err := runGitCmd(ctx, dir, args...)
		Expect(err).NotTo(HaveOccurred())
		return strings.TrimSpace(out)
	}

	// commitWithTrailers creates a commit whose message carries the given trailers, mimicking a
	// controller-performed merge commit, and returns its SHA.
	commitWithTrailers := func(message string, trailers map[string]string) string {
		GinkgoHelper()
		var body strings.Builder
		body.WriteString(message + "\n\n")
		for _, key := range []string{
			constants.TrailerShaDryActive,
			constants.TrailerPullRequestMergeTime,
			constants.TrailerCommitStatusActivePrefix + "argocd-health-phase",
			constants.TrailerCommitStatusActivePrefix + "e2e-phase",
		} {
			if value, ok := trailers[key]; ok {
				body.WriteString(key + ": " + value + "\n")
			}
		}
		Expect(os.WriteFile(path.Join(workDir, "file.txt"), []byte(message), 0o644)).To(Succeed())
		mustRunGit(workDir, "add", "-A")
		mustRunGit(workDir, "commit", "-m", body.String())
		return mustRunGit(workDir, "rev-parse", "HEAD")
	}

	addNote := func(sha string, trailers map[string][]string) {
		GinkgoHelper()
		payload, err := json.Marshal(trailers)
		Expect(err).NotTo(HaveOccurred())
		mustRunGit(workDir, "notes", "--ref="+git.PromoterHistoryNotesRef, "add", "-f", "-m", string(payload), sha)
		mustRunGit(workDir, "push", "origin", git.PromoterHistoryNotesRef)
		Expect(gitOps.FetchNotes(ctx)).To(Succeed())
	}

	BeforeEach(func() {
		var err error
		bareDir, err = os.MkdirTemp("", "dry-sha-bare-*")
		Expect(err).NotTo(HaveOccurred())
		mustRunGit(bareDir, "init", "--bare")

		workDir, err = os.MkdirTemp("", "dry-sha-work-*")
		Expect(err).NotTo(HaveOccurred())
		mustRunGit(workDir, "clone", bareDir, ".")
		mustRunGit(workDir, "config", "user.name", "Test User")
		mustRunGit(workDir, "config", "user.email", "test@example.com")
		mustRunGit(workDir, "config", "commit.gpgsign", "false")

		commitWithTrailers("initial", nil)
		branch = mustRunGit(workDir, "rev-parse", "--abbrev-ref", "HEAD")
		mustRunGit(workDir, "push", "-u", "origin", branch)

		gitRepo := &promoterv1alpha1.GitRepository{
			Name: "dry-sha-repo", Namespace: "default",
			Spec: promoterv1alpha1.GitRepositorySpec{
				Fake: &promoterv1alpha1.FakeRepo{Owner: "test-owner", Name: "dry-sha-repo"},
			},
		}
		gitOps = git.NewEnvironmentOperations(gitRepo, &localGitProvider{repoPath: bareDir}, "default/dry-sha-"+branch)
		Expect(gitOps.CloneRepo(ctx)).To(Succeed())
		Expect(gitOps.FetchBranch(ctx, branch)).To(Succeed())
		Expect(gitOps.FetchNotes(ctx)).To(Succeed())
	})

	AfterEach(func() {
		_ = os.RemoveAll(bareDir)
		_ = os.RemoveAll(workDir)
	})

	It("pairs Sha-dry-active with the active commit statuses recorded in the same snapshot", func() {
		sha := commitWithTrailers("promote", nil)
		mustRunGit(workDir, "push", "origin", branch)
		Expect(gitOps.FetchBranch(ctx, branch)).To(Succeed())
		addNote(sha, map[string][]string{
			constants.TrailerShaDryActive:                                     {activeDry},
			constants.TrailerCommitStatusActivePrefix + "argocd-health-phase": {"success"},
			constants.TrailerCommitStatusActivePrefix + "e2e-phase":           {"success"},
			constants.TrailerPullRequestMergeTime:                             {"2026-09-14T12:00:00Z"},
		})

		r := &DryShaSuccessfulCommitStatusReconciler{}
		record, ok := r.dryShaRecordForCommit(ctx, gitOps, sha)
		Expect(ok).To(BeTrue())
		Expect(record.Sha).To(Equal(activeDry),
			"the record must describe what was RUNNING before the merge, not what merged")
		Expect(record.MergeSha).To(Equal(sha))
		Expect(record.Source).To(Equal(promoterv1alpha1.DryShaRecordSourceNote))
		Expect(record.Successful).To(BeTrue())
		Expect(record.MergedAt.UTC().Format("2006-01-02")).To(Equal("2026-09-14"))
	})

	It("marks the entry unsuccessful when any recorded active commit status was not successful", func() {
		sha := commitWithTrailers("promote-unhealthy", nil)
		mustRunGit(workDir, "push", "origin", branch)
		Expect(gitOps.FetchBranch(ctx, branch)).To(Succeed())
		addNote(sha, map[string][]string{
			constants.TrailerShaDryActive:                                     {activeDry},
			constants.TrailerCommitStatusActivePrefix + "argocd-health-phase": {"success"},
			constants.TrailerCommitStatusActivePrefix + "e2e-phase":           {"pending"},
		})

		r := &DryShaSuccessfulCommitStatusReconciler{}
		record, ok := r.dryShaRecordForCommit(ctx, gitOps, sha)
		Expect(ok).To(BeTrue())
		Expect(record.Successful).To(BeFalse())
	})

	It("falls back to the merge commit's own trailers when no note exists", func() {
		sha := commitWithTrailers("promote-no-note", map[string]string{
			constants.TrailerShaDryActive:                                     olderDry,
			constants.TrailerCommitStatusActivePrefix + "argocd-health-phase": "success",
		})
		mustRunGit(workDir, "push", "origin", branch)
		Expect(gitOps.FetchBranch(ctx, branch)).To(Succeed())

		r := &DryShaSuccessfulCommitStatusReconciler{}
		record, ok := r.dryShaRecordForCommit(ctx, gitOps, sha)
		Expect(ok).To(BeTrue())
		Expect(record.Sha).To(Equal(olderDry))
		Expect(record.Source).To(Equal(promoterv1alpha1.DryShaRecordSourceCommitMessage))
		Expect(record.Successful).To(BeTrue())
	})

	It("prefers the note over the commit message trailers", func() {
		sha := commitWithTrailers("promote-both", map[string]string{
			constants.TrailerShaDryActive:                                     olderDry,
			constants.TrailerCommitStatusActivePrefix + "argocd-health-phase": "success",
		})
		mustRunGit(workDir, "push", "origin", branch)
		Expect(gitOps.FetchBranch(ctx, branch)).To(Succeed())
		addNote(sha, map[string][]string{
			constants.TrailerShaDryActive:                                     {activeDry},
			constants.TrailerCommitStatusActivePrefix + "argocd-health-phase": {"failure"},
		})

		r := &DryShaSuccessfulCommitStatusReconciler{}
		record, ok := r.dryShaRecordForCommit(ctx, gitOps, sha)
		Expect(ok).To(BeTrue())
		Expect(record.Sha).To(Equal(activeDry), "the note is the durable copy and must win")
		Expect(record.Successful).To(BeFalse())
	})

	It("skips a commit that carries no Sha-dry-active trailer", func() {
		sha := commitWithTrailers("no-trailers", nil)
		mustRunGit(workDir, "push", "origin", branch)
		Expect(gitOps.FetchBranch(ctx, branch)).To(Succeed())

		r := &DryShaSuccessfulCommitStatusReconciler{}
		_, ok := r.dryShaRecordForCommit(ctx, gitOps, sha)
		Expect(ok).To(BeFalse(),
			"a pull request merged before the promoter refreshed its message carries no trailers")
	})
})
