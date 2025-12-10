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
	"errors"
	"fmt"
	"reflect"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"
)

// ResourceExecutor handles Kubernetes resource creation/update.
type ResourceExecutor struct {
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
}

// NewResourceExecutor creates a new ResourceExecutor.
func NewResourceExecutor(c client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) *ResourceExecutor {
	return &ResourceExecutor{
		client:   c,
		scheme:   scheme,
		recorder: recorder,
	}
}

// Execute creates or updates a Kubernetes resource based on the template.
func (r *ResourceExecutor) Execute(ctx context.Context, hook *promoterv1alpha1.PromotionEventHook, ps *promoterv1alpha1.PromotionStrategy, webhookResp *utils.WebhookResponse) (*utils.ResourceResult, error) {
	resource := hook.Spec.Action.Resource
	if resource == nil {
		return nil, errors.New("resource action is nil")
	}

	// Use webhookResponseData from status if available
	// This is set by webhookResponseExpr after webhook execution
	var data any
	if len(hook.Status.WebhookResponseData) > 0 {
		data = hook.Status.WebhookResponseData
	}

	// Build template context
	templateCtx := PromotionEventHookTemplateContext{
		PromotionStrategy: ps,
		Data:              data,
	}

	// Render template
	renderedYAML, err := utils.RenderStringTemplate(resource.Template, templateCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to render resource template: %w", err)
	}

	// Parse YAML to unstructured
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(renderedYAML), &obj.Object); err != nil {
		return nil, fmt.Errorf("failed to parse rendered YAML: %w", err)
	}

	// Validate namespace matches hook namespace
	objNamespace := obj.GetNamespace()
	if objNamespace == "" {
		// Default to hook's namespace
		obj.SetNamespace(hook.Namespace)
		objNamespace = hook.Namespace
	}
	if objNamespace != hook.Namespace {
		return nil, fmt.Errorf("resource namespace %q does not match hook namespace %q", objNamespace, hook.Namespace)
	}

	// Set owner reference if requested
	if resource.SetOwnerReference {
		r.setOwnerReference(hook, obj)
	}

	// Create or update the resource
	result, err := r.createOrUpdate(ctx, obj)
	if err != nil {
		return nil, fmt.Errorf("failed to create or update resource: %w", err)
	}

	return result, nil
}

// setOwnerReference sets the PromotionEventHook as the owner of the resource.
func (r *ResourceExecutor) setOwnerReference(hook *promoterv1alpha1.PromotionEventHook, obj *unstructured.Unstructured) {
	// Get the GVK for PromotionEventHook
	kind := reflect.TypeOf(promoterv1alpha1.PromotionEventHook{}).Name()
	gvk := promoterv1alpha1.GroupVersion.WithKind(kind)

	ownerRef := metav1.NewControllerRef(hook, gvk)
	ownerRef.BlockOwnerDeletion = nil // Don't block deletion

	existingRefs := obj.GetOwnerReferences()
	existingRefs = append(existingRefs, *ownerRef)
	obj.SetOwnerReferences(existingRefs)
}

// createOrUpdate creates or updates the resource using controllerutil.CreateOrUpdate pattern.
func (r *ResourceExecutor) createOrUpdate(ctx context.Context, desired *unstructured.Unstructured) (*utils.ResourceResult, error) {
	// Create a copy for the existing resource lookup
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	existing.SetName(desired.GetName())
	existing.SetNamespace(desired.GetNamespace())

	result, err := controllerutil.CreateOrUpdate(ctx, r.client, existing, func() error {
		// Copy spec and other relevant fields from desired to existing
		// We preserve metadata from existing (like resourceVersion) but update the content
		existingAnnotations := existing.GetAnnotations()
		existingLabels := existing.GetLabels()

		// Merge annotations
		desiredAnnotations := desired.GetAnnotations()
		if desiredAnnotations == nil {
			desiredAnnotations = make(map[string]string)
		}
		for k, v := range existingAnnotations {
			if _, ok := desiredAnnotations[k]; !ok {
				desiredAnnotations[k] = v
			}
		}

		// Merge labels
		desiredLabels := desired.GetLabels()
		if desiredLabels == nil {
			desiredLabels = make(map[string]string)
		}
		for k, v := range existingLabels {
			if _, ok := desiredLabels[k]; !ok {
				desiredLabels[k] = v
			}
		}

		// Copy the content from desired
		existing.Object = desired.DeepCopy().Object
		existing.SetAnnotations(desiredAnnotations)
		existing.SetLabels(desiredLabels)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create or update resource: %w", err)
	}

	gvk := existing.GroupVersionKind()
	resourceResult := &utils.ResourceResult{
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
		Namespace:  existing.GetNamespace(),
		Name:       existing.GetName(),
	}

	// Log the operation result
	switch result {
	case controllerutil.OperationResultCreated:
		r.recorder.Eventf(existing, "Normal", "Created", "Created resource %s/%s", existing.GetNamespace(), existing.GetName())
	case controllerutil.OperationResultUpdated:
		r.recorder.Eventf(existing, "Normal", "Updated", "Updated resource %s/%s", existing.GetNamespace(), existing.GetName())
	default:
		// No change needed
	}

	return resourceResult, nil
}

// GetGVKFromUnstructured extracts the GroupVersionKind from an unstructured object.
func GetGVKFromUnstructured(obj *unstructured.Unstructured) schema.GroupVersionKind {
	return obj.GroupVersionKind()
}
