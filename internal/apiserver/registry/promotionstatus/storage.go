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

package promotionstatus

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/endpoints/request"
	genericrest "k8s.io/apiserver/pkg/registry/rest"
	restclient "k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/apiserver/apis/aggregated/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

// DefaultCacheTTL is the default time-to-live for cached PromotionStatus objects.
const DefaultCacheTTL = 5 * time.Second

// REST implements rest.Storage for PromotionStatus.
// It provides an aggregated view of PromotionStrategy and related resources.
type REST struct {
	client client.Client
	cache  *Cache
}

var (
	_ genericrest.Storage              = &REST{}
	_ genericrest.Getter               = &REST{}
	_ genericrest.Lister               = &REST{}
	_ genericrest.Scoper               = &REST{}
	_ genericrest.SingularNameProvider = &REST{}
)

// NewREST creates a new REST storage for PromotionStatus.
func NewREST(config *restclient.Config) *REST {
	return NewRESTWithCacheTTL(config, DefaultCacheTTL)
}

// NewRESTWithCacheTTL creates a new REST storage with a custom cache TTL.
func NewRESTWithCacheTTL(config *restclient.Config, cacheTTL time.Duration) *REST {
	scheme := utils.GetScheme()
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		panic(fmt.Errorf("failed to create client for PromotionStatus REST: %w", err))
	}
	return &REST{
		client: c,
		cache:  NewCache(cacheTTL),
	}
}

// New returns an empty PromotionStatus object.
func (r *REST) New() runtime.Object {
	return &v1alpha1.PromotionStatus{}
}

// Destroy cleans up resources on shutdown.
func (r *REST) Destroy() {
	// Nothing to clean up for in-memory storage
}

// NewList returns an empty PromotionStatusList object.
func (r *REST) NewList() runtime.Object {
	return &v1alpha1.PromotionStatusList{}
}

// NamespaceScoped returns true because PromotionStatus is namespaced.
func (r *REST) NamespaceScoped() bool {
	return true
}

// GetSingularName returns the singular name of the resource.
func (r *REST) GetSingularName() string {
	return "promotionstatus"
}

// Get retrieves the PromotionStatus for a given PromotionStrategy by name.
func (r *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	startTime := time.Now()
	namespace, ok := request.NamespaceFrom(ctx)
	if !ok {
		RecordRequest("get", "", "error", time.Since(startTime).Seconds())
		return nil, errors.NewBadRequest("namespace is required")
	}

	// Check cache first
	if cached := r.cache.Get(namespace, name); cached != nil {
		RecordCacheHit(namespace)
		RecordRequest("get", namespace, "success", time.Since(startTime).Seconds())
		return cached, nil
	}
	RecordCacheMiss(namespace)

	// Fetch the PromotionStrategy
	ps := &promoterv1alpha1.PromotionStrategy{}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, ps); err != nil {
		RecordRequest("get", namespace, "error", time.Since(startTime).Seconds())
		return nil, err
	}

	// Build the aggregated status
	aggregationStart := time.Now()
	status, err := r.buildPromotionStatus(ctx, ps)
	RecordAggregationDuration(namespace, name, time.Since(aggregationStart).Seconds())
	if err != nil {
		RecordRequest("get", namespace, "error", time.Since(startTime).Seconds())
		return nil, err
	}

	// Cache the result
	r.cache.Set(status)
	RecordCacheSize(r.cache.Size())

	RecordRequest("get", namespace, "success", time.Since(startTime).Seconds())
	return status, nil
}

