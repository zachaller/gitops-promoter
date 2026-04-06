package controller

import (
	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	acv1alpha1 "github.com/argoproj-labs/gitops-promoter/applyconfiguration/api/v1alpha1"
)

func commitShaStateToApplyConfig(s *promoterv1alpha1.CommitShaState) *acv1alpha1.CommitShaStateApplyConfiguration {
	ac := acv1alpha1.CommitShaState().
		WithRepoURL(s.RepoURL).
		WithAuthor(s.Author).
		WithSubject(s.Subject).
		WithBody(s.Body)
	if s.Sha != "" {
		ac.WithSha(s.Sha)
	}
	if !s.CommitTime.IsZero() {
		ac.WithCommitTime(s.CommitTime)
	}
	for i := range s.References {
		ac.WithReferences(revisionReferenceToApplyConfig(&s.References[i]))
	}
	return ac
}

func revisionReferenceToApplyConfig(r *promoterv1alpha1.RevisionReference) *acv1alpha1.RevisionReferenceApplyConfiguration {
	ac := acv1alpha1.RevisionReference()
	if r.Commit != nil {
		cm := acv1alpha1.CommitMetadata().
			WithAuthor(r.Commit.Author).
			WithSubject(r.Commit.Subject).
			WithBody(r.Commit.Body).
			WithSha(r.Commit.Sha).
			WithRepoURL(r.Commit.RepoURL)
		cm.Date = r.Commit.Date
		ac.WithCommit(cm)
	}
	return ac
}

func hydratorMetadataToApplyConfig(n *promoterv1alpha1.HydratorMetadata) *acv1alpha1.HydratorMetadataApplyConfiguration {
	ac := acv1alpha1.HydratorMetadata().
		WithRepoURL(n.RepoURL).
		WithAuthor(n.Author).
		WithSubject(n.Subject).
		WithBody(n.Body)
	if n.DrySha != "" {
		ac.WithDrySha(n.DrySha)
	}
	if !n.Date.IsZero() {
		ac.WithDate(n.Date)
	}
	for i := range n.References {
		ac.WithReferences(revisionReferenceToApplyConfig(&n.References[i]))
	}
	return ac
}

func commitBranchStateToApplyConfig(s *promoterv1alpha1.CommitBranchState) *acv1alpha1.CommitBranchStateApplyConfiguration {
	ac := acv1alpha1.CommitBranchState().
		WithDry(commitShaStateToApplyConfig(&s.Dry)).
		WithHydrated(commitShaStateToApplyConfig(&s.Hydrated))
	if s.Note != nil {
		ac.WithNote(hydratorMetadataToApplyConfig(s.Note))
	}
	for i := range s.CommitStatuses {
		ac.WithCommitStatuses(changeRequestPolicyCommitStatusPhaseToApplyConfig(&s.CommitStatuses[i]))
	}
	return ac
}

func changeRequestPolicyCommitStatusPhaseToApplyConfig(cs *promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase) *acv1alpha1.ChangeRequestPolicyCommitStatusPhaseApplyConfiguration {
	return acv1alpha1.ChangeRequestPolicyCommitStatusPhase().
		WithKey(cs.Key).
		WithPhase(cs.Phase).
		WithUrl(cs.Url).
		WithDescription(cs.Description)
}

func pullRequestCommonStatusToApplyConfig(pr *promoterv1alpha1.PullRequestCommonStatus) *acv1alpha1.PullRequestCommonStatusApplyConfiguration {
	ac := acv1alpha1.PullRequestCommonStatus().
		WithID(pr.ID).
		WithUrl(pr.Url)
	if pr.State != "" {
		ac.WithState(pr.State)
	}
	if !pr.PRCreationTime.IsZero() {
		ac.WithPRCreationTime(pr.PRCreationTime)
	}
	if !pr.PRMergeTime.IsZero() {
		ac.WithPRMergeTime(pr.PRMergeTime)
	}
	ac.ExternallyMergedOrClosed = pr.ExternallyMergedOrClosed
	return ac
}

func historyToApplyConfig(h *promoterv1alpha1.History) *acv1alpha1.HistoryApplyConfiguration {
	ac := acv1alpha1.History().
		WithActive(commitBranchStateToApplyConfig(&h.Active))
	proposed := acv1alpha1.CommitBranchStateHistoryProposed().
		WithHydrated(commitShaStateToApplyConfig(&h.Proposed.Hydrated))
	for i := range h.Proposed.CommitStatuses {
		proposed.WithCommitStatuses(changeRequestPolicyCommitStatusPhaseToApplyConfig(&h.Proposed.CommitStatuses[i]))
	}
	ac.WithProposed(proposed)
	if h.PullRequest != nil {
		ac.WithPullRequest(pullRequestCommonStatusToApplyConfig(h.PullRequest))
	}
	return ac
}
