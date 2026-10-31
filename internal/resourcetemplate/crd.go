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

// Package resourcetemplate implements the wrapped template CRDs behind KubernetesResourceCommitStatus.
//
// ControllerConfiguration.spec.kubernetesResourceCommitStatus.templates lists kinds already installed in the
// cluster (for example batch/v1 Job). For each, Syncer generates a namespaced CustomResourceDefinition
// <Kind>Template.templates.promoter.argoproj.io whose spec.template is validated against the source kind's
// schema. Users write a manifest of the real kind into spec.template; because the wrapper is a different kind,
// the real controller never reconciles it. The KubernetesResourceCommitStatus controller renders the template
// (RenderTemplate) and creates the real kind, then maps its live state to a commit status phase
// (EvaluatePhase).
package resourcetemplate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/openapi"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// establishedTimeout bounds how long Sync waits for a generated CRD to become Established.
const establishedTimeout = 30 * time.Second

// Source is one configured kind to wrap.
type Source = promoterv1alpha1.ResourceTemplateSource

// SourceGVK returns the GroupVersionKind of the real resource created from templates of source.
func SourceGVK(source Source) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: source.Group, Version: source.Version, Kind: source.Kind}
}

// TemplateGVK returns the GroupVersionKind of the generated template kind for source.
func TemplateGVK(source Source) schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   promoterv1alpha1.ResourceTemplateGroup,
		Version: promoterv1alpha1.ResourceTemplateVersion,
		Kind:    source.TemplateKindName(),
	}
}

// TemplatePlural returns the lowercase plural resource name of the generated template kind (jobtemplates).
func TemplatePlural(source Source) string {
	return strings.ToLower(source.TemplateKindName()) + "s"
}

// TemplateCRDName returns the name of the generated CustomResourceDefinition for source.
func TemplateCRDName(source Source) string {
	return TemplatePlural(source) + "." + promoterv1alpha1.ResourceTemplateGroup
}

// FindSource returns the configured source whose generated template kind is templateKind.
func FindSource(sources []Source, templateKind string) (Source, bool) {
	for _, s := range sources {
		if s.TemplateKindName() == templateKind {
			return s, true
		}
	}
	return Source{}, false
}

// ValidateSources rejects configurations whose generated template kinds collide.
func ValidateSources(sources []Source) error {
	seen := make(map[string]Source, len(sources))
	for _, s := range sources {
		kind := s.TemplateKindName()
		if prev, ok := seen[kind]; ok {
			return fmt.Errorf("template kind %s is generated for both %s and %s; set templateKind on one of them",
				kind, SourceGVK(prev), SourceGVK(s))
		}
		seen[kind] = s
	}
	return nil
}

// BuildCRD returns the CustomResourceDefinition wrapping source. templateSchema is the schema of spec.template
// (see TemplateSchemaFromRoot and FallbackTemplateSchema); schemaSource is recorded in an annotation.
func BuildCRD(source Source, templateSchema *apiextensionsv1.JSONSchemaProps, schemaSource string) *apiextensionsv1.CustomResourceDefinition {
	if templateSchema == nil {
		templateSchema = FallbackTemplateSchema()
		schemaSource = SchemaSourceNone
	}
	kind := source.TemplateKindName()
	sourceGVK := SourceGVK(source)
	return &apiextensionsv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{APIVersion: apiextensionsv1.SchemeGroupVersion.String(), Kind: "CustomResourceDefinition"},
		ObjectMeta: metav1.ObjectMeta{
			Name: TemplateCRDName(source),
			Labels: map[string]string{
				promoterv1alpha1.GeneratedTemplateLabel: "true",
			},
			Annotations: map[string]string{
				promoterv1alpha1.TemplateSourceGroupAnnotation:   source.Group,
				promoterv1alpha1.TemplateSourceVersionAnnotation: source.Version,
				promoterv1alpha1.TemplateSourceKindAnnotation:    source.Kind,
				promoterv1alpha1.TemplateSchemaSourceAnnotation:  schemaSource,
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: promoterv1alpha1.ResourceTemplateGroup,
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   TemplatePlural(source),
				Singular: strings.ToLower(kind),
				Kind:     kind,
				ListKind: kind + "List",
				Categories: []string{
					"promoter-templates",
				},
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    promoterv1alpha1.ResourceTemplateVersion,
				Served:  true,
				Storage: true,
				AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
					{Name: "Source", Type: typeString, JSONPath: ".spec.template.kind"},
					{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"},
				},
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: typeObject,
						Description: fmt.Sprintf("%s wraps a %s manifest for KubernetesResourceCommitStatus. The wrapper is never "+
							"reconciled by the %s controller; the KubernetesResourceCommitStatus controller renders spec.template "+
							"and creates a real %s per environment and SHA. Generated by GitOps Promoter from "+
							"ControllerConfiguration.spec.kubernetesResourceCommitStatus.templates; do not edit.",
							kind, sourceGVK, source.Kind, source.Kind),
						Required: []string{"spec"},
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"apiVersion": {Type: typeString},
							"kind":       {Type: typeString},
							"metadata":   {Type: typeObject},
							"spec": {
								Type:     typeObject,
								Required: []string{"template"},
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									"template": *templateSchema,
								},
							},
						},
					},
				},
			}},
		},
	}
}

