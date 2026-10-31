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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/resourcetemplate"
)

// openAPIDoc mimics the shape of /openapi/v3/apis/batch/v1: typed references wrapped in allOf, "default: {}"
// on structs, list-type extensions, an int-or-string union, a Quantity-style string-or-number union, a
// recursive type, and a RawExtension-style untyped object.
const openAPIDoc = `{
  "openapi": "3.0.0",
  "info": {"title": "Kubernetes", "version": "v1.37.0"},
  "components": {
    "schemas": {
      "io.k8s.api.batch.v1.Job": {
        "type": "object",
        "description": "Job represents the configuration of a single job.",
        "properties": {
          "apiVersion": {"type": "string", "description": "APIVersion defines the versioned schema"},
          "kind": {"type": "string"},
          "metadata": {"allOf": [{"$ref": "#/components/schemas/io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"}], "default": {}},
          "spec": {"allOf": [{"$ref": "#/components/schemas/io.k8s.api.batch.v1.JobSpec"}], "default": {}, "description": "Specification of the desired behavior of a job."},
          "status": {"allOf": [{"$ref": "#/components/schemas/io.k8s.api.batch.v1.JobStatus"}], "default": {}}
        },
        "x-kubernetes-group-version-kind": [{"group": "batch", "kind": "Job", "version": "v1"}]
      },
      "io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "labels": {"type": "object", "additionalProperties": {"type": "string", "default": ""}}
        }
      },
      "io.k8s.api.batch.v1.JobSpec": {
        "type": "object",
        "required": ["template"],
        "properties": {
          "backoffLimit": {"type": "integer", "format": "int32"},
          "completionMode": {"type": "string", "enum": ["Indexed", "NonIndexed"]},
          "template": {"allOf": [{"$ref": "#/components/schemas/io.k8s.api.core.v1.PodTemplateSpec"}], "default": {}},
          "ports": {
            "type": "array",
            "items": {"allOf": [{"$ref": "#/components/schemas/io.k8s.api.core.v1.ContainerPort"}], "default": {}},
            "x-kubernetes-list-type": "map",
            "x-kubernetes-list-map-keys": ["containerPort", "protocol"],
            "x-kubernetes-patch-strategy": "merge",
            "x-kubernetes-patch-merge-key": "containerPort"
          },
          "unkeyed": {
            "type": "array",
            "items": {"allOf": [{"$ref": "#/components/schemas/io.k8s.api.core.v1.ContainerPort"}], "default": {}},
            "x-kubernetes-list-type": "map",
            "x-kubernetes-list-map-keys": ["name"]
          },
          "finalizers": {"type": "array", "items": {"type": "string", "default": ""}, "x-kubernetes-list-type": "set"},
          "targetPort": {"allOf": [{"$ref": "#/components/schemas/io.k8s.apimachinery.pkg.util.intstr.IntOrString"}]},
          "memory": {"allOf": [{"$ref": "#/components/schemas/io.k8s.apimachinery.pkg.api.resource.Quantity"}]},
          "raw": {"allOf": [{"$ref": "#/components/schemas/io.k8s.apimachinery.pkg.runtime.RawExtension"}]},
          "tree": {"allOf": [{"$ref": "#/components/schemas/example.Node"}]},
          "missing": {"allOf": [{"$ref": "#/components/schemas/example.Missing"}]}
        }
      },
      "io.k8s.api.batch.v1.JobStatus": {"type": "object", "properties": {"succeeded": {"type": "integer"}}},
      "io.k8s.api.core.v1.PodTemplateSpec": {
        "type": "object",
        "properties": {"metadata": {"allOf": [{"$ref": "#/components/schemas/io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"}], "default": {}}}
      },
      "io.k8s.api.core.v1.ContainerPort": {
        "type": "object",
        "required": ["containerPort"],
        "properties": {
          "containerPort": {"type": "integer", "format": "int32", "default": 0},
          "protocol": {"type": "string", "default": "TCP"},
          "name": {"type": "string"}
        }
      },
      "io.k8s.apimachinery.pkg.util.intstr.IntOrString": {"oneOf": [{"type": "string"}, {"type": "integer"}]},
      "io.k8s.apimachinery.pkg.api.resource.Quantity": {"oneOf": [{"type": "string"}, {"type": "number"}]},
      "io.k8s.apimachinery.pkg.runtime.RawExtension": {"type": "object"},
      "example.Node": {
        "type": "object",
        "properties": {"children": {"type": "array", "items": {"allOf": [{"$ref": "#/components/schemas/example.Node"}]}}}
      }
    }
  }
}`

