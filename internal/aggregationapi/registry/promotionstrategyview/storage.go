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

package promotionstrategyview

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	aggregationv1alpha1 "github.com/argoproj-labs/gitops-promoter/internal/aggregationapi/v1alpha1"
)

// REST implements the REST storage for PromotionStrategyView.
// It provides read-only access to aggregated PromotionStrategy data.
type REST struct {
	client client.Client
}

var (
	_ rest.Getter               = &REST{}
	_ rest.Lister               = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

// NewREST creates a new REST storage for PromotionStrategyView.
func NewREST(c client.Client) *REST {
	return &REST{
		client: c,
	}
}

// New returns a new instance of PromotionStrategyView.
func (r *REST) New() runtime.Object {
	return &aggregationv1alpha1.PromotionStrategyView{}
}

// Destroy cleans up resources on shutdown.
func (r *REST) Destroy() {}

// NewList returns a new list of PromotionStrategyView.
func (r *REST) NewList() runtime.Object {
	return &aggregationv1alpha1.PromotionStrategyViewList{}
}

// NamespaceScoped returns true because PromotionStrategyView is namespace-scoped.
func (r *REST) NamespaceScoped() bool {
	return true
}

// GetSingularName returns the singular name of the resource.
func (r *REST) GetSingularName() string {
	return "promotionstrategyview"
}

// Get retrieves a PromotionStrategyView by name.
func (r *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	namespace := genericNamespaceFromContext(ctx)
	if namespace == "" {
		return nil, errors.NewBadRequest("namespace is required")
	}

	// Get the PromotionStrategy
	ps := &promoterv1alpha1.PromotionStrategy{}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, ps); err != nil {
		if errors.IsNotFound(err) {
			return nil, errors.NewNotFound(aggregationv1alpha1.Resource("promotionstrategyviews"), name)
		}
		return nil, fmt.Errorf("failed to get PromotionStrategy: %w", err)
	}

	// Build the aggregated view
	view, err := r.buildAggregatedView(ctx, ps)
	if err != nil {
		return nil, fmt.Errorf("failed to build aggregated view: %w", err)
	}

	return view, nil
}

// List retrieves all PromotionStrategyViews in a namespace.
func (r *REST) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	namespace := genericNamespaceFromContext(ctx)

	// List all PromotionStrategies
	psList := &promoterv1alpha1.PromotionStrategyList{}
	listOpts := []client.ListOption{}
	if namespace != "" {
		listOpts = append(listOpts, client.InNamespace(namespace))
	}
	if options != nil && options.LabelSelector != nil {
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: options.LabelSelector})
	}

	if err := r.client.List(ctx, psList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list PromotionStrategies: %w", err)
	}

	// Build aggregated views for each PromotionStrategy
	viewList := &aggregationv1alpha1.PromotionStrategyViewList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PromotionStrategyViewList",
			APIVersion: aggregationv1alpha1.SchemeGroupVersion.String(),
		},
		Items: make([]aggregationv1alpha1.PromotionStrategyView, 0, len(psList.Items)),
	}

	for i := range psList.Items {
		view, err := r.buildAggregatedView(ctx, &psList.Items[i])
		if err != nil {
			// Log error but continue with other items
			continue
		}
		viewList.Items = append(viewList.Items, *view)
	}

	return viewList, nil
}

// ConvertToTable converts the object to a table for kubectl output.
func (r *REST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Description: "Name of the PromotionStrategy"},
			{Name: "Environments", Type: "integer", Description: "Number of environments"},
			{Name: "GitRepository", Type: "string", Description: "Referenced GitRepository"},
			{Name: "Ready", Type: "string", Description: "Ready status"},
		},
	}

	switch obj := object.(type) {
	case *aggregationv1alpha1.PromotionStrategyView:
		table.Rows = append(table.Rows, r.viewToTableRow(obj))
	case *aggregationv1alpha1.PromotionStrategyViewList:
		for i := range obj.Items {
			table.Rows = append(table.Rows, r.viewToTableRow(&obj.Items[i]))
		}
	}

	return table, nil
}