// Syncer generates and updates the template CRDs for a list of sources.
type Syncer struct {
	client  client.Client
	mapper  meta.RESTMapper
	openAPI openapi.Client
}

// NewSyncer returns a Syncer that reads and writes CRDs with c, resolves kinds with mapper, and derives schemas of
// built-in kinds from the OpenAPI v3 document served by the cluster cfg points at. c should read without the
// manager cache (an API reader or a direct client) because Sync runs before the cache starts.
func NewSyncer(cfg *rest.Config, c client.Client, mapper meta.RESTMapper) (*Syncer, error) {
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create discovery client: %w", err)
	}
	return &Syncer{client: c, mapper: mapper, openAPI: disco.OpenAPIV3()}, nil
}

// NewSyncerWithOpenAPI is NewSyncer with an injected OpenAPI client (tests).
func NewSyncerWithOpenAPI(c client.Client, mapper meta.RESTMapper, openAPI openapi.Client) *Syncer {
	return &Syncer{client: c, mapper: mapper, openAPI: openAPI}
}

// Sync creates or updates the template CRD of every source and waits until each is Established. Sources that fail
// (kind not installed, CRD rejected) are reported together after every other source was processed, so one bad
// entry does not block the rest.
func (s *Syncer) Sync(ctx context.Context, sources []Source) error {
	if err := ValidateSources(sources); err != nil {
		return err
	}
	var errs []error
	for _, source := range sources {
		if err := s.syncOne(ctx, source); err != nil {
			errs = append(errs, fmt.Errorf("template CRD for %s: %w", SourceGVK(source), err))
		}
	}
	return errors.Join(errs...)
}

func (s *Syncer) syncOne(ctx context.Context, source Source) error {
	logger := log.FromContext(ctx).WithValues("source", SourceGVK(source).String(), "templateKind", source.TemplateKindName())
	gvk := SourceGVK(source)
	mapping, err := s.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("source kind is not served by the cluster: %w", err)
	}

	templateSchema, schemaSource := s.templateSchema(ctx, source, mapping)
	logger.Info("Generating template CRD", "schemaSource", schemaSource)
	if err := s.apply(ctx, BuildCRD(source, templateSchema, schemaSource)); err != nil {
		if !k8serrors.IsInvalid(err) || schemaSource == SchemaSourceNone {
			return err
		}
		// The derived schema was rejected by the API server (for example a non-structural construct the
		// conversion did not anticipate). Fall back to an unvalidated template rather than failing the kind.
		logger.Error(err, "API server rejected the derived template schema; falling back to a schema without validation")
		if err := s.apply(ctx, BuildCRD(source, nil, SchemaSourceNone)); err != nil {
			return err
		}
	}
	return s.waitEstablished(ctx, TemplateCRDName(source))
}

