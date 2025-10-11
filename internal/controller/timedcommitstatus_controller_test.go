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
	"os"
	"time"

	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

var _ = Describe("TimedCommitStatus Controller", func() {
	Context("When reconciling a TimedCommitStatus with time gates", func() {
		ctx := context.Background()

		It("should create CommitStatus resources with correct phases based on time elapsed", func() {
			By("Setting up the test environment")
			name, scmSecret, scmProvider, gitRepo, _, _, promotionStrategy := promotionStrategyResource(ctx, "timed-commit-status-test", "default")
			setupInitialTestGitRepoOnServer(name, name)

			Expect(k8sClient.Create(ctx, scmSecret)).To(Succeed())
			Expect(k8sClient.Create(ctx, scmProvider)).To(Succeed())
			Expect(k8sClient.Create(ctx, gitRepo)).To(Succeed())
			Expect(k8sClient.Create(ctx, promotionStrategy)).To(Succeed())

			By("Creating a TimedCommitStatus resource")
			timedCommitStatus := &promoterv1alpha1.TimedCommitStatus{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-tcs",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.TimedCommitStatusSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					Environment: []promoterv1alpha1.EnvironmentTimeCommitStatus{
						{
							Branch:   "environment/development",
							Duration: metav1.Duration{Duration: 5 * time.Minute},
						},
						{
							Branch:   "environment/staging",
							Duration: metav1.Duration{Duration: 10 * time.Minute},
						},
						{
							Branch:   "environment/production",
							Duration: metav1.Duration{Duration: 30 * time.Minute},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, timedCommitStatus)).To(Succeed())

			By("Waiting for PromotionStrategy to be reconciled")
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: "default",
				}, promotionStrategy)
				g.Expect(err).To(Succeed())
				g.Expect(len(promotionStrategy.Status.Environments)).To(Equal(3))
				g.Expect(promotionStrategy.Status.Environments[0].Active.Hydrated.Sha).ToNot(BeEmpty())
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Verifying CommitStatus for development environment is created with success phase (first environment)")
			Eventually(func(g Gomega) {
				commitStatus := &promoterv1alpha1.CommitStatus{}
				commitStatusName := utils.KubeSafeUniqueName(ctx, name+"-tcs-environment/development")
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      commitStatusName,
					Namespace: "default",
				}, commitStatus)
				g.Expect(err).To(Succeed())
				g.Expect(commitStatus.Spec.Phase).To(Equal(promoterv1alpha1.CommitPhaseSuccess))
				g.Expect(commitStatus.Spec.Name).To(Equal("promoter/timed/environment/development"))
				g.Expect(commitStatus.Spec.Description).To(ContainSubstring("First environment"))
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Adding a change to trigger promotion")
			gitPath, err := os.MkdirTemp("", "*")
			Expect(err).NotTo(HaveOccurred())
			makeChangeAndHydrateRepo(gitPath, name, name, "change for time gate test", "test commit body")

			// Get the CTPs to simulate webhooks
			ctpDev := promoterv1alpha1.ChangeTransferPolicy{}
			ctpStaging := promoterv1alpha1.ChangeTransferPolicy{}
			ctpProd := promoterv1alpha1.ChangeTransferPolicy{}

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      utils.KubeSafeUniqueName(ctx, utils.GetChangeTransferPolicyName(promotionStrategy.Name, "environment/development")),
					Namespace: "default",
				}, &ctpDev)
				g.Expect(err).To(Succeed())
			}, constants.EventuallyTimeout).Should(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      utils.KubeSafeUniqueName(ctx, utils.GetChangeTransferPolicyName(promotionStrategy.Name, "environment/staging")),
					Namespace: "default",
				}, &ctpStaging)
				g.Expect(err).To(Succeed())
			}, constants.EventuallyTimeout).Should(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      utils.KubeSafeUniqueName(ctx, utils.GetChangeTransferPolicyName(promotionStrategy.Name, "environment/production")),
					Namespace: "default",
				}, &ctpProd)
				g.Expect(err).To(Succeed())
			}, constants.EventuallyTimeout).Should(Succeed())

			simulateWebhook(ctx, k8sClient, &ctpDev)
			simulateWebhook(ctx, k8sClient, &ctpStaging)
			simulateWebhook(ctx, k8sClient, &ctpProd)

			By("Waiting for PRs to be merged in development")
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: "default",
				}, promotionStrategy)
				g.Expect(err).To(Succeed())
				// Development PR should be merged quickly (no commit status gates in base PromotionStrategy)
				g.Expect(promotionStrategy.Status.Environments[0].PullRequest).To(BeNil())
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Verifying CommitStatus for staging shows pending (waiting for time gate)")
			Eventually(func(g Gomega) {
				commitStatus := &promoterv1alpha1.CommitStatus{}
				commitStatusName := utils.KubeSafeUniqueName(ctx, name+"-tcs-environment/staging")
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      commitStatusName,
					Namespace: "default",
				}, commitStatus)
				g.Expect(err).To(Succeed())
				// Should be pending initially as the time hasn't elapsed
				g.Expect(commitStatus.Spec.Phase).To(Equal(promoterv1alpha1.CommitPhasePending))
				g.Expect(commitStatus.Spec.Description).To(ContainSubstring("Waiting"))
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Cleaning up resources")
			Expect(k8sClient.Delete(ctx, timedCommitStatus)).To(Succeed())
			Expect(k8sClient.Delete(ctx, promotionStrategy)).To(Succeed())
		})

		It("should report CommitStatus on the active hydrated SHA", func() {
			By("Setting up the test environment")
			name, scmSecret, scmProvider, gitRepo, _, _, promotionStrategy := promotionStrategyResource(ctx, "timed-commit-status-sha-test", "default")
			setupInitialTestGitRepoOnServer(name, name)

			Expect(k8sClient.Create(ctx, scmSecret)).To(Succeed())
			Expect(k8sClient.Create(ctx, scmProvider)).To(Succeed())
			Expect(k8sClient.Create(ctx, gitRepo)).To(Succeed())
			Expect(k8sClient.Create(ctx, promotionStrategy)).To(Succeed())

			By("Waiting for PromotionStrategy to be reconciled")
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: "default",
				}, promotionStrategy)
				g.Expect(err).To(Succeed())
				g.Expect(len(promotionStrategy.Status.Environments)).To(Equal(3))
				g.Expect(promotionStrategy.Status.Environments[0].Active.Hydrated.Sha).ToNot(BeEmpty())
				g.Expect(promotionStrategy.Status.Environments[0].Active.Dry.Sha).ToNot(BeEmpty())
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Creating a TimedCommitStatus resource")
			timedCommitStatus := &promoterv1alpha1.TimedCommitStatus{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-tcs",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.TimedCommitStatusSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					Environment: []promoterv1alpha1.EnvironmentTimeCommitStatus{
						{
							Branch:   "environment/development",
							Duration: metav1.Duration{Duration: 1 * time.Minute},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, timedCommitStatus)).To(Succeed())

			By("Waiting for TimedCommitStatus to be reconciled and create CommitStatus")
			var activeHydratedSha string
			Eventually(func(g Gomega) {
				// Get the current active hydrated SHA from PromotionStrategy
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: "default",
				}, promotionStrategy)
				g.Expect(err).To(Succeed())
				activeHydratedSha = promotionStrategy.Status.Environments[0].Active.Hydrated.Sha
				g.Expect(activeHydratedSha).ToNot(BeEmpty())

				// Verify CommitStatus is created and uses that SHA
				commitStatus := &promoterv1alpha1.CommitStatus{}
				commitStatusName := utils.KubeSafeUniqueName(ctx, name+"-tcs-environment/development")
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      commitStatusName,
					Namespace: "default",
				}, commitStatus)
				g.Expect(err).To(Succeed())
				g.Expect(commitStatus.Spec.Sha).To(Equal(activeHydratedSha))
				g.Expect(commitStatus.Spec.RepositoryReference.Name).To(Equal(name))
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Cleaning up resources")
			Expect(k8sClient.Delete(ctx, timedCommitStatus)).To(Succeed())
			Expect(k8sClient.Delete(ctx, promotionStrategy)).To(Succeed())
		})

		It("should handle missing PromotionStrategy gracefully", func() {
			By("Creating a TimedCommitStatus without a PromotionStrategy")
			name := "tcs-no-ps-" + utils.KubeSafeUniqueName(ctx, randomString(10))
			timedCommitStatus := &promoterv1alpha1.TimedCommitStatus{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: promoterv1alpha1.TimedCommitStatusSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: "non-existent-ps",
					},
					Environment: []promoterv1alpha1.EnvironmentTimeCommitStatus{
						{
							Branch:   "environment/development",
							Duration: metav1.Duration{Duration: 5 * time.Minute},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, timedCommitStatus)).To(Succeed())

			By("Verifying the TimedCommitStatus shows error condition")
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: "default",
				}, timedCommitStatus)
				g.Expect(err).To(Succeed())
				// The status should have error conditions
				g.Expect(len(timedCommitStatus.Status.Conditions)).To(BeNumerically(">", 0))
			}, constants.EventuallyTimeout).Should(Succeed())

			By("Cleaning up resources")
			Expect(k8sClient.Delete(ctx, timedCommitStatus)).To(Succeed())
		})
	})
})