// List returns a list of PromotionStatus objects for all PromotionStrategies in the namespace.
func (r *REST) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	startTime := time.Now()
	namespace, ok := request.NamespaceFrom(ctx)
	if !ok {
		namespace = metav1.NamespaceAll
	}

	// Parse label and field selectors
	labelSelector := labels.Everything()
	fieldSelector := fields.Everything()

	if options != nil {
		if options.LabelSelector != nil {
			labelSelector = options.LabelSelector
		}
		if options.FieldSelector != nil {
			fieldSelector = options.FieldSelector
		}
	}

	// List all PromotionStrategies
	psList := &promoterv1alpha1.PromotionStrategyList{}
	listOpts := []client.ListOption{}
	if namespace != metav1.NamespaceAll {
		listOpts = append(listOpts, client.InNamespace(namespace))
	}

	// Apply label selector to the underlying PromotionStrategy list
	if !labelSelector.Empty() {
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: labelSelector})
	}

	if err := r.client.List(ctx, psList, listOpts...); err != nil {
		RecordRequest("list", namespace, "error", time.Since(startTime).Seconds())
		return nil, err
	}

	// Build aggregated status for each PromotionStrategy
	result := &v1alpha1.PromotionStatusList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PromotionStatusList",
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
		},
	}

	for i := range psList.Items {
		ps := &psList.Items[i]

		// Check cache first for each item
		if cached := r.cache.Get(ps.Namespace, ps.Name); cached != nil {
			RecordCacheHit(ps.Namespace)
			if matchesFieldSelector(cached, fieldSelector) {
				result.Items = append(result.Items, *cached)
			}
			continue
		}
		RecordCacheMiss(ps.Namespace)

		aggregationStart := time.Now()
		status, err := r.buildPromotionStatus(ctx, ps)
		RecordAggregationDuration(ps.Namespace, ps.Name, time.Since(aggregationStart).Seconds())
		if err != nil {
			// Log error but continue with other items
			continue
		}

		// Cache the result
		r.cache.Set(status)

		// Apply field selector filtering
		if !matchesFieldSelector(status, fieldSelector) {
			continue
		}

		result.Items = append(result.Items, *status)
	}

	RecordCacheSize(r.cache.Size())
	RecordRequest("list", namespace, "success", time.Since(startTime).Seconds())
	return result, nil
}

// matchesFieldSelector checks if the PromotionStatus matches the field selector.
func matchesFieldSelector(status *v1alpha1.PromotionStatus, selector fields.Selector) bool {
	if selector.Empty() {
		return true
	}

	// Build a fields.Set from the PromotionStatus
	fieldSet := fields.Set{
		"metadata.name":      status.Name,
		"metadata.namespace": status.Namespace,
	}

	// Add spec fields
	if status.Spec.PromotionStrategyRef.Name != "" {
		fieldSet["spec.promotionStrategyRef.name"] = status.Spec.PromotionStrategyRef.Name
	}

	return selector.Matches(fieldSet)
}

// ConvertToTable implements the TableConvertor interface for kubectl get output.
func (r *REST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return genericrest.NewDefaultTableConvertor(v1alpha1.Resource("promotionstatuses")).ConvertToTable(ctx, object, tableOptions)
}

// buildPromotionStatus builds an aggregated PromotionStatus from a PromotionStrategy.
func (r *REST) buildPromotionStatus(ctx context.Context, ps *promoterv1alpha1.PromotionStrategy) (*v1alpha1.PromotionStatus, error) {
	status := &v1alpha1.PromotionStatus{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PromotionStatus",
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
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
		Spec: v1alpha1.PromotionStatusSpec{
			PromotionStrategyRef: v1alpha1.ObjectReference{
				Name:      ps.Name,
				Namespace: ps.Namespace,
			},
		},
		Status: v1alpha1.PromotionStatusStatus{
			Conditions: ps.Status.Conditions,
		},
	}

	// Aggregate environment statuses
	for _, envStatus := range ps.Status.Environments {
		aggEnvStatus := v1alpha1.AggregatedEnvironmentStatus{
			Branch: envStatus.Branch,
			ActiveCommit: v1alpha1.CommitInfo{
				DrySha:      envStatus.Active.Dry.Sha,
				HydratedSha: envStatus.Active.Hydrated.Sha,
			},
		}

		// Add proposed commit info if available
		if envStatus.Proposed.Dry.Sha != "" {
			aggEnvStatus.ProposedCommit = &v1alpha1.CommitInfo{
				DrySha:      envStatus.Proposed.Dry.Sha,
				HydratedSha: envStatus.Proposed.Hydrated.Sha,
			}
		}

		// Add pull request info if available
		if envStatus.PullRequest != nil {
			aggEnvStatus.PullRequest = &v1alpha1.PullRequestInfo{
				ID:    envStatus.PullRequest.ID,
				State: string(envStatus.PullRequest.State),
				URL:   envStatus.PullRequest.Url,
			}
		}

		// Fetch related CommitStatuses for this environment
		commitStatuses, err := r.fetchCommitStatuses(ctx, ps.Namespace, ps.Name, envStatus.Branch)
		if err == nil {
			aggEnvStatus.CommitStatuses = commitStatuses
		}

		// Fetch related ChangeTransferPolicies
		ctps, err := r.fetchChangeTransferPolicies(ctx, ps.Namespace, ps.Name, envStatus.Branch)
		if err == nil {
			aggEnvStatus.ChangeTransferPolicies = ctps
		}

		status.Status.Environments = append(status.Status.Environments, aggEnvStatus)
	}

	return status, nil
}

