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
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
)

var _ = Describe("PromotionEventHook Controller", Ordered, func() {
	var (
		ctx               context.Context
		name              string
		scmSecret         *corev1.Secret
		scmProvider       *promoterv1alpha1.ScmProvider
		gitRepo           *promoterv1alpha1.GitRepository
		promotionStrategy *promoterv1alpha1.PromotionStrategy
	)

	BeforeAll(func() {
		ctx = context.Background()

		By("Setting up test git repository and resources")
		name, scmSecret, scmProvider, gitRepo, _, _, promotionStrategy = promotionStrategyResource(ctx, "promotion-event-hook-test", "default")

		setupInitialTestGitRepoOnServer(ctx, name, name)

		Expect(k8sClient.Create(ctx, scmSecret)).To(Succeed())
		Expect(k8sClient.Create(ctx, scmProvider)).To(Succeed())
		Expect(k8sClient.Create(ctx, gitRepo)).To(Succeed())
		Expect(k8sClient.Create(ctx, promotionStrategy)).To(Succeed())

		By("Waiting for PromotionStrategy to be reconciled with initial state")
		Eventually(func(g Gomega) {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: "default",
			}, promotionStrategy)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(promotionStrategy.Status.Environments).To(HaveLen(3))
			// Ensure all environments have active hydrated commits
			for _, env := range promotionStrategy.Status.Environments {
				g.Expect(env.Active.Hydrated.Sha).ToNot(BeEmpty(), "Active hydrated SHA should be set for "+env.Branch)
			}
		}, constants.EventuallyTimeout).Should(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up test resources")
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

	Describe("Basic Trigger Expression", func() {
		var (
			testServer    *httptest.Server
			webhookCalled bool
		)

		AfterEach(func() {
			if testServer != nil {
				testServer.Close()
			}
		})

		It("should evaluate triggerExpr and fire when trigger is true", func() {
			webhookCalled = false

			// Setup mock webhook server
			testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				webhookCalled = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status": "success"}`))
			}))

			// Create PromotionEventHook with a simple trigger that always fires
			peh := &promoterv1alpha1.PromotionEventHook{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-trigger-true",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.PromotionEventHookSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					TriggerExpr: `{trigger: true, testData: "hello"}`,
					Action: promoterv1alpha1.PromotionEventHookAction{
						Webhook: &promoterv1alpha1.WebhookAction{
							URL:    testServer.URL,
							Method: "POST",
							Headers: map[string]string{
								"Content-Type": "application/json",
							},
							Body: `{"message": "triggered", "strategy": "{{ .PromotionStrategy.Name }}"}`,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, peh)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, peh)
			}()

			// Wait for webhook to be called
			Eventually(func(g Gomega) {
				g.Expect(webhookCalled).To(BeTrue())
			}, constants.EventuallyTimeout).Should(Succeed())

			// Verify status was updated
			Eventually(func(g Gomega) {
				var fetchedPEH promoterv1alpha1.PromotionEventHook
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(peh), &fetchedPEH)).To(Succeed())
				g.Expect(fetchedPEH.Status.TriggerData).To(HaveKeyWithValue("testData", "hello"))
				g.Expect(fetchedPEH.Status.WebhookStatus).NotTo(BeNil())
				g.Expect(fetchedPEH.Status.WebhookStatus.Success).To(BeTrue())
				g.Expect(fetchedPEH.Status.LastTriggerTime).NotTo(BeNil())
			}, constants.EventuallyTimeout).Should(Succeed())
		})

		It("should not fire when trigger is false", func() {
			webhookCalled = false

			// Setup mock webhook server
			testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				webhookCalled = true
				w.WriteHeader(http.StatusOK)
			}))

			// Create PromotionEventHook with a trigger that never fires
			peh := &promoterv1alpha1.PromotionEventHook{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-trigger-false",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.PromotionEventHookSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					TriggerExpr: `{trigger: false}`,
					Action: promoterv1alpha1.PromotionEventHookAction{
						Webhook: &promoterv1alpha1.WebhookAction{
							URL:    testServer.URL,
							Method: "POST",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, peh)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, peh)
			}()

			// Wait for evaluation to occur
			Eventually(func(g Gomega) {
				var fetchedPEH promoterv1alpha1.PromotionEventHook
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(peh), &fetchedPEH)).To(Succeed())
				g.Expect(fetchedPEH.Status.LastEvaluationTime).NotTo(BeNil())
			}, constants.EventuallyTimeout).Should(Succeed())

			// Verify webhook was NOT called
			Consistently(func(g Gomega) {
				g.Expect(webhookCalled).To(BeFalse())
			}, "2s").Should(Succeed())
		})
	})

	Describe("Webhook Authentication", func() {
		var testServer *httptest.Server

		AfterEach(func() {
			if testServer != nil {
				testServer.Close()
			}
		})

		It("should use basic auth from secret", func() {
			var receivedAuthHeader string
			webhookCalled := false

			// Setup mock webhook server that captures auth header
			testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedAuthHeader = r.Header.Get("Authorization")
				webhookCalled = true
				w.WriteHeader(http.StatusOK)
			}))

			// Create auth secret
			authSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-auth-basic",
					Namespace: "default",
				},
				StringData: map[string]string{
					"username": "testuser",
					"password": "testpass",
				},
			}
			Expect(k8sClient.Create(ctx, authSecret)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, authSecret)
			}()

			// Create PromotionEventHook with basic auth
			peh := &promoterv1alpha1.PromotionEventHook{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-basic-auth",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.PromotionEventHookSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					TriggerExpr: `{trigger: true}`,
					Action: promoterv1alpha1.PromotionEventHookAction{
						Webhook: &promoterv1alpha1.WebhookAction{
							URL:    testServer.URL,
							Method: "POST",
							Auth: &promoterv1alpha1.WebhookAuth{
								Basic: &promoterv1alpha1.BasicAuth{
									SecretRef: &promoterv1alpha1.BasicAuthSecretRef{
										Name: name + "-auth-basic",
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, peh)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, peh)
			}()

			// Wait for webhook to be called with auth header
			Eventually(func(g Gomega) {
				g.Expect(webhookCalled).To(BeTrue())
				g.Expect(receivedAuthHeader).To(HavePrefix("Basic "))
			}, constants.EventuallyTimeout).Should(Succeed())
		})

		It("should use bearer token from secret", func() {
			var receivedAuthHeader string
			webhookCalled := false

			// Setup mock webhook server that captures auth header
			testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedAuthHeader = r.Header.Get("Authorization")
				webhookCalled = true
				w.WriteHeader(http.StatusOK)
			}))

			// Create auth secret
			authSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-auth-bearer",
					Namespace: "default",
				},
				StringData: map[string]string{
					"token": "my-secret-token",
				},
			}
			Expect(k8sClient.Create(ctx, authSecret)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, authSecret)
			}()

			// Create PromotionEventHook with bearer auth
			peh := &promoterv1alpha1.PromotionEventHook{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-bearer-auth",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.PromotionEventHookSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					TriggerExpr: `{trigger: true}`,
					Action: promoterv1alpha1.PromotionEventHookAction{
						Webhook: &promoterv1alpha1.WebhookAction{
							URL:    testServer.URL,
							Method: "POST",
							Auth: &promoterv1alpha1.WebhookAuth{
								Bearer: &promoterv1alpha1.BearerAuth{
									SecretRef: &promoterv1alpha1.BearerAuthSecretRef{
										Name: name + "-auth-bearer",
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, peh)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, peh)
			}()

			// Wait for webhook to be called with auth header
			Eventually(func(g Gomega) {
				g.Expect(webhookCalled).To(BeTrue())
				g.Expect(receivedAuthHeader).To(Equal("Bearer my-secret-token"))
			}, constants.EventuallyTimeout).Should(Succeed())
		})
	})

	Describe("Resource Creation", func() {
		It("should create a ConfigMap using resource template", func() {
			configMapName := name + "-config"

			// Create PromotionEventHook with resource action
			peh := &promoterv1alpha1.PromotionEventHook{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-resource",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.PromotionEventHookSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					TriggerExpr: `{trigger: true}`,
					Action: promoterv1alpha1.PromotionEventHookAction{
						Resource: &promoterv1alpha1.ResourceAction{
							Template: fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: default
data:
  strategy: "{{ .PromotionStrategy.Name }}"
`, configMapName),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, peh)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, peh)
				var cm corev1.ConfigMap
				if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: configMapName}, &cm); err == nil {
					_ = k8sClient.Delete(ctx, &cm)
				}
			}()

			// Wait for ConfigMap to be created
			Eventually(func(g Gomega) {
				var cm corev1.ConfigMap
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: configMapName}, &cm)).To(Succeed())
				g.Expect(cm.Data["strategy"]).To(Equal(name))
			}, constants.EventuallyTimeout).Should(Succeed())

			// Verify status was updated
			Eventually(func(g Gomega) {
				var fetchedPEH promoterv1alpha1.PromotionEventHook
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(peh), &fetchedPEH)).To(Succeed())
				g.Expect(fetchedPEH.Status.ResourceStatus).NotTo(BeNil())
				g.Expect(fetchedPEH.Status.ResourceStatus.Success).To(BeTrue())
				g.Expect(fetchedPEH.Status.ResourceStatus.ResourceRef).NotTo(BeNil())
				g.Expect(fetchedPEH.Status.ResourceStatus.ResourceRef.Kind).To(Equal("ConfigMap"))
				g.Expect(fetchedPEH.Status.ResourceStatus.ResourceRef.Name).To(Equal(configMapName))
			}, constants.EventuallyTimeout).Should(Succeed())
		})

		It("should use webhook response data in resource template via webhookResponseExpr", func() {
			webhookCallCount := 0

			// Setup mock webhook server that returns JSON data
			testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				webhookCallCount++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"deploymentId": "deploy-12345", "status": "success", "region": "us-west-2"}`))
			}))
			defer testServer.Close()

			configMapName := name + "-webhook-data"

			// Create PromotionEventHook with webhook + webhookResponseExpr + resource
			peh := &promoterv1alpha1.PromotionEventHook{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-webhook-resource",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.PromotionEventHookSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					TriggerExpr: `{trigger: true}`,
					Action: promoterv1alpha1.PromotionEventHookAction{
						Webhook: &promoterv1alpha1.WebhookAction{
							URL:    testServer.URL,
							Method: "POST",
							Body:   `{"app": "{{ .PromotionStrategy.Name }}"}`,
							// Transform webhook response data for use in templates
							ResponseExpr: `{
								deploymentId: webhookResponse.data.deploymentId,
								status: webhookResponse.data.status,
								region: webhookResponse.data.region
							}`,
						},
						Resource: &promoterv1alpha1.ResourceAction{
							Template: fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: default
data:
  deploymentId: "{{ .WebhookResponseData.deploymentId }}"
  status: "{{ .WebhookResponseData.status }}"
  region: "{{ .WebhookResponseData.region }}"
  strategy: "{{ .PromotionStrategy.Name }}"
`, configMapName),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, peh)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, peh)
				var cm corev1.ConfigMap
				if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: configMapName}, &cm); err == nil {
					_ = k8sClient.Delete(ctx, &cm)
				}
			}()

			// Wait for webhook to be called
			Eventually(func(g Gomega) {
				g.Expect(webhookCallCount).To(Equal(1))
			}, constants.EventuallyTimeout).Should(Succeed())

			// Wait for ConfigMap to be created with data from webhook response
			Eventually(func(g Gomega) {
				var cm corev1.ConfigMap
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: configMapName}, &cm)).To(Succeed())
				g.Expect(cm.Data["deploymentId"]).To(Equal("deploy-12345"))
				g.Expect(cm.Data["status"]).To(Equal("success"))
				g.Expect(cm.Data["region"]).To(Equal("us-west-2"))
				g.Expect(cm.Data["strategy"]).To(Equal(name))
			}, constants.EventuallyTimeout).Should(Succeed())

			// Verify webhookResponseData was stored in status
			Eventually(func(g Gomega) {
				var fetchedPEH promoterv1alpha1.PromotionEventHook
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(peh), &fetchedPEH)).To(Succeed())
				g.Expect(fetchedPEH.Status.WebhookResponseData).To(HaveKeyWithValue("deploymentId", "deploy-12345"))
				g.Expect(fetchedPEH.Status.WebhookResponseData).To(HaveKeyWithValue("status", "success"))
				g.Expect(fetchedPEH.Status.WebhookResponseData).To(HaveKeyWithValue("region", "us-west-2"))
			}, constants.EventuallyTimeout).Should(Succeed())
		})

		It("should enforce namespace restriction for resources", func() {
			configMapName := name + "-wrong-namespace"

			// Create PromotionEventHook that tries to create resource in a different namespace
			peh := &promoterv1alpha1.PromotionEventHook{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-wrong-ns",
					Namespace: "default", // Hook is in default namespace
				},
				Spec: promoterv1alpha1.PromotionEventHookSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					TriggerExpr: `{trigger: true}`,
					Action: promoterv1alpha1.PromotionEventHookAction{
						Resource: &promoterv1alpha1.ResourceAction{
							Template: fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: kube-system
data:
  test: "data"
`, configMapName),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, peh)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, peh)
			}()

			// Wait for status to show failure due to namespace mismatch
			Eventually(func(g Gomega) {
				var fetchedPEH promoterv1alpha1.PromotionEventHook
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(peh), &fetchedPEH)).To(Succeed())
				g.Expect(fetchedPEH.Status.ResourceStatus).NotTo(BeNil())
				g.Expect(fetchedPEH.Status.ResourceStatus.Success).To(BeFalse())
				g.Expect(fetchedPEH.Status.ResourceStatus.Error).To(ContainSubstring("does not match hook namespace"))
			}, constants.EventuallyTimeout).Should(Succeed())

			// Verify ConfigMap was NOT created in kube-system
			var cm corev1.ConfigMap
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: configMapName}, &cm)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Describe("Fire Once Pattern", func() {
		It("should only fire once based on triggerData state", func() {
			webhookCallCount := 0

			// Setup mock webhook server that counts calls
			testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				webhookCallCount++
				w.WriteHeader(http.StatusOK)
			}))
			defer testServer.Close()

			// Create PromotionEventHook with fire-once pattern
			// This trigger only fires when the strategy name differs from the stored lastStrategy
			peh := &promoterv1alpha1.PromotionEventHook{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-fire-once",
					Namespace: "default",
				},
				Spec: promoterv1alpha1.PromotionEventHookSpec{
					PromotionStrategyRef: promoterv1alpha1.ObjectReference{
						Name: name,
					},
					TriggerExpr: `{
						trigger: promotionStrategy.Name != status.TriggerData["lastStrategy"],
						lastStrategy: promotionStrategy.Name
					}`,
					Action: promoterv1alpha1.PromotionEventHookAction{
						Webhook: &promoterv1alpha1.WebhookAction{
							URL:    testServer.URL,
							Method: "POST",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, peh)).To(Succeed())

			// Cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, peh)
			}()

			// Wait for first webhook call
			Eventually(func(g Gomega) {
				g.Expect(webhookCallCount).To(Equal(1))
			}, constants.EventuallyTimeout).Should(Succeed())

			// Wait for status to be updated with lastStrategy
			Eventually(func(g Gomega) {
				var fetchedPEH promoterv1alpha1.PromotionEventHook
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(peh), &fetchedPEH)).To(Succeed())
				g.Expect(fetchedPEH.Status.TriggerData).To(HaveKeyWithValue("lastStrategy", name))
			}, constants.EventuallyTimeout).Should(Succeed())

			// Verify webhook is NOT called again (fire-once pattern)
			Consistently(func(g Gomega) {
				g.Expect(webhookCallCount).To(Equal(1))
			}, "3s").Should(Succeed())
		})
	})
})
