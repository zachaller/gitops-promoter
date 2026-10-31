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

package resourcetemplate_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/resourcetemplate"
)

const testSha = "abc1234567890abc1234567890abc1234567890a"

func testTemplateData() resourcetemplate.TemplateData {
	krcs := &promoterv1alpha1.KubernetesResourceCommitStatus{
		Name: "smoke", Namespace: "default",
		Spec: promoterv1alpha1.KubernetesResourceCommitStatusSpec{Key: "smoke-test", ReportOn: "proposed"},
	}
	ps := &promoterv1alpha1.PromotionStrategy{Name: "my-app", Namespace: "default"}
	env := &promoterv1alpha1.EnvironmentStatus{Branch: "environment/development"}
	env.Proposed.Dry.Sha = "dry0000000000000000000000000000000000000"
	return resourcetemplate.NewTemplateData(krcs, ps, env, testSha, "smoke-environment-development-abc1234-1234abcd")
}

func jobTemplate(template map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "templates.promoter.argoproj.io/v1alpha1",
		"kind":       "JobTemplate",
		"metadata":   map[string]any{"name": "smoke", "namespace": "default"},
		"spec":       map[string]any{"template": template},
	}}
}

var _ = Describe("RenderTemplate", func() {
	jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

	It("renders string leaves, keeps other scalars, and fixes identity fields", func() {
		data := testTemplateData()
		tmpl := jobTemplate(map[string]any{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata": map[string]any{
				"name":            "ignored",
				"namespace":       "ignored-too",
				"ownerReferences": []any{map[string]any{"name": "someone"}},
				"labels":          map[string]any{"branch": "{{ .Branch | replace \"/\" \"-\" }}"},
			},
			"spec": map[string]any{
				"backoffLimit": int64(2),
				"suspend":      false,
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{map[string]any{
							"name":  "test",
							"image": "example/test:{{ .ShortSha }}",
							"args":  []any{"--sha={{ .Sha }}", "--dry={{ .Environment.Proposed.Dry.Sha }}", "--name={{ .ResourceName }}", "--key={{ .Key }}", "--ps={{ .PromotionStrategy.Name }}"},
						}},
					},
				},
			},
			"status": map[string]any{"succeeded": int64(1)},
		})

		out, err := resourcetemplate.RenderTemplate(tmpl, jobGVK, data)
		Expect(err).NotTo(HaveOccurred())
		Expect(out.GetName()).To(Equal(data.ResourceName))
		Expect(out.GetNamespace()).To(Equal("default"))
		Expect(out.GetOwnerReferences()).To(BeEmpty())
		Expect(out.GetLabels()).To(HaveKeyWithValue("branch", "environment-development"))
		Expect(out.Object).NotTo(HaveKey("status"))

		backoff, _, _ := unstructured.NestedInt64(out.Object, "spec", "backoffLimit")
		Expect(backoff).To(Equal(int64(2)))
		suspend, _, _ := unstructured.NestedBool(out.Object, "spec", "suspend")
		Expect(suspend).To(BeFalse())
		containers, _, _ := unstructured.NestedSlice(out.Object, "spec", "template", "spec", "containers")
		Expect(containers).To(HaveLen(1))
		container, _ := containers[0].(map[string]any)
		Expect(container["image"]).To(Equal("example/test:abc1234"))
		Expect(container["args"]).To(Equal([]any{
			"--sha=" + testSha,
			"--dry=dry0000000000000000000000000000000000000",
			"--name=" + data.ResourceName,
			"--key=smoke-test",
			"--ps=my-app",
		}))

		By("not mutating the template")
		name, _, _ := unstructured.NestedString(tmpl.Object, "spec", "template", "metadata", "name")
		Expect(name).To(Equal("ignored"))
	})

	It("rejects a template whose apiVersion/kind do not match the source", func() {
		_, err := resourcetemplate.RenderTemplate(jobTemplate(map[string]any{"apiVersion": "batch/v1", "kind": "CronJob"}), jobGVK, testTemplateData())
		Expect(err).To(MatchError(ContainSubstring("spec.template is batch/v1, Kind=CronJob, want batch/v1, Kind=Job")))
	})

	It("rejects a template without spec.template", func() {
		tmpl := jobTemplate(nil)
		unstructured.RemoveNestedField(tmpl.Object, "spec", "template")
		_, err := resourcetemplate.RenderTemplate(tmpl, jobGVK, testTemplateData())
		Expect(err).To(MatchError(resourcetemplate.ErrTemplateMissing))
	})

	It("reports the path of a template that fails to render", func() {
		tmpl := jobTemplate(map[string]any{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"spec":       map[string]any{"args": []any{"ok", "{{ .Missing.Field }}"}},
		})
		_, err := resourcetemplate.RenderTemplate(tmpl, jobGVK, testTemplateData())
		Expect(err).To(MatchError(ContainSubstring("render spec.template.spec.args[1]")))
	})
})