// fetchCommitStatuses fetches all CommitStatus resources related to a PromotionStrategy environment.
func (r *REST) fetchCommitStatuses(ctx context.Context, namespace, _, branch string) ([]v1alpha1.CommitStatusInfo, error) {
	var result []v1alpha1.CommitStatusInfo

	// Fetch regular CommitStatuses
	csList := &promoterv1alpha1.CommitStatusList{}
	if err := r.client.List(ctx, csList, client.InNamespace(namespace)); err != nil {
		RecordResourceFetchError("CommitStatus", namespace)
		return nil, err
	}

	for _, cs := range csList.Items {
		// Filter by labels if they match the PromotionStrategy
		if cs.Labels != nil {
			if csKey, ok := cs.Labels["promoter.argoproj.io/commit-status"]; ok {
				result = append(result, v1alpha1.CommitStatusInfo{
					Name:        cs.Name,
					Key:         csKey,
					Phase:       string(cs.Status.Phase),
					Description: cs.Spec.Description,
					URL:         cs.Spec.Url,
					Source: v1alpha1.CommitStatusSource{
						Type: "CommitStatus",
						Name: cs.Name,
					},
				})
			}
		}
	}

	// Fetch ArgoCDCommitStatuses
	argoCSList := &promoterv1alpha1.ArgoCDCommitStatusList{}
	if err := r.client.List(ctx, argoCSList, client.InNamespace(namespace)); err != nil {
		RecordResourceFetchError("ArgoCDCommitStatus", namespace)
	} else {
		for _, acs := range argoCSList.Items {
			// ArgoCDCommitStatus doesn't have a single phase - it has phases per application
			// For now, we just indicate it exists
			result = append(result, v1alpha1.CommitStatusInfo{
				Name: acs.Name,
				Source: v1alpha1.CommitStatusSource{
					Type: "ArgoCDCommitStatus",
					Name: acs.Name,
				},
			})
		}
	}

	// Fetch TimedCommitStatuses
	timedCSList := &promoterv1alpha1.TimedCommitStatusList{}
	if err := r.client.List(ctx, timedCSList, client.InNamespace(namespace)); err != nil {
		RecordResourceFetchError("TimedCommitStatus", namespace)
	} else {
		for _, tcs := range timedCSList.Items {
			// Find the phase for this branch from the environments status
			var phase string
			for _, envStatus := range tcs.Status.Environments {
				if envStatus.Branch == branch {
					phase = envStatus.Phase
					break
				}
			}
			result = append(result, v1alpha1.CommitStatusInfo{
				Name:  tcs.Name,
				Phase: phase,
				Source: v1alpha1.CommitStatusSource{
					Type: "TimedCommitStatus",
					Name: tcs.Name,
				},
			})
		}
	}

	return result, nil
}

// fetchChangeTransferPolicies fetches all ChangeTransferPolicy resources related to a PromotionStrategy.
func (r *REST) fetchChangeTransferPolicies(ctx context.Context, namespace, psName, branch string) ([]v1alpha1.ChangeTransferPolicyInfo, error) {
	var result []v1alpha1.ChangeTransferPolicyInfo

	ctpList := &promoterv1alpha1.ChangeTransferPolicyList{}
	if err := r.client.List(ctx, ctpList, client.InNamespace(namespace)); err != nil {
		RecordResourceFetchError("ChangeTransferPolicy", namespace)
		return nil, err
	}

	for _, ctp := range ctpList.Items {
		// Check if this CTP is related to the PromotionStrategy by checking owner references
		// or by matching the active branch
		isRelated := false
		for _, ownerRef := range ctp.OwnerReferences {
			if ownerRef.Kind == "PromotionStrategy" && ownerRef.Name == psName {
				isRelated = true
				break
			}
		}

		// Also check if the active branch matches
		if ctp.Spec.ActiveBranch == branch {
			isRelated = true
		}

		if isRelated {
			info := v1alpha1.ChangeTransferPolicyInfo{
				Name: ctp.Name,
			}

			for _, acs := range ctp.Spec.ActiveCommitStatuses {
				info.ActiveCommitStatuses = append(info.ActiveCommitStatuses, acs.Key)
			}

			for _, pcs := range ctp.Spec.ProposedCommitStatuses {
				info.ProposedCommitStatuses = append(info.ProposedCommitStatuses, pcs.Key)
			}

			result = append(result, info)
		}
	}

	return result, nil
}
