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

package resourcetemplate

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

// shortShaLength is how many leading characters of a SHA ShortSha keeps.
const shortShaLength = 7

// TemplateData is the data passed to the Go templates in a wrapped template's spec.template and to
// spec.descriptionTemplate and spec.urlTemplate of a KubernetesResourceCommitStatus. Phase and Resource are
// only set for description and URL rendering, after the phase expression ran.
type TemplateData struct {
	Environment                    *promoterv1alpha1.EnvironmentStatus
	PromotionStrategy              *promoterv1alpha1.PromotionStrategy
	KubernetesResourceCommitStatus *promoterv1alpha1.KubernetesResourceCommitStatus
	// Resource is the live resource (nil when it does not exist). Set for description/URL templates only.
	Resource     map[string]any
	Branch       string
	Sha          string
	ShortSha     string
	Key          string
	ReportOn     string
	ResourceName string
	Namespace    string
	// Phase is the phase computed this reconcile. Set for description/URL templates only.
	Phase string
}

// NewTemplateData builds the template data for one environment of krcs. resourceName is the name the controller
// assigns to the created resource.
func NewTemplateData(
	krcs *promoterv1alpha1.KubernetesResourceCommitStatus,
	ps *promoterv1alpha1.PromotionStrategy,
	env *promoterv1alpha1.EnvironmentStatus,
	sha, resourceName string,
) TemplateData {
	return TemplateData{
		Environment:                    env,
		PromotionStrategy:              ps,
		KubernetesResourceCommitStatus: krcs,
		Branch:                         env.Branch,
		Sha:                            sha,
		ShortSha:                       utils.TruncateString(sha, shortShaLength),
		Key:                            krcs.Spec.Key,
		ReportOn:                       krcs.Spec.ReportOn,
		ResourceName:                   resourceName,
		Namespace:                      krcs.Namespace,
	}
}

// ErrTemplateMissing is returned when a wrapped template has no spec.template object.
var ErrTemplateMissing = errors.New("template has no spec.template object")

// RenderTemplate renders the spec.template of the wrapped template object tmpl into the resource to create.
// Every string value is rendered as a Go template with Sprig functions (see utils.RenderStringTemplate); map
// keys and non-string scalars are copied as-is. The result's apiVersion and kind must match gvk, and its
// metadata name, namespace, and ownerReferences are replaced by the controller, so the function sets name and
// namespace from data and clears owner references; status is dropped.
func RenderTemplate(tmpl *unstructured.Unstructured, gvk schema.GroupVersionKind, data TemplateData) (*unstructured.Unstructured, error) {
	raw, found, err := unstructured.NestedMap(tmpl.Object, "spec", "template")
	if err != nil {
		return nil, fmt.Errorf("read spec.template of %s %q: %w", tmpl.GetKind(), tmpl.GetName(), err)
	}
	if !found {
		return nil, fmt.Errorf("%s %q: %w", tmpl.GetKind(), tmpl.GetName(), ErrTemplateMissing)
	}

	rendered, err := renderValue(raw, data, "spec.template")
	if err != nil {
		return nil, err
	}
	obj, ok := rendered.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s %q: %w", tmpl.GetKind(), tmpl.GetName(), ErrTemplateMissing)
	}

	out := &unstructured.Unstructured{Object: obj}
	if got := out.GroupVersionKind(); got != gvk {
		return nil, fmt.Errorf("%s %q spec.template is %s, want %s", tmpl.GetKind(), tmpl.GetName(), got, gvk)
	}
	delete(out.Object, "status")
	out.SetName(data.ResourceName)
	out.SetNamespace(data.Namespace)
	out.SetOwnerReferences(nil)
	return out, nil
}

// renderValue walks v and renders every string leaf that contains a template action.
func renderValue(v any, data TemplateData, path string) (any, error) {
	switch typed := v.(type) {
	case string:
		if !strings.Contains(typed, "{{") {
			return typed, nil
		}
		rendered, err := utils.RenderStringTemplate(typed, data)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", path, err)
		}
		return rendered, nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, child := range typed {
			rendered, err := renderValue(child, data, path+"."+k)
			if err != nil {
				return nil, err
			}
			out[k] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			rendered, err := renderValue(child, data, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	default:
		return v, nil
	}
}

// RenderString renders a Go template string (description or URL template) with data. Empty input yields "".
func RenderString(tmpl string, data TemplateData) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	out, err := utils.RenderStringTemplate(tmpl, data)
	if err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}
	return out, nil
}