var _ = Describe("RenderString", func() {
	It("renders with phase and resource and returns empty for an empty template", func() {
		data := testTemplateData()
		data.Phase = "success"
		data.Resource = map[string]any{"status": map[string]any{"succeeded": int64(1)}}
		out, err := resourcetemplate.RenderString("{{ .Branch }} is {{ .Phase }} ({{ .Resource.status.succeeded }})", data)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("environment/development is success (1)"))

		out, err = resourcetemplate.RenderString("", data)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(BeEmpty())

		_, err = resourcetemplate.RenderString("{{ .Branch", data)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("EvaluatePhase", func() {
	var evaluator *resourcetemplate.PhaseEvaluator

	BeforeEach(func() {
		evaluator = resourcetemplate.NewPhaseEvaluator()
	})

	input := func(resource map[string]any) resourcetemplate.PhaseInput {
		return resourcetemplate.PhaseInput{
			Resource: resource,
			Branch:   "environment/development",
			Sha:      testSha,
			Phase:    "pending",
		}
	}

	DescribeTable("maps expression results to phases",
		func(expression string, resource map[string]any, want promoterv1alpha1.CommitStatusPhase) {
			got, err := evaluator.EvaluatePhase(expression, input(resource))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("bool true", "true", nil, promoterv1alpha1.CommitPhaseSuccess),
		Entry("bool false", "false", nil, promoterv1alpha1.CommitPhasePending),
		Entry("string", "'failure'", nil, promoterv1alpha1.CommitPhaseFailure),
		Entry("object", "{phase: 'success'}", nil, promoterv1alpha1.CommitPhaseSuccess),
		Entry("object without phase", "{}", nil, promoterv1alpha1.CommitPhasePending),
		Entry("nil resource", "Resource == nil ? 'pending' : 'success'", nil, promoterv1alpha1.CommitPhasePending),
		Entry("job succeeded", "Resource == nil ? 'pending' : (Resource.status?.succeeded ?? 0) > 0 ? 'success' : (Resource.status?.failed ?? 0) > 0 ? 'failure' : 'pending'",
			map[string]any{"status": map[string]any{"succeeded": int64(1)}}, promoterv1alpha1.CommitPhaseSuccess),
		Entry("job failed", "Resource == nil ? 'pending' : (Resource.status?.succeeded ?? 0) > 0 ? 'success' : (Resource.status?.failed ?? 0) > 0 ? 'failure' : 'pending'",
			map[string]any{"status": map[string]any{"failed": int64(3)}}, promoterv1alpha1.CommitPhaseFailure),
		Entry("job running", "Resource == nil ? 'pending' : (Resource.status?.succeeded ?? 0) > 0 ? 'success' : (Resource.status?.failed ?? 0) > 0 ? 'failure' : 'pending'",
			map[string]any{"status": map[string]any{"active": int64(1)}}, promoterv1alpha1.CommitPhasePending),
		Entry("ready condition", "any(Resource?.status?.conditions ?? [], {.type == 'Ready' && .status == 'True'})",
			map[string]any{"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}, promoterv1alpha1.CommitPhaseSuccess),
		Entry("previous phase and sha", "Phase == 'pending' && Sha == '"+testSha+"' && Branch == 'environment/development'", nil, promoterv1alpha1.CommitPhaseSuccess),
	)

	It("rejects unknown phases, wrong types, and bad expressions", func() {
		_, err := evaluator.EvaluatePhase("'done'", input(nil))
		Expect(err).To(MatchError(ContainSubstring(`unknown phase "done"`)))
		_, err = evaluator.EvaluatePhase("42", input(nil))
		Expect(err).To(MatchError(ContainSubstring("must return bool, string, or object")))
		_, err = evaluator.EvaluatePhase("{phase: 1}", input(nil))
		Expect(err).To(MatchError(ContainSubstring("field phase must be a string")))
		_, err = evaluator.EvaluatePhase("Resource.", input(nil))
		Expect(err).To(MatchError(ContainSubstring("compile phase expression")))
		_, err = evaluator.EvaluatePhase("Resource.status.succeeded > 0", input(nil))
		Expect(err).To(MatchError(ContainSubstring("evaluate phase expression")), "nil resource without optional chaining fails at runtime")
	})
})