var _ = Describe("SchemaFromOpenAPIV3", func() {
	jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

	It("inlines refs and converts to a structural schema", func() {
		root, err := resourcetemplate.SchemaFromOpenAPIV3([]byte(openAPIDoc), jobGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Type).To(Equal("object"))
		Expect(root.Description).To(ContainSubstring("Job represents"))

		spec := root.Properties["spec"]
		Expect(spec.Type).To(Equal("object"))
		Expect(spec.Description).To(Equal("Specification of the desired behavior of a job."), "allOf wrapper description wins")
		Expect(spec.Default).To(BeNil(), "object defaults are dropped")
		Expect(spec.Required).To(ConsistOf("template"))

		By("keeping scalar validations and defaults")
		Expect(spec.Properties["backoffLimit"].Type).To(Equal("integer"))
		Expect(spec.Properties["backoffLimit"].Format).To(Equal("int32"))
		Expect(spec.Properties["completionMode"].Enum).To(HaveLen(2))

		By("carrying valid list-map extensions and dropping invalid ones")
		ports := spec.Properties["ports"]
		Expect(ports.XListType).To(HaveValue(Equal("map")))
		Expect(ports.XListMapKeys).To(Equal([]string{"containerPort", "protocol"}))
		Expect(ports.Items.Schema.Properties["protocol"].Default).NotTo(BeNil(), "scalar default kept so the map key is valid")
		unkeyed := spec.Properties["unkeyed"]
		Expect(unkeyed.XListType).To(BeNil(), "list map key without default or required is downgraded")
		Expect(unkeyed.XListMapKeys).To(BeEmpty())
		Expect(spec.Properties["finalizers"].XListType).To(HaveValue(Equal("set")))

		By("mapping unions")
		Expect(spec.Properties["targetPort"].XIntOrString).To(BeTrue())
		Expect(spec.Properties["targetPort"].Type).To(BeEmpty())
		Expect(spec.Properties["memory"].XIntOrString).To(BeFalse())
		Expect(spec.Properties["memory"].XPreserveUnknownFields).To(HaveValue(BeTrue()))

		By("preserving unknown fields for untyped objects, recursion, and missing components")
		Expect(spec.Properties["raw"].Type).To(Equal("object"))
		Expect(spec.Properties["raw"].XPreserveUnknownFields).To(HaveValue(BeTrue()))
		tree := spec.Properties["tree"]
		Expect(tree.Properties).To(HaveKey("children"))
		Expect(tree.Properties["children"].Items.Schema.XPreserveUnknownFields).To(HaveValue(BeTrue()), "recursion is cut")
		Expect(spec.Properties["missing"].XPreserveUnknownFields).To(HaveValue(BeTrue()))

		By("mapping additionalProperties")
		labels := root.Properties["metadata"].Properties["labels"]
		Expect(labels.AdditionalProperties).NotTo(BeNil())
		Expect(labels.AdditionalProperties.Schema.Type).To(Equal("string"))
	})

	It("errors when the kind is not in the document", func() {
		_, err := resourcetemplate.SchemaFromOpenAPIV3([]byte(openAPIDoc), schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"})
		Expect(err).To(MatchError(ContainSubstring("no schema for batch/v1, Kind=CronJob")))
	})

	It("errors on an unparsable document", func() {
		_, err := resourcetemplate.SchemaFromOpenAPIV3([]byte("not json"), jobGVK)
		Expect(err).To(MatchError(ContainSubstring("parse OpenAPI v3 document")))
	})
})

var _ = Describe("TemplateSchemaFromRoot", func() {
	It("pins apiVersion and kind, opens metadata for templating, and drops status", func() {
		root, err := resourcetemplate.SchemaFromOpenAPIV3([]byte(openAPIDoc), schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"})
		Expect(err).NotTo(HaveOccurred())
		tmpl := resourcetemplate.TemplateSchemaFromRoot(root)
		Expect(tmpl.XEmbeddedResource).To(BeFalse(), "embedded-resource validation would reject Go templates in labels")
		Expect(tmpl.Type).To(Equal("object"))
		Expect(tmpl.Properties).NotTo(HaveKey("status"))
		Expect(tmpl.Properties["apiVersion"]).To(Equal(apiextensionsv1.JSONSchemaProps{Type: "string", Description: "APIVersion defines the versioned schema"}))
		Expect(tmpl.Properties["kind"].Type).To(Equal("string"))
		Expect(tmpl.Properties["metadata"].Type).To(Equal("object"))
		Expect(tmpl.Properties["metadata"].XPreserveUnknownFields).To(HaveValue(BeTrue()))
		Expect(tmpl.Properties["metadata"].Properties).To(BeEmpty())
		Expect(tmpl.Properties).To(HaveKey("spec"))
	})

	It("falls back to preserving unknown fields when the root has no properties", func() {
		tmpl := resourcetemplate.TemplateSchemaFromRoot(&apiextensionsv1.JSONSchemaProps{Type: "object"})
		Expect(tmpl.XPreserveUnknownFields).To(HaveValue(BeTrue()))
	})

	It("returns the fallback schema for nil", func() {
		Expect(resourcetemplate.TemplateSchemaFromRoot(nil)).To(Equal(resourcetemplate.FallbackTemplateSchema()))
	})
})

var _ = Describe("SchemaFromCRD", func() {
	It("returns a copy of the matching version's schema", func() {
		crd := &apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1"},
				{Name: "v1", Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object"}}}}},
			},
		}}
		got := resourcetemplate.SchemaFromCRD(crd, "v1")
		Expect(got).NotTo(BeNil())
		Expect(got.Properties).To(HaveKey("spec"))
		got.Properties["spec"] = apiextensionsv1.JSONSchemaProps{Type: "string"}
		Expect(crd.Spec.Versions[1].Schema.OpenAPIV3Schema.Properties["spec"].Type).To(Equal("object"), "returned schema is a copy")
		Expect(resourcetemplate.SchemaFromCRD(crd, "v1alpha1")).To(BeNil())
		Expect(resourcetemplate.SchemaFromCRD(crd, "v2")).To(BeNil())
	})
})

