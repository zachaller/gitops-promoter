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
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
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
	_ rest.Watcher              = &REST{}
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

// Watch implements rest.Watcher.
// It watches PromotionStrategy resources and emits aggregated PromotionStrategyView events.
func (r *REST) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	namespace := genericNamespaceFromContext(ctx)

	// Create a new aggregating watcher
	aw := newAggregatingWatcher(ctx, r, namespace, options)

	// Start watching PromotionStrategies
	go aw.run()

	return aw, nil
}

// aggregatingWatcher watches multiple resources and emits aggregated events.
type aggregatingWatcher struct {
	ctx       context.Context
	rest      *REST
	namespace string
	options   *metainternalversion.ListOptions
	result    chan watch.Event
	done      chan struct{}
	once      sync.Once
}

func newAggregatingWatcher(ctx context.Context, r *REST, namespace string, options *metainternalversion.ListOptions) *aggregatingWatcher {
	return &aggregatingWatcher{
		ctx:       ctx,
		rest:      r,
		namespace: namespace,
		options:   options,
		result:    make(chan watch.Event, 100),
		done:      make(chan struct{}),
	}
}

// ResultChan returns the channel for watch events.
func (w *aggregatingWatcher) ResultChan() <-chan watch.Event {
	return w.result
}

// Stop stops the watcher.
func (w *aggregatingWatcher) Stop() {
	w.once.Do(func() {
		close(w.done)
	})
}

func (w *aggregatingWatcher) run() {
	defer close(w.result)

	// Create a watch on PromotionStrategies
	psList := &promoterv1alpha1.PromotionStrategyList{}
	listOpts := []client.ListOption{}
	if w.namespace != "" {
		listOpts = append(listOpts, client.InNamespace(w.namespace))
	}
	if w.options != nil && w.options.LabelSelector != nil {
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: w.options.LabelSelector})
	}

	// Get the initial list to establish the resource version
	if err := w.rest.client.List(w.ctx, psList, listOpts...); err != nil {
		w.result <- watch.Event{
			Type: watch.Error,
			Object: &metav1.Status{
				Status:  metav1.StatusFailure,
				Message: fmt.Sprintf("failed to list PromotionStrategies: %v", err),
				Reason:  metav1.StatusReasonInternalError,
				Code:    500,
			},
		}
		return
	}

	// Send initial ADDED events for existing resources
	for i := range psList.Items {
		view, err := w.rest.buildAggregatedView(w.ctx, &psList.Items[i])
		if err != nil {
			continue
		}
		select {
		case w.result <- watch.Event{Type: watch.Added, Object: view}:
		case <-w.done:
			return
		case <-w.ctx.Done():
			return
		}
	}

	// Start watching for changes
	watchClient, ok := w.rest.client.(client.WithWatch)
	if !ok {
		w.result <- watch.Event{
			Type: watch.Error,
			Object: &metav1.Status{
				Status:  metav1.StatusFailure,
				Message: "client does not support watch",
				Reason:  metav1.StatusReasonInternalError,
				Code:    500,
			},
		}
		return
	}

	psWatcher, err := watchClient.Watch(w.ctx, psList, listOpts...)
	if err != nil {
		w.result <- watch.Event{
			Type: watch.Error,
			Object: &metav1.Status{
				Status:  metav1.StatusFailure,
				Message: fmt.Sprintf("failed to watch PromotionStrategies: %v", err),
				Reason:  metav1.StatusReasonInternalError,
				Code:    500,
			},
		}
		return
	}
	defer psWatcher.Stop()

	// Process watch events
	for {
		select {
		case event, ok := <-psWatcher.ResultChan():
			if !ok {
				return
			}

			ps, ok := event.Object.(*promoterv1alpha1.PromotionStrategy)
			if !ok {
				// Handle error or status objects
				if status, ok := event.Object.(*metav1.Status); ok {
					w.result <- watch.Event{Type: watch.Error, Object: status}
				}
				continue
			}

			// Build the aggregated view
			view, err := w.rest.buildAggregatedView(w.ctx, ps)
			if err != nil {
				continue
			}

			// Convert the event type
			var eventType watch.EventType
			switch event.Type {
			case watch.Added:
				eventType = watch.Added
			case watch.Modified:
				eventType = watch.Modified
			case watch.Deleted:
				eventType = watch.Deleted
			case watch.Bookmark:
				eventType = watch.Bookmark
			default:
				continue
			}

			select {
			case w.result <- watch.Event{Type: eventType, Object: view}:
			case <-w.done:
				return
			case <-w.ctx.Done():
				return
			}

		case <-w.done:
			return
		case <-w.ctx.Done():
			return
		}
	}
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
					Metadata: aggregationv1alpha1.ResourceMetadata{
						Name:      acs.Name,
						Namespace: acs.Namespace,
						UID:       string(acs.UID),
					},
					Spec:   acs.Spec,
					Status: acs.Status,
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
					Metadata: aggregationv1alpha1.ResourceMetadata{
						Name:      gcs.Name,
						Namespace: gcs.Namespace,
						UID:       string(gcs.UID),
					},
					Spec:   gcs.Spec,
					Status: gcs.Status,
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
					Metadata: aggregationv1alpha1.ResourceMetadata{
						Name:      tcs.Name,
						Namespace: tcs.Namespace,
						UID:       string(tcs.UID),
					},
					Spec:   tcs.Spec,
					Status: tcs.Status,
				})
			}
		}
	}

	// Aggregate low-level CommitStatus resources by looking up SHAs from the PromotionStrategy status.
	// CommitStatus resources are looked up by their spec.sha field, matching the hydrated SHAs
	// from each environment's active and proposed states.
	if ps.Status.Environments != nil {
		// Collect unique SHAs from all environments
		shas := make(map[string]struct{})
		for _, env := range ps.Status.Environments {
			if env.Active.Hydrated.Sha != "" {
				shas[env.Active.Hydrated.Sha] = struct{}{}
			}
			if env.Proposed.Hydrated.Sha != "" {
				shas[env.Proposed.Hydrated.Sha] = struct{}{}
			}
		}

		// Track which CommitStatuses we've already added to avoid duplicates
		seen := make(map[string]struct{})

		// List all CommitStatuses in the namespace and filter by SHA in-memory.
		// This approach works regardless of whether field indexing is available.
		csList := &promoterv1alpha1.CommitStatusList{}
		if err := r.client.List(ctx, csList, client.InNamespace(ps.Namespace)); err == nil {
			for _, cs := range csList.Items {
				// Check if this CommitStatus's SHA matches any of our target SHAs
				if _, matches := shas[cs.Spec.Sha]; !matches {
					continue
				}

				// Skip if we've already added this CommitStatus
				key := cs.Namespace + "/" + cs.Name
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}

				view.Aggregated.CommitStatuses.CommitStatuses = append(view.Aggregated.CommitStatuses.CommitStatuses, aggregationv1alpha1.CommitStatusRef{
					Metadata: aggregationv1alpha1.ResourceMetadata{
						Name:            cs.Name,
						Namespace:       cs.Namespace,
						UID:             string(cs.UID),
						OwnerReferences: cs.OwnerReferences,
					},
					Spec:   cs.Spec,
					Status: cs.Status,
				})
			}
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