// templateSchema derives the spec.template schema for source, preferring the source CRD, then OpenAPI v3.
func (s *Syncer) templateSchema(ctx context.Context, source Source, mapping *meta.RESTMapping) (*apiextensionsv1.JSONSchemaProps, string) {
	logger := log.FromContext(ctx)
	gvk := SourceGVK(source)

	if source.Group != "" {
		var crd apiextensionsv1.CustomResourceDefinition
		err := s.client.Get(ctx, client.ObjectKey{Name: mapping.Resource.Resource + "." + source.Group}, &crd)
		switch {
		case err == nil:
			if root := SchemaFromCRD(&crd, source.Version); root != nil {
				return TemplateSchemaFromRoot(root), SchemaSourceCRD
			}
			logger.Info("Source CRD has no schema for the configured version; trying OpenAPI", "crd", crd.Name)
		case !k8serrors.IsNotFound(err):
			logger.Error(err, "Failed to read source CRD; trying OpenAPI", "crd", mapping.Resource.Resource+"."+source.Group)
		default:
			logger.V(4).Info("Source kind has no CustomResourceDefinition; trying OpenAPI", "crd", mapping.Resource.Resource+"."+source.Group)
		}
	}

	if s.openAPI != nil {
		root, err := s.schemaFromOpenAPI(gvk)
		if err == nil {
			return TemplateSchemaFromRoot(root), SchemaSourceOpenAPI
		}
		logger.Error(err, "Failed to derive template schema from OpenAPI; the template will not be validated")
	}
	return nil, SchemaSourceNone
}

func (s *Syncer) schemaFromOpenAPI(gvk schema.GroupVersionKind) (*apiextensionsv1.JSONSchemaProps, error) {
	paths, err := s.openAPI.Paths()
	if err != nil {
		return nil, fmt.Errorf("list OpenAPI v3 paths: %w", err)
	}
	path := "apis/" + gvk.Group + "/" + gvk.Version
	if gvk.Group == "" {
		path = "api/" + gvk.Version
	}
	gv, ok := paths[path]
	if !ok {
		return nil, fmt.Errorf("OpenAPI v3 has no document for %s", path)
	}
	document, err := gv.Schema(runtime.ContentTypeJSON)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenAPI v3 document %s: %w", path, err)
	}
	return SchemaFromOpenAPIV3(document, gvk)
}

// apply creates the CRD or updates the spec, labels, and annotations of the existing one.
func (s *Syncer) apply(ctx context.Context, desired *apiextensionsv1.CustomResourceDefinition) error {
	var existing apiextensionsv1.CustomResourceDefinition
	err := s.client.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if k8serrors.IsNotFound(err) {
		if err := s.client.Create(ctx, desired); err != nil {
			return fmt.Errorf("create CustomResourceDefinition %s: %w", desired.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get CustomResourceDefinition %s: %w", desired.Name, err)
	}
	if existing.Labels[promoterv1alpha1.GeneratedTemplateLabel] != "true" {
		return fmt.Errorf("CustomResourceDefinition %s exists but was not generated by GitOps Promoter (missing label %s); "+
			"rename the template kind with templateKind or delete the CRD", desired.Name, promoterv1alpha1.GeneratedTemplateLabel)
	}
	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	existing.Spec = desired.Spec
	if err := s.client.Update(ctx, &existing); err != nil {
		return fmt.Errorf("update CustomResourceDefinition %s: %w", desired.Name, err)
	}
	return nil
}

// waitEstablished blocks until the CRD reports Established=True or the timeout elapses.
func (s *Syncer) waitEstablished(ctx context.Context, name string) error {
	err := wait.PollUntilContextTimeout(ctx, 250*time.Millisecond, establishedTimeout, true, func(ctx context.Context) (bool, error) {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := s.client.Get(ctx, client.ObjectKey{Name: name}, &crd); err != nil {
			return false, fmt.Errorf("get CustomResourceDefinition %s: %w", name, err)
		}
		for _, cond := range crd.Status.Conditions {
			if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
				return true, nil
			}
			if cond.Type == apiextensionsv1.NamesAccepted && cond.Status == apiextensionsv1.ConditionFalse {
				return false, fmt.Errorf("CustomResourceDefinition %s names were not accepted: %s", name, cond.Message)
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for CustomResourceDefinition %s to be established: %w", name, err)
	}
	return nil
}