var _ = Describe("BuildCRD", func() {
	source := promoterv1alpha1.ResourceTemplateSource{Group: "batch", Version: "v1", Kind: "Job"}

	It("names and labels the template CRD and embeds the schema under spec.template", func() {
		crd := resourcetemplate.BuildCRD(source, resourcetemplate.TemplateSchemaFromRoot(&apiextensionsv1.JSONSchemaProps{
			Type:       "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object", XPreserveUnknownFields: ptr.To(true)}},
		}), resourcetemplate.SchemaSourceOpenAPI)
		Expect(crd.Name).To(Equal("jobtemplates.templates.promoter.argoproj.io"))
		Expect(crd.Spec.Group).To(Equal(promoterv1alpha1.ResourceTemplateGroup))
		Expect(crd.Spec.Scope).To(Equal(apiextensionsv1.NamespaceScoped))
		Expect(crd.Spec.Names.Kind).To(Equal("JobTemplate"))
		Expect(crd.Spec.Names.Plural).To(Equal("jobtemplates"))
		Expect(crd.Labels).To(HaveKeyWithValue(promoterv1alpha1.GeneratedTemplateLabel, "true"))
		Expect(crd.Annotations).To(HaveKeyWithValue(promoterv1alpha1.TemplateSourceGroupAnnotation, "batch"))
		Expect(crd.Annotations).To(HaveKeyWithValue(promoterv1alpha1.TemplateSourceVersionAnnotation, "v1"))
		Expect(crd.Annotations).To(HaveKeyWithValue(promoterv1alpha1.TemplateSourceKindAnnotation, "Job"))
		Expect(crd.Annotations).To(HaveKeyWithValue(promoterv1alpha1.TemplateSchemaSourceAnnotation, resourcetemplate.SchemaSourceOpenAPI))

		Expect(crd.Spec.Versions).To(HaveLen(1))
		root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
		Expect(root.Required).To(ConsistOf("spec"))
		template := root.Properties["spec"].Properties["template"]
		Expect(template.Type).To(Equal("object"))
		Expect(template.Properties).To(HaveKey("spec"))
		Expect(root.Properties["spec"].Required).To(ConsistOf("template"))
	})

	It("uses the fallback schema and records schema source none when no schema is given", func() {
		crd := resourcetemplate.BuildCRD(source, nil, resourcetemplate.SchemaSourceOpenAPI)
		Expect(crd.Annotations).To(HaveKeyWithValue(promoterv1alpha1.TemplateSchemaSourceAnnotation, resourcetemplate.SchemaSourceNone))
		template := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["template"]
		Expect(template.Type).To(Equal("object"))
		Expect(template.XPreserveUnknownFields).To(HaveValue(BeTrue()))
	})

	It("honors templateKind overrides", func() {
		crd := resourcetemplate.BuildCRD(promoterv1alpha1.ResourceTemplateSource{Group: "argoproj.io", Version: "v1alpha1", Kind: "Workflow", TemplateKind: "ArgoWorkflowTemplate"}, nil, "")
		Expect(crd.Name).To(Equal("argoworkflowtemplates.templates.promoter.argoproj.io"))
		Expect(crd.Spec.Names.Kind).To(Equal("ArgoWorkflowTemplate"))
		Expect(crd.Spec.Names.ListKind).To(Equal("ArgoWorkflowTemplateList"))
	})
})

