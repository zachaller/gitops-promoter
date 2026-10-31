/*
Copyright 2026.

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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/resourcetemplate"
	"github.com/argoproj-labs/gitops-promoter/internal/types/constants"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

const krcsTestKey = "k8s-resource-test"

// configMapTemplateSource mirrors the entry controllerConfigurationForTests adds for these specs.
var configMapTemplateSource = promoterv1alpha1.ResourceTemplateSource{Version: "v1", Kind: "ConfigMap"}

// newConfigMapTemplate returns a ConfigMapTemplate whose spec.template renders branch and SHA into data and
// starts with data.result "pending", which specs flip to "ok" to drive the phase expression.
func newConfigMapTemplate(name, namespace string) *unstructured.Unstructured {
	tmpl := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name": "ignored-by-the-controller",
					"labels": map[string]any{
						"example.com/branch": `{{ .Branch | replace "/" "-" }}`,
					},
				},
				"data": map[string]any{
					"branch": "{{ .Branch }}",
					"sha":    "{{ .Sha }}",
					"short":  "{{ .ShortSha }}",
					"dry":    "{{ .Environment.Proposed.Dry.Sha }}",
					"result": "pending",
				},
			},
		},
	}}
	tmpl.SetGroupVersionKind(resourcetemplate.TemplateGVK(configMapTemplateSource))
	tmpl.SetName(name)
	tmpl.SetNamespace(namespace)
	return tmpl
}

// touchKRCS bumps the gate's generation so the controller reconciles it now instead of at the next requeue
// (after a terminal phase the resource is not polled any more).
func touchKRCS(ctx context.Context, krcs *promoterv1alpha1.KubernetesResourceCommitStatus) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		var current promoterv1alpha1.KubernetesResourceCommitStatus
		g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(krcs), &current)).To(Succeed())
		current.Spec.PollInterval = metav1.Duration{Duration: current.Spec.PollInterval.Duration + time.Second}
		g.Expect(k8sClient.Update(ctx, &current)).To(Succeed())
	}, constants.EventuallyTimeout).Should(Succeed())
}

// configMapPhaseExpression succeeds once data.result is "ok" and fails when it is "failed".
const configMapPhaseExpression = `Resource == nil ? 'pending' : (Resource.data?.result ?? '') == 'ok' ? 'success' : (Resource.data?.result ?? '') == 'failed' ? 'failure' : 'pending'`

var _ = Describe("KubernetesResourceCommitStatus Controller", Ordered, func() {
	var (
		ctx               context.Context
		name              string
		scmSecret         *v1.Secret
		scmProvider       *promoterv1alpha1.ScmProvider
		gitRepo           *promoterv1alpha1.GitRepository
		promotionStrategy *promoterv1alpha1.PromotionStrategy
		template          *unstructured.Unstructured
	)

	BeforeAll(func() {
		ctx = context.Background()

		By("Verifying the ConfigMapTemplate CRD was generated at controller setup")
		var crd apiextensionsv1.CustomResourceDefinition
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: resourcetemplate.TemplateCRDName(configMapTemplateSource)}, &crd)).To(Succeed())
		Expect(crd.Labels).To(HaveKeyWithValue(promoterv1alpha1.GeneratedTemplateLabel, "true"))
		Expect(crd.Annotations).To(HaveKeyWithValue(promoterv1alpha1.TemplateSourceKindAnnotation, "ConfigMap"))
		Expect(crd.Annotations).To(HaveKeyWithValue(promoterv1alpha1.TemplateSchemaSourceAnnotation, resourcetemplate.SchemaSourceOpenAPI),
			"envtest serves OpenAPI v3 for core/v1, so the ConfigMap schema is derived from it")
		tmplSchema := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["template"]
		Expect(tmplSchema.Properties).To(HaveKey("data"))
		Expect(tmplSchema.Properties["data"].AdditionalProperties).NotTo(BeNil())

		By("Setting up test git repository and resources")
		name, scmSecret, scmProvider, gitRepo, _, _, promotionStrategy = promotionStrategyResource(ctx, "k8s-resource-test", "default")
		// Gate only the development environment so every spec deals with exactly one resource and CommitStatus.
		for i := range promotionStrategy.Spec.Environments {
			if promotionStrategy.Spec.Environments[i].Branch == testBranchDevelopment {
				promotionStrategy.Spec.Environments[i].ProposedCommitStatuses = []promoterv1alpha1.CommitStatusSelector{{Key: krcsTestKey}}
			}
		}
		// PS hard-fails without an ordering gate; declare and create PECS like other gate tests.
		declareDependentsSuccessfulGate(promotionStrategy)

		setupInitialTestGitRepoOnServer(ctx, gitRepo)

		Expect(k8sClient.Create(ctx, scmSecret)).To(Succeed())
		Expect(k8sClient.Create(ctx, scmProvider)).To(Succeed())
		Expect(k8sClient.Create(ctx, gitRepo)).To(Succeed())
		Expect(k8sClient.Create(ctx, promotionStrategy)).To(Succeed())
		createDependentsSuccessfulCommitStatus(ctx, promotionStrategy)

		template = newConfigMapTemplate(name+"-template", "default")
		Expect(k8sClient.Create(ctx, template)).To(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		if template != nil {
			_ = k8sClient.Delete(ctx, template)
		}
		if promotionStrategy != nil {
			_ = k8sClient.Delete(ctx, &promoterv1alpha1.DependentsSuccessfulCommitStatus{
				Name: promotionStrategy.Name, Namespace: promotionStrategy.Namespace,
			})
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

	newKRCS := func(suffix string) *promoterv1alpha1.KubernetesResourceCommitStatus {
		return &promoterv1alpha1.KubernetesResourceCommitStatus{
			Name:      name + suffix,
			Namespace: "default",
			Spec: promoterv1alpha1.KubernetesResourceCommitStatusSpec{
				Key:                  krcsTestKey,
				PromotionStrategyRef: promoterv1alpha1.ObjectReference{Name: name},
				TemplateRef:          promoterv1alpha1.ResourceTemplateReference{Kind: "ConfigMapTemplate", Name: template.GetName()},
				Phase:                promoterv1alpha1.KubernetesResourcePhaseSpec{Expression: configMapPhaseExpression},
				DescriptionTemplate:  "Check for {{ .Branch }} is {{ .Phase }}",
				UrlTemplate:          "https://example.com/{{ .Namespace }}/{{ .ResourceName }}",
				PollInterval:         metav1.Duration{Duration: 2 * time.Second},
			},
		}
	}

	Describe("Resource lifecycle", func() {
		var krcs *promoterv1alpha1.KubernetesResourceCommitStatus

		BeforeEach(func() {
			krcs = newKRCS("-lifecycle")
			Expect(k8sClient.Create(ctx, krcs)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, krcs)
		})

		It("creates the resource from the template, reports pending, then follows the resource to success", func() {
			var created v1.ConfigMap
			var reportedSha string

			By("creating a ConfigMap for the proposed SHA and reporting pending")
			Eventually(func(g Gomega) {
				var current promoterv1alpha1.KubernetesResourceCommitStatus
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(krcs), &current)).To(Succeed())
				g.Expect(current.Status.Environments).To(HaveLen(1))
				env := current.Status.Environments[0]
				g.Expect(env.Branch).To(Equal(testBranchDevelopment))
				g.Expect(env.Phase).To(Equal(promoterv1alpha1.CommitPhasePending))
				g.Expect(env.ReportedSha).NotTo(BeEmpty())
				g.Expect(env.ResourceExists).To(BeTrue())
				g.Expect(env.Resource).NotTo(BeNil())
				g.Expect(env.Resource.Kind).To(Equal("ConfigMap"))
				g.Expect(env.Resource.APIVersion).To(Equal("v1"))
				g.Expect(env.Resource.Namespace).To(Equal("default"))
				g.Expect(env.Resource.Name).To(Equal(kubernetesResourceName(&current, testBranchDevelopment, env.ReportedSha)))
				g.Expect(env.Description).To(Equal("Check for " + testBranchDevelopment + " is pending"))
				g.Expect(env.Url).To(Equal("https://example.com/default/" + env.Resource.Name))
				g.Expect(env.LastEvaluationTime).NotTo(BeNil())
				reportedSha = env.ReportedSha

				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: env.Resource.Name, Namespace: "default"}, &created)).To(Succeed())
			}, constants.EventuallyTimeout).Should(Succeed())

			By("rendering template variables and stamping labels and the owner reference")
			Expect(created.Data).To(HaveKeyWithValue("branch", testBranchDevelopment))
			Expect(created.Data).To(HaveKeyWithValue("sha", reportedSha))
			Expect(created.Data).To(HaveKeyWithValue("short", reportedSha[:7]))
			Expect(created.Data).To(HaveKeyWithValue("result", "pending"))
			Expect(created.Data["dry"]).NotTo(BeEmpty())
			Expect(created.Labels).To(HaveKeyWithValue("example.com/branch", "environment-development"))
			Expect(created.Labels).To(HaveKeyWithValue(promoterv1alpha1.CommitStatusLabel, krcsTestKey))
			Expect(created.Labels).To(HaveKeyWithValue(promoterv1alpha1.EnvironmentLabel, utils.KubeSafeLabel(testBranchDevelopment)))
			Expect(created.Labels).To(HaveKeyWithValue(promoterv1alpha1.ReportedShaLabel, reportedSha))
			Expect(created.Labels).To(HaveKeyWithValue("promoter.argoproj.io/kubernetes-resource-commit-status", utils.KubeSafeLabel(krcs.Name)))
			Expect(created.OwnerReferences).To(HaveLen(1))
			Expect(created.OwnerReferences[0].Kind).To(Equal("KubernetesResourceCommitStatus"))
			Expect(created.OwnerReferences[0].Name).To(Equal(krcs.Name))
			Expect(created.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

			By("creating a pending CommitStatus with the rendered description and URL")
			commitStatusName := utils.CommitStatusResourceName(ctx, krcs, testBranchDevelopment)
			var cs promoterv1alpha1.CommitStatus
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: commitStatusName, Namespace: "default"}, &cs)).To(Succeed())
			Expect(cs.Spec.Phase).To(Equal(promoterv1alpha1.CommitPhasePending))
			Expect(cs.Spec.Sha).To(Equal(reportedSha))
			Expect(cs.Spec.Name).To(Equal(krcsTestKey + "/" + testBranchDevelopment))
			Expect(cs.Spec.Url).To(Equal("https://example.com/default/" + created.Name))
			Expect(cs.Labels).To(HaveKeyWithValue(promoterv1alpha1.CommitStatusLabel, krcsTestKey))

			By("emitting a KubernetesResourceCreated event")
			Eventually(func(g Gomega) {
				var eventList v1.EventList
				g.Expect(k8sClient.List(ctx, &eventList, ctrlclient.InNamespace("default"))).To(Succeed())
				expectEventEmitted(g, hasEventWithReason(eventList, krcs.Name, constants.KubernetesResourceCreatedReason))
			}, constants.EventuallyTimeout).Should(Succeed())

			By("flipping the live resource to ok and observing success")
			Eventually(func(g Gomega) {
				var cm v1.ConfigMap
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(&created), &cm)).To(Succeed())
				cm.Data["result"] = "ok"
				g.Expect(k8sClient.Update(ctx, &cm)).To(Succeed())
			}, constants.EventuallyTimeout).Should(Succeed())

			Eventually(func(g Gomega) {
				var current promoterv1alpha1.KubernetesResourceCommitStatus
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(krcs), &current)).To(Succeed())
				g.Expect(current.Status.Environments).To(HaveLen(1))
				g.Expect(current.Status.Environments[0].Phase).To(Equal(promoterv1alpha1.CommitPhaseSuccess))
				g.Expect(current.Status.Environments[0].Description).To(Equal("Check for " + testBranchDevelopment + " is success"))

				var cs promoterv1alpha1.CommitStatus
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: commitStatusName, Namespace: "default"}, &cs)).To(Succeed())
				g.Expect(cs.Spec.Phase).To(Equal(promoterv1alpha1.CommitPhaseSuccess))

				ready := meta.FindStatusCondition(current.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			}, constants.EventuallyTimeout).Should(Succeed())

			By("emitting a CommitStatusPhaseChanged event for the transition")
			Eventually(func(g Gomega) {
				var eventList v1.EventList
				g.Expect(k8sClient.List(ctx, &eventList, ctrlclient.InNamespace("default"))).To(Succeed())
				expectEventEmitted(g, hasEventWithReasonAndMessage(eventList, krcs.Name, constants.CommitStatusPhaseChangedReason, "changed from pending to success"))
			}, constants.EventuallyTimeout).Should(Succeed())

			By("keeping the success report when the resource of a succeeded SHA is deleted")
			Expect(k8sClient.Delete(ctx, &created)).To(Succeed())
			touchKRCS(ctx, krcs)
			Eventually(func(g Gomega) {
				var current promoterv1alpha1.KubernetesResourceCommitStatus
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(krcs), &current)).To(Succeed())
				g.Expect(current.Status.ObservedGeneration).To(Equal(current.Generation))
				g.Expect(current.Status.Environments).To(HaveLen(1))
				g.Expect(current.Status.Environments[0].ResourceExists).To(BeFalse())
				g.Expect(current.Status.Environments[0].Phase).To(Equal(promoterv1alpha1.CommitPhaseSuccess))
			}, constants.EventuallyTimeout).Should(Succeed())
			var cm v1.ConfigMap
			err := k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(&created), &cm)
			Expect(ctrlclient.IgnoreNotFound(err)).To(Succeed())
			Expect(err).To(HaveOccurred(), "a succeeded resource must not be recreated")
		})

		It("reports failure from the expression and deletes resources of superseded SHAs", func() {
			var current promoterv1alpha1.KubernetesResourceCommitStatus
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(krcs), &current)).To(Succeed())
				g.Expect(current.Status.Environments).To(HaveLen(1))
				g.Expect(current.Status.Environments[0].Resource).NotTo(BeNil())
			}, constants.EventuallyTimeout).Should(Succeed())
			env := current.Status.Environments[0]

			By("planting a ConfigMap labeled for an older SHA of the same environment")
			stale := &v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      krcs.Name + "-stale",
					Namespace: "default",
					Labels: map[string]string{
						utils.CommitStatusGateLabelKeyForParent(&current): utils.KubeSafeLabel(current.Name),
						promoterv1alpha1.EnvironmentLabel:                 utils.KubeSafeLabel(testBranchDevelopment),
						promoterv1alpha1.CommitStatusLabel:                krcsTestKey,
						promoterv1alpha1.ReportedShaLabel:                 "0000000000000000000000000000000000000000",
					},
					OwnerReferences: []metav1.OwnerReference{
						*metav1.NewControllerRef(&current, promoterv1alpha1.GroupVersion.WithKind("KubernetesResourceCommitStatus")),
					},
				},
			}
			Expect(k8sClient.Create(ctx, stale)).To(Succeed())
			// A resource with the parent label but a different controller must be left alone.
			foreign := stale.DeepCopy()
			foreign.Name = krcs.Name + "-foreign"
			foreign.ResourceVersion = ""
			foreign.OwnerReferences = nil
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, foreign) })

			By("marking the live resource failed so the gate reconciles and reports failure")
			Eventually(func(g Gomega) {
				var cm v1.ConfigMap
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: env.Resource.Name, Namespace: "default"}, &cm)).To(Succeed())
				cm.Data["result"] = "failed"
				g.Expect(k8sClient.Update(ctx, &cm)).To(Succeed())
			}, constants.EventuallyTimeout).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(krcs), &current)).To(Succeed())
				g.Expect(current.Status.Environments).To(HaveLen(1))
				g.Expect(current.Status.Environments[0].Phase).To(Equal(promoterv1alpha1.CommitPhaseFailure))

				var cm v1.ConfigMap
				err := k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(stale), &cm)
				g.Expect(ctrlclient.IgnoreNotFound(err)).To(Succeed())
				g.Expect(err).To(HaveOccurred(), "superseded ConfigMap should be deleted")
			}, constants.EventuallyTimeout).Should(Succeed())

			var kept v1.ConfigMap
			Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(foreign), &kept)).To(Succeed(), "resources not controlled by the gate are kept")

			Eventually(func(g Gomega) {
				var eventList v1.EventList
				g.Expect(k8sClient.List(ctx, &eventList, ctrlclient.InNamespace("default"))).To(Succeed())
				expectEventEmitted(g, hasEventWithReason(eventList, krcs.Name, constants.KubernetesResourceDeletedReason))
			}, constants.EventuallyTimeout).Should(Succeed())

			By("recreating the resource of a failed SHA when it is deleted, so the check re-runs")
			var failed v1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: env.Resource.Name, Namespace: "default"}, &failed)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &failed)).To(Succeed())
			touchKRCS(ctx, krcs)
			Eventually(func(g Gomega) {
				var cm v1.ConfigMap
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(&failed), &cm)).To(Succeed())
				g.Expect(cm.UID).NotTo(Equal(failed.UID))
				g.Expect(cm.Data).To(HaveKeyWithValue("result", "pending"))

				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(krcs), &current)).To(Succeed())
				g.Expect(current.Status.Environments).To(HaveLen(1))
				g.Expect(current.Status.Environments[0].Phase).To(Equal(promoterv1alpha1.CommitPhasePending))
				g.Expect(current.Status.Environments[0].ResourceExists).To(BeTrue())
			}, constants.EventuallyTimeout).Should(Succeed())
		})
	})

	Describe("historyLimit", func() {
		var krcs *promoterv1alpha1.KubernetesResourceCommitStatus

		BeforeEach(func() {
			krcs = newKRCS("-history")
			krcs.Spec.HistoryLimit = ptr.To[int32](1)
			Expect(k8sClient.Create(ctx, krcs)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, krcs)
		})

		It("keeps the newest superseded resource per environment", func() {
			var current promoterv1alpha1.KubernetesResourceCommitStatus
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(krcs), &current)).To(Succeed())
				g.Expect(current.Status.Environments).To(HaveLen(1))
			}, constants.EventuallyTimeout).Should(Succeed())

			makeStale := func(suffix, sha string) *v1.ConfigMap {
				cm := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name: krcs.Name + suffix, Namespace: "default",
					Labels: map[string]string{
						utils.CommitStatusGateLabelKeyForParent(&current): utils.KubeSafeLabel(current.Name),
						promoterv1alpha1.EnvironmentLabel:                 utils.KubeSafeLabel(testBranchDevelopment),
						promoterv1alpha1.ReportedShaLabel:                 sha,
					},
					OwnerReferences: []metav1.OwnerReference{
						*metav1.NewControllerRef(&current, promoterv1alpha1.GroupVersion.WithKind("KubernetesResourceCommitStatus")),
					},
				}}
				Expect(k8sClient.Create(ctx, cm)).To(Succeed())
				return cm
			}
			older := makeStale("-older", "1111111111111111111111111111111111111111")
			// creationTimestamp has second granularity; make the ordering unambiguous.
			time.Sleep(1100 * time.Millisecond)
			newer := makeStale("-newer", "2222222222222222222222222222222222222222")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, newer) })

			By("forcing a reconcile by changing the live resource")
			Eventually(func(g Gomega) {
				var cm v1.ConfigMap
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: current.Status.Environments[0].Resource.Name, Namespace: "default"}, &cm)).To(Succeed())
				cm.Data["result"] = "ok"
				g.Expect(k8sClient.Update(ctx, &cm)).To(Succeed())
			}, constants.EventuallyTimeout).Should(Succeed())

			Eventually(func(g Gomega) {
				var cm v1.ConfigMap
				err := k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(older), &cm)
				g.Expect(ctrlclient.IgnoreNotFound(err)).To(Succeed())
				g.Expect(err).To(HaveOccurred(), "oldest superseded ConfigMap beyond historyLimit should be deleted")
			}, constants.EventuallyTimeout).Should(Succeed())
			Consistently(func(g Gomega) {
				var cm v1.ConfigMap
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(newer), &cm)).To(Succeed())
			}, 3*time.Second).Should(Succeed(), "newest superseded ConfigMap is kept by historyLimit: 1")
		})
	})

	Describe("publishTo: empty", func() {
		var krcs *promoterv1alpha1.KubernetesResourceCommitStatus

		BeforeEach(func() {
			krcs = newKRCS("-skip-scm")
			krcs.Spec.PublishTo = &[]promoterv1alpha1.CommitStatusPublishTo{}
			Expect(k8sClient.Create(ctx, krcs)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, krcs)
		})

		It("sets publishTo on the child CommitStatus", func() {
			Eventually(func(g Gomega) {
				var cs promoterv1alpha1.CommitStatus
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: utils.CommitStatusResourceName(ctx, krcs, testBranchDevelopment), Namespace: "default"}, &cs)).To(Succeed())
				g.Expect(cs.Spec.PublishTo).To(HaveValue(BeEmpty()))
			}, constants.EventuallyTimeout).Should(Succeed())
		})
	})

	Describe("configuration errors", func() {
		var krcs *promoterv1alpha1.KubernetesResourceCommitStatus

		AfterEach(func() {
			if krcs != nil {
				_ = k8sClient.Delete(ctx, krcs)
			}
		})

		expectReadyFalseContaining := func(substring string) {
			Eventually(func(g Gomega) {
				var current promoterv1alpha1.KubernetesResourceCommitStatus
				g.Expect(k8sClient.Get(ctx, ctrlclient.ObjectKeyFromObject(krcs), &current)).To(Succeed())
				ready := meta.FindStatusCondition(current.Status.Conditions, "Ready")
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(ready.Message).To(ContainSubstring(substring))
			}, constants.EventuallyTimeout).Should(Succeed())
		}

		It("sets Ready=False when the template kind is not configured", func() {
			krcs = newKRCS("-unknown-kind")
			krcs.Spec.TemplateRef.Kind = "SecretTemplate"
			Expect(k8sClient.Create(ctx, krcs)).To(Succeed())
			expectReadyFalseContaining(`template kind "SecretTemplate" is not configured`)
		})

		It("sets Ready=False when the template does not exist", func() {
			krcs = newKRCS("-missing-template")
			krcs.Spec.TemplateRef.Name = "does-not-exist"
			Expect(k8sClient.Create(ctx, krcs)).To(Succeed())
			expectReadyFalseContaining(`template ConfigMapTemplate "does-not-exist" not found`)
		})

		It("sets Ready=False when the phase expression is invalid", func() {
			krcs = newKRCS("-bad-expression")
			krcs.Spec.Phase.Expression = "'not-a-phase'"
			Expect(k8sClient.Create(ctx, krcs)).To(Succeed())
			expectReadyFalseContaining(`unknown phase "not-a-phase"`)
		})

		It("sets Ready=False when the PromotionStrategy does not exist", func() {
			krcs = newKRCS("-missing-ps")
			krcs.Spec.PromotionStrategyRef.Name = "nonexistent-ps"
			Expect(k8sClient.Create(ctx, krcs)).To(Succeed())
			expectReadyFalseContaining("not found")
		})
	})
})

var _ = Describe("KubernetesResourceCommitStatus - YAML testdata", func() {
	It("should deserialize the example manifest", func() {
		data, err := os.ReadFile("testdata/KubernetesResourceCommitStatus.yaml")
		Expect(err).NotTo(HaveOccurred())

		var krcs promoterv1alpha1.KubernetesResourceCommitStatus
		Expect(unmarshalYamlStrict(string(data), &krcs)).To(Succeed())

		Expect(krcs.Spec.Key).To(Equal("smoke-test"))
		Expect(krcs.Spec.PromotionStrategyRef.Name).To(Equal("example-promotion-strategy"))
		Expect(krcs.Spec.TemplateRef).To(Equal(promoterv1alpha1.ResourceTemplateReference{Kind: "JobTemplate", Name: "smoke-test"}))
		Expect(krcs.Spec.Phase.Expression).To(ContainSubstring("Resource.status?.succeeded"))
		Expect(krcs.Spec.HistoryLimit).To(HaveValue(Equal(int32(1))))
		Expect(krcs.Status.Environments).To(HaveLen(2))
		Expect(krcs.Status.Environments[0].Resource.Kind).To(Equal("Job"))
	})
})

var _ = Describe("defaultKubernetesResourceDescription", func() {
	It("describes each phase in the present tense", func() {
		Expect(defaultKubernetesResourceDescription("Job", "smoke", promoterv1alpha1.CommitPhasePending)).To(Equal("Waiting for Job smoke"))
		Expect(defaultKubernetesResourceDescription("Job", "smoke", promoterv1alpha1.CommitPhaseSuccess)).To(Equal("Job smoke is successful"))
		Expect(defaultKubernetesResourceDescription("Job", "smoke", promoterv1alpha1.CommitPhaseFailure)).To(Equal("Job smoke is failing"))
	})
})
