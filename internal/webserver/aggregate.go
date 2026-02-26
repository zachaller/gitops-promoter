package webserver

import (
	"context"
	"fmt"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PromotionStrategyAggregate bundles a PromotionStrategy with all its related child resources.
type PromotionStrategyAggregate struct {
	PromotionStrategy      promoterv1alpha1.PromotionStrategy      `json:"promotionStrategy"`
	ChangeTransferPolicies []promoterv1alpha1.ChangeTransferPolicy `json:"changeTransferPolicies"`
	PullRequests           []promoterv1alpha1.PullRequest          `json:"pullRequests"`
	CommitStatuses         []promoterv1alpha1.CommitStatus         `json:"commitStatuses"`
}

// buildAggregate fetches a PromotionStrategy and all related child resources, returning them as a single aggregate.
func (ws *WebServer) buildAggregate(ctx context.Context, namespace, name string) (*PromotionStrategyAggregate, error) {
	// 1. Get the PromotionStrategy by namespace/name
	ps := &promoterv1alpha1.PromotionStrategy{}
	if err := ws.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, ps); err != nil {
		return nil, fmt.Errorf("failed to get PromotionStrategy %s/%s: %w", namespace, name, err)
	}

	safeLabel := utils.KubeSafeLabel(name)

	// 2. List ChangeTransferPolicies with matching PromotionStrategy label
	ctpList := &promoterv1alpha1.ChangeTransferPolicyList{}
	if err := ws.List(ctx, ctpList,
		client.InNamespace(namespace),
		client.MatchingLabels{promoterv1alpha1.PromotionStrategyLabel: safeLabel},
	); err != nil {
		return nil, fmt.Errorf("failed to list ChangeTransferPolicies: %w", err)
	}

	// 3. List PullRequests with matching PromotionStrategy label
	prList := &promoterv1alpha1.PullRequestList{}
	if err := ws.List(ctx, prList,
		client.InNamespace(namespace),
		client.MatchingLabels{promoterv1alpha1.PromotionStrategyLabel: safeLabel},
	); err != nil {
		return nil, fmt.Errorf("failed to list PullRequests: %w", err)
	}

	// 4. List CommitStatuses with the "previous environment" label, then filter by CTP owner reference
	csList := &promoterv1alpha1.CommitStatusList{}
	if err := ws.List(ctx, csList,
		client.InNamespace(namespace),
		client.MatchingLabels{promoterv1alpha1.CommitStatusLabel: promoterv1alpha1.PreviousEnvironmentCommitStatusKey},
	); err != nil {
		return nil, fmt.Errorf("failed to list CommitStatuses: %w", err)
	}

	// Build a set of CTP UIDs for fast lookup
	ctpUIDs := make(map[types.UID]struct{}, len(ctpList.Items))
	for i := range ctpList.Items {
		ctpUIDs[ctpList.Items[i].UID] = struct{}{}
	}

	// Filter: keep only CommitStatuses whose OwnerReference UID matches a CTP
	filteredCS := []promoterv1alpha1.CommitStatus{}
	for i := range csList.Items {
		for _, ref := range csList.Items[i].OwnerReferences {
			if _, ok := ctpUIDs[ref.UID]; ok {
				filteredCS = append(filteredCS, csList.Items[i])
				break
			}
		}
	}

	// 5. Ensure non-nil empty slices so they marshal as [] not null
	ctps := ctpList.Items
	if ctps == nil {
		ctps = []promoterv1alpha1.ChangeTransferPolicy{}
	}
	prs := prList.Items
	if prs == nil {
		prs = []promoterv1alpha1.PullRequest{}
	}

	return &PromotionStrategyAggregate{
		PromotionStrategy:      *ps,
		ChangeTransferPolicies: ctps,
		PullRequests:           prs,
		CommitStatuses:         filteredCS,
	}, nil
}