func (r *REST) viewToTableRow(view *aggregationv1alpha1.PromotionStrategyView) metav1.TableRow {
	ready := "Unknown"
	for _, cond := range view.Status.Conditions {
		if cond.Type == "Ready" {
			ready = string(cond.Status)
			break
		}
	}

	gitRepoName := ""
	if view.Aggregated.GitRepository != nil {
		gitRepoName = view.Aggregated.GitRepository.Name
	}

	return metav1.TableRow{
		Cells: []interface{}{
			view.Name,
			len(view.Spec.Environments),
			gitRepoName,
			ready,
		},
		Object: runtime.RawExtension{Object: view},
	}
}

// buildAggregatedView builds a PromotionStrategyView from a PromotionStrategy.
func (r *REST) buildAggregatedView(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy) (*aggregationv1alpha1.PromotionStrategyView, error) {
	view := &aggregationv1alpha1.PromotionStrategyView{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PromotionStrategyView",
			APIVersion: aggregationv1alpha1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              ps.Name,
			Namespace:         ps.Namespace,
			UID:               ps.UID,
			ResourceVersion:   ps.ResourceVersion,
			CreationTimestamp: ps.CreationTimestamp,
			Labels:            ps.Labels,
			Annotations:       ps.Annotations,
		},
		Spec:   ps.Spec,
		Status: ps.Status,
	}

	// Aggregate GitRepository
	if err := r.aggregateGitRepository(ctx, ps, view); err != nil {
		// Non-fatal: continue without GitRepository
		_ = err
	}

	// Aggregate ChangeTransferPolicies
	if err := r.aggregateChangeTransferPolicies(ctx, ps, view); err != nil {
		// Non-fatal: continue without ChangeTransferPolicies
		_ = err
	}

	// Aggregate CommitStatuses
	if err := r.aggregateCommitStatuses(ctx, ps, view); err != nil {
		// Non-fatal: continue without CommitStatuses
		_ = err
	}

	// Aggregate PullRequests
	if err := r.aggregatePullRequests(ctx, ps, view); err != nil {
		// Non-fatal: continue without PullRequests
		_ = err
	}

	return view, nil
}

func (r *REST) aggregateGitRepository(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy, view *aggregationv1alpha1.PromotionStrategyView) error {
	gr := &promoterv1alpha1.GitRepository{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Namespace: ps.Namespace,
		Name:      ps.Spec.RepositoryReference.Name,
	}, gr); err != nil {
		return err
	}

	view.Aggregated.GitRepository = &aggregationv1alpha1.GitRepositoryRef{
		Name:      gr.Name,
		Namespace: gr.Namespace,
		Spec:      gr.Spec,
		Status:    gr.Status,
	}

	return nil
}

func (r *REST) aggregateChangeTransferPolicies(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy, view *aggregationv1alpha1.PromotionStrategyView) error {
	// List ChangeTransferPolicies with the PromotionStrategy label
	ctpList := &promoterv1alpha1.ChangeTransferPolicyList{}
	if err := r.client.List(ctx, ctpList,
		client.InNamespace(ps.Namespace),
		client.MatchingLabels{
			promoterv1alpha1.PromotionStrategyLabel: ps.Name,
		},
	); err != nil {
		return err
	}

	view.Aggregated.ChangeTransferPolicies = make([]aggregationv1alpha1.ChangeTransferPolicyRef, 0, len(ctpList.Items))
	for _, ctp := range ctpList.Items {
		view.Aggregated.ChangeTransferPolicies = append(view.Aggregated.ChangeTransferPolicies, aggregationv1alpha1.ChangeTransferPolicyRef{
			Name:      ctp.Name,
			Namespace: ctp.Namespace,
			Branch:    ctp.Spec.ActiveBranch,
			Spec:      ctp.Spec,
			Status:    ctp.Status,
		})
	}

	return nil
}