var _ = Describe("Source helpers", func() {
	It("derives GVKs and finds sources by template kind", func() {
		sources := []promoterv1alpha1.ResourceTemplateSource{
			{Version: "v1", Kind: "ConfigMap"},
			{Group: "batch", Version: "v1", Kind: "Job", TemplateKind: "BatchJobTemplate"},
		}
		Expect(resourcetemplate.SourceGVK(sources[0])).To(Equal(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}))
		Expect(resourcetemplate.TemplateGVK(sources[0])).To(Equal(schema.GroupVersionKind{Group: promoterv1alpha1.ResourceTemplateGroup, Version: "v1alpha1", Kind: "ConfigMapTemplate"}))

		got, ok := resourcetemplate.FindSource(sources, "BatchJobTemplate")
		Expect(ok).To(BeTrue())
		Expect(got.Kind).To(Equal("Job"))
		_, ok = resourcetemplate.FindSource(sources, "JobTemplate")
		Expect(ok).To(BeFalse(), "the override replaces the default name")
	})

	It("rejects colliding template kinds", func() {
		err := resourcetemplate.ValidateSources([]promoterv1alpha1.ResourceTemplateSource{
			{Group: "argoproj.io", Version: "v1alpha1", Kind: "Workflow"},
			{Group: "example.com", Version: "v1", Kind: "Workflow"},
		})
		Expect(err).To(MatchError(ContainSubstring("WorkflowTemplate is generated for both")))
		Expect(resourcetemplate.ValidateSources(nil)).To(Succeed())
	})
})