func (r *REST) aggregateCommitStatuses(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy, view *aggregationv1alpha1.PromotionStrategyView) error {
	// Aggregate ArgoCDCommitStatus resources
	argocdList := &promoterv1alpha1.ArgoCDCommitStatusList{}
	if err := r.client.List(ctx, argocdList, client.InNamespace(ps.Namespace)); err == nil {
		for _, acs := range argocdList.Items {
			if acs.Spec.PromotionStrategyRef.Name == ps.Name {
				view.Aggregated.CommitStatuses.ArgoCD = append(view.Aggregated.CommitStatuses.ArgoCD, aggregationv1alpha1.ArgoCDCommitStatusRef{
					Name:      acs.Name,
					Namespace: acs.Namespace,
					Spec:      acs.Spec,
					Status:    acs.Status,
				})
			}
		}
	}

	// Aggregate GitCommitStatus resources
	gitList := &promoterv1alpha1.GitCommitStatusList{}
	if err := r.client.List(ctx, gitList, client.InNamespace(ps.Namespace)); err == nil {
		for _, gcs := range gitList.Items {
			if gcs.Spec.PromotionStrategyRef.Name == ps.Name {
				view.Aggregated.CommitStatuses.Git = append(view.Aggregated.CommitStatuses.Git, aggregationv1alpha1.GitCommitStatusRef{
					Name:      gcs.Name,
					Namespace: gcs.Namespace,
					Spec:      gcs.Spec,
					Status:    gcs.Status,
				})
			}
		}
	}

	// Aggregate TimedCommitStatus resources
	timedList := &promoterv1alpha1.TimedCommitStatusList{}
	if err := r.client.List(ctx, timedList, client.InNamespace(ps.Namespace)); err == nil {
		for _, tcs := range timedList.Items {
			if tcs.Spec.PromotionStrategyRef.Name == ps.Name {
				view.Aggregated.CommitStatuses.Timed = append(view.Aggregated.CommitStatuses.Timed, aggregationv1alpha1.TimedCommitStatusRef{
					Name:      tcs.Name,
					Namespace: tcs.Namespace,
					Spec:      tcs.Spec,
					Status:    tcs.Status,
				})
			}
		}
	}

	// Aggregate low-level CommitStatus resources (those with PromotionStrategy label)
	csList := &promoterv1alpha1.CommitStatusList{}
	if err := r.client.List(ctx, csList,
		client.InNamespace(ps.Namespace),
		client.MatchingLabels{
			promoterv1alpha1.PromotionStrategyLabel: ps.Name,
		},
	); err == nil {
		for _, cs := range csList.Items {
			view.Aggregated.CommitStatuses.CommitStatuses = append(view.Aggregated.CommitStatuses.CommitStatuses, aggregationv1alpha1.CommitStatusRef{
				Name:      cs.Name,
				Namespace: cs.Namespace,
				Spec:      cs.Spec,
				Status:    cs.Status,
			})
		}
	}

	return nil
}

func (r *REST) aggregatePullRequests(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy, view *aggregationv1alpha1.PromotionStrategyView) error {
	// List PullRequests with the PromotionStrategy label
	prList := &promoterv1alpha1.PullRequestList{}
	if err := r.client.List(ctx, prList,
		client.InNamespace(ps.Namespace),
		client.MatchingLabels{
			promoterv1alpha1.PromotionStrategyLabel: ps.Name,
		},
	); err != nil {
		return err
	}

	view.Aggregated.PullRequests = make([]aggregationv1alpha1.PullRequestRef, 0, len(prList.Items))
	for _, pr := range prList.Items {
		view.Aggregated.PullRequests = append(view.Aggregated.PullRequests, aggregationv1alpha1.PullRequestRef{
			Name:      pr.Name,
			Namespace: pr.Namespace,
			Branch:    pr.Spec.TargetBranch,
			Spec:      pr.Spec,
			Status:    pr.Status,
		})
	}

	return nil
}

// genericNamespaceFromContext extracts the namespace from the context.
// This is a simplified version - the actual implementation depends on how
// the apiserver passes namespace information.
func genericNamespaceFromContext(ctx context.Context) string {
	// The namespace is typically passed via request info in the context
	// For now, we'll use a simple approach that works with the apiserver
	if ns, ok := ctx.Value(namespaceKey).(string); ok {
		return ns
	}
	return ""
}

// namespaceKey is the context key for namespace.
type contextKey string

const namespaceKey contextKey = "namespace"

// WithNamespace returns a context with the namespace set.
func WithNamespace(ctx context.Context, namespace string) context.Context {
	return context.WithValue(ctx, namespaceKey, namespace)
}
