package utils

import (
	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	acv1alpha1 "github.com/argoproj-labs/gitops-promoter/applyconfiguration/api/v1alpha1"
)

// CommitStatusStatusToApplyConfig converts a CommitStatusStatus to its apply configuration.
// Conditions are NOT copied -- HandleSSAReconciliationResult manages them.
func CommitStatusStatusToApplyConfig(s promoterv1alpha1.CommitStatusStatus) *acv1alpha1.CommitStatusStatusApplyConfiguration {
	ac := acv1alpha1.CommitStatusStatus()
	if s.Id != "" {
		ac.WithId(s.Id)
	}
	if s.Sha != "" {
		ac.WithSha(s.Sha)
	}
	if s.Phase != "" {
		ac.WithPhase(s.Phase)
	}
	return ac
}

// PullRequestStatusToApplyConfig converts a PullRequestStatus to its apply configuration.
// Conditions are NOT copied -- HandleSSAReconciliationResult manages them.
func PullRequestStatusToApplyConfig(s promoterv1alpha1.PullRequestStatus) *acv1alpha1.PullRequestStatusApplyConfiguration {
	ac := acv1alpha1.PullRequestStatus()
	if s.ID != "" {
		ac.WithID(s.ID)
	}
	if s.State != "" {
		ac.WithState(s.State)
	}
	if !s.PRCreationTime.IsZero() {
		ac.WithPRCreationTime(s.PRCreationTime)
	}
	if s.Url != "" {
		ac.WithUrl(s.Url)
	}
	if s.ExternallyMergedOrClosed != nil {
		ac.WithExternallyMergedOrClosed(*s.ExternallyMergedOrClosed)
	}
	return ac
}

// ArgoCDCommitStatusStatusToApplyConfig converts an ArgoCDCommitStatusStatus to its apply configuration.
// Conditions are NOT copied -- HandleSSAReconciliationResult manages them.
func ArgoCDCommitStatusStatusToApplyConfig(s promoterv1alpha1.ArgoCDCommitStatusStatus) *acv1alpha1.ArgoCDCommitStatusStatusApplyConfiguration {
	ac := acv1alpha1.ArgoCDCommitStatusStatus()
	for i := range s.ApplicationsSelected {
		ac.WithApplicationsSelected(applicationsSelectedToApplyConfig(s.ApplicationsSelected[i]))
	}
	return ac
}

func applicationsSelectedToApplyConfig(a promoterv1alpha1.ApplicationsSelected) *acv1alpha1.ApplicationsSelectedApplyConfiguration {
	ac := acv1alpha1.ApplicationsSelected().
		WithNamespace(a.Namespace).
		WithName(a.Name).
		WithPhase(a.Phase).
		WithSha(a.Sha).
		WithEnvironment(a.Environment).
		WithClusterName(a.ClusterName)
	if a.LastTransitionTime != nil {
		ac.WithLastTransitionTime(*a.LastTransitionTime)
	}
	return ac
}

// GitCommitStatusStatusToApplyConfig converts a GitCommitStatusStatus to its apply configuration.
// Conditions are NOT copied -- HandleSSAReconciliationResult manages them.
func GitCommitStatusStatusToApplyConfig(s promoterv1alpha1.GitCommitStatusStatus) *acv1alpha1.GitCommitStatusStatusApplyConfiguration {
	ac := acv1alpha1.GitCommitStatusStatus()
	for i := range s.Environments {
		ac.WithEnvironments(gitCommitStatusEnvironmentStatusToApplyConfig(s.Environments[i]))
	}
	return ac
}

func gitCommitStatusEnvironmentStatusToApplyConfig(e promoterv1alpha1.GitCommitStatusEnvironmentStatus) *acv1alpha1.GitCommitStatusEnvironmentStatusApplyConfiguration {
	ac := acv1alpha1.GitCommitStatusEnvironmentStatus().
		WithBranch(e.Branch).
		WithProposedHydratedSha(e.ProposedHydratedSha).
		WithPhase(e.Phase)
	if e.ActiveHydratedSha != "" {
		ac.WithActiveHydratedSha(e.ActiveHydratedSha)
	}
	if e.TargetedSha != "" {
		ac.WithTargetedSha(e.TargetedSha)
	}
	if e.ExpressionResult != nil {
		ac.WithExpressionResult(*e.ExpressionResult)
	}
	return ac
}

// TimedCommitStatusStatusToApplyConfig converts a TimedCommitStatusStatus to its apply configuration.
// Conditions are NOT copied -- HandleSSAReconciliationResult manages them.
func TimedCommitStatusStatusToApplyConfig(s promoterv1alpha1.TimedCommitStatusStatus) *acv1alpha1.TimedCommitStatusStatusApplyConfiguration {
	ac := acv1alpha1.TimedCommitStatusStatus()
	for i := range s.Environments {
		ac.WithEnvironments(timedCommitStatusEnvironmentsStatusToApplyConfig(s.Environments[i]))
	}
	return ac
}

func timedCommitStatusEnvironmentsStatusToApplyConfig(e promoterv1alpha1.TimedCommitStatusEnvironmentsStatus) *acv1alpha1.TimedCommitStatusEnvironmentsStatusApplyConfiguration {
	return acv1alpha1.TimedCommitStatusEnvironmentsStatus().
		WithBranch(e.Branch).
		WithSha(e.Sha).
		WithCommitTime(e.CommitTime).
		WithRequiredDuration(e.RequiredDuration).
		WithPhase(e.Phase).
		WithAtMostDurationRemaining(e.AtMostDurationRemaining)
}

// WebRequestCommitStatusStatusToApplyConfig converts a WebRequestCommitStatusStatus to its apply configuration.
// Conditions are NOT copied -- HandleSSAReconciliationResult manages them.
func WebRequestCommitStatusStatusToApplyConfig(s promoterv1alpha1.WebRequestCommitStatusStatus) *acv1alpha1.WebRequestCommitStatusStatusApplyConfiguration {
	ac := acv1alpha1.WebRequestCommitStatusStatus()
	for i := range s.Environments {
		ac.WithEnvironments(webRequestCommitStatusEnvironmentStatusToApplyConfig(s.Environments[i]))
	}
	if s.PromotionStrategyContext != nil {
		ac.WithPromotionStrategyContext(webRequestCommitStatusPSContextToApplyConfig(*s.PromotionStrategyContext))
	}
	return ac
}

func webRequestCommitStatusEnvironmentStatusToApplyConfig(e promoterv1alpha1.WebRequestCommitStatusEnvironmentStatus) *acv1alpha1.WebRequestCommitStatusEnvironmentStatusApplyConfiguration {
	ac := acv1alpha1.WebRequestCommitStatusEnvironmentStatus().
		WithBranch(e.Branch).
		WithPhase(e.Phase)
	if e.ReportedSha != "" {
		ac.WithReportedSha(e.ReportedSha)
	}
	if e.LastSuccessfulSha != "" {
		ac.WithLastSuccessfulSha(e.LastSuccessfulSha)
	}
	if e.LastRequestTime != nil {
		ac.WithLastRequestTime(*e.LastRequestTime)
	}
	if e.LastResponseStatusCode != nil {
		ac.WithLastResponseStatusCode(*e.LastResponseStatusCode)
	}
	if e.TriggerOutput != nil {
		ac.WithTriggerOutput(*e.TriggerOutput)
	}
	if e.ResponseOutput != nil {
		ac.WithResponseOutput(*e.ResponseOutput)
	}
	if e.SuccessOutput != nil {
		ac.WithSuccessOutput(*e.SuccessOutput)
	}
	return ac
}

func webRequestCommitStatusPSContextToApplyConfig(c promoterv1alpha1.WebRequestCommitStatusPromotionStrategyContextStatus) *acv1alpha1.WebRequestCommitStatusPromotionStrategyContextStatusApplyConfiguration {
	ac := acv1alpha1.WebRequestCommitStatusPromotionStrategyContextStatus()
	for i := range c.PhasePerBranch {
		ac.WithPhasePerBranch(acv1alpha1.WebRequestCommitStatusPhasePerBranchItem().
			WithBranch(c.PhasePerBranch[i].Branch).
			WithPhase(c.PhasePerBranch[i].Phase))
	}
	if c.LastRequestTime != nil {
		ac.WithLastRequestTime(*c.LastRequestTime)
	}
	if c.LastResponseStatusCode != nil {
		ac.WithLastResponseStatusCode(*c.LastResponseStatusCode)
	}
	if c.TriggerOutput != nil {
		ac.WithTriggerOutput(*c.TriggerOutput)
	}
	if c.ResponseOutput != nil {
		ac.WithResponseOutput(*c.ResponseOutput)
	}
	if c.SuccessOutput != nil {
		ac.WithSuccessOutput(*c.SuccessOutput)
	}
	for i := range c.LastSuccessfulShas {
		ac.WithLastSuccessfulShas(acv1alpha1.WebRequestCommitStatusLastSuccessfulShaItem().
			WithBranch(c.LastSuccessfulShas[i].Branch).
			WithLastSuccessfulSha(c.LastSuccessfulShas[i].LastSuccessfulSha))
	}
	return ac
}

// ChangeTransferPolicyStatusToApplyConfig converts a ChangeTransferPolicyStatus to its apply configuration.
// Conditions are NOT copied -- HandleSSAReconciliationResult manages them.
func ChangeTransferPolicyStatusToApplyConfig(s promoterv1alpha1.ChangeTransferPolicyStatus) *acv1alpha1.ChangeTransferPolicyStatusApplyConfiguration {
	ac := acv1alpha1.ChangeTransferPolicyStatus().
		WithProposed(commitBranchStateToApplyConfig(s.Proposed)).
		WithActive(commitBranchStateToApplyConfig(s.Active))
	if s.PullRequest != nil {
		ac.WithPullRequest(PullRequestCommonStatusToApplyConfig(*s.PullRequest))
	}
	for i := range s.History {
		ac.WithHistory(historyToApplyConfig(s.History[i]))
	}
	return ac
}

// PromotionStrategyStatusToApplyConfig converts a PromotionStrategyStatus to its apply configuration.
// Conditions are NOT copied -- HandleSSAReconciliationResult manages them.
func PromotionStrategyStatusToApplyConfig(s promoterv1alpha1.PromotionStrategyStatus) *acv1alpha1.PromotionStrategyStatusApplyConfiguration {
	ac := acv1alpha1.PromotionStrategyStatus()
	for i := range s.Environments {
		ac.WithEnvironments(EnvironmentStatusToApplyConfig(s.Environments[i]))
	}
	return ac
}

// EnvironmentStatusToApplyConfig converts an EnvironmentStatus to its apply configuration.
func EnvironmentStatusToApplyConfig(e promoterv1alpha1.EnvironmentStatus) *acv1alpha1.EnvironmentStatusApplyConfiguration {
	ac := acv1alpha1.EnvironmentStatus().
		WithBranch(e.Branch).
		WithProposed(commitBranchStateToApplyConfig(e.Proposed)).
		WithActive(commitBranchStateToApplyConfig(e.Active))
	if e.PullRequest != nil {
		ac.WithPullRequest(PullRequestCommonStatusToApplyConfig(*e.PullRequest))
	}
	for i := range e.LastHealthyDryShas {
		ac.WithLastHealthyDryShas(acv1alpha1.HealthyDryShas().
			WithSha(e.LastHealthyDryShas[i].Sha).
			WithTime(e.LastHealthyDryShas[i].Time))
	}
	for i := range e.History {
		ac.WithHistory(historyToApplyConfig(e.History[i]))
	}
	return ac
}

func commitBranchStateToApplyConfig(s promoterv1alpha1.CommitBranchState) *acv1alpha1.CommitBranchStateApplyConfiguration {
	ac := acv1alpha1.CommitBranchState().
		WithDry(commitShaStateToApplyConfig(s.Dry)).
		WithHydrated(commitShaStateToApplyConfig(s.Hydrated))
	if s.Note != nil {
		ac.WithNote(hydratorMetadataToApplyConfig(*s.Note))
	}
	for i := range s.CommitStatuses {
		ac.WithCommitStatuses(changeRequestPolicyCommitStatusPhaseToApplyConfig(s.CommitStatuses[i]))
	}
	return ac
}

func commitShaStateToApplyConfig(s promoterv1alpha1.CommitShaState) *acv1alpha1.CommitShaStateApplyConfiguration {
	ac := acv1alpha1.CommitShaState()
	if s.Sha != "" {
		ac.WithSha(s.Sha)
	}
	if !s.CommitTime.IsZero() {
		ac.WithCommitTime(s.CommitTime)
	}
	if s.RepoURL != "" {
		ac.WithRepoURL(s.RepoURL)
	}
	if s.Author != "" {
		ac.WithAuthor(s.Author)
	}
	if s.Subject != "" {
		ac.WithSubject(s.Subject)
	}
	if s.Body != "" {
		ac.WithBody(s.Body)
	}
	for i := range s.References {
		ac.WithReferences(revisionReferenceToApplyConfig(s.References[i]))
	}
	return ac
}

func hydratorMetadataToApplyConfig(h promoterv1alpha1.HydratorMetadata) *acv1alpha1.HydratorMetadataApplyConfiguration {
	ac := acv1alpha1.HydratorMetadata()
	if h.RepoURL != "" {
		ac.WithRepoURL(h.RepoURL)
	}
	if h.DrySha != "" {
		ac.WithDrySha(h.DrySha)
	}
	if h.Author != "" {
		ac.WithAuthor(h.Author)
	}
	if !h.Date.IsZero() {
		ac.WithDate(h.Date)
	}
	if h.Subject != "" {
		ac.WithSubject(h.Subject)
	}
	if h.Body != "" {
		ac.WithBody(h.Body)
	}
	for i := range h.References {
		ac.WithReferences(revisionReferenceToApplyConfig(h.References[i]))
	}
	return ac
}

func revisionReferenceToApplyConfig(r promoterv1alpha1.RevisionReference) *acv1alpha1.RevisionReferenceApplyConfiguration {
	ac := acv1alpha1.RevisionReference()
	if r.Commit != nil {
		ac.WithCommit(commitMetadataToApplyConfig(*r.Commit))
	}
	return ac
}

func commitMetadataToApplyConfig(c promoterv1alpha1.CommitMetadata) *acv1alpha1.CommitMetadataApplyConfiguration {
	ac := acv1alpha1.CommitMetadata()
	if c.Author != "" {
		ac.WithAuthor(c.Author)
	}
	if c.Date != nil {
		ac.WithDate(*c.Date)
	}
	if c.Subject != "" {
		ac.WithSubject(c.Subject)
	}
	if c.Body != "" {
		ac.WithBody(c.Body)
	}
	if c.Sha != "" {
		ac.WithSha(c.Sha)
	}
	if c.RepoURL != "" {
		ac.WithRepoURL(c.RepoURL)
	}
	return ac
}

// PullRequestCommonStatusToApplyConfig converts a PullRequestCommonStatus to its apply configuration.
func PullRequestCommonStatusToApplyConfig(s promoterv1alpha1.PullRequestCommonStatus) *acv1alpha1.PullRequestCommonStatusApplyConfiguration {
	ac := acv1alpha1.PullRequestCommonStatus()
	if s.ID != "" {
		ac.WithID(s.ID)
	}
	if s.State != "" {
		ac.WithState(s.State)
	}
	if !s.PRCreationTime.IsZero() {
		ac.WithPRCreationTime(s.PRCreationTime)
	}
	if !s.PRMergeTime.IsZero() {
		ac.WithPRMergeTime(s.PRMergeTime)
	}
	if s.Url != "" {
		ac.WithUrl(s.Url)
	}
	if s.ExternallyMergedOrClosed != nil {
		ac.WithExternallyMergedOrClosed(*s.ExternallyMergedOrClosed)
	}
	return ac
}

func historyToApplyConfig(h promoterv1alpha1.History) *acv1alpha1.HistoryApplyConfiguration {
	ac := acv1alpha1.History().
		WithProposed(commitBranchStateHistoryProposedToApplyConfig(h.Proposed)).
		WithActive(commitBranchStateToApplyConfig(h.Active))
	if h.PullRequest != nil {
		ac.WithPullRequest(PullRequestCommonStatusToApplyConfig(*h.PullRequest))
	}
	return ac
}

func commitBranchStateHistoryProposedToApplyConfig(s promoterv1alpha1.CommitBranchStateHistoryProposed) *acv1alpha1.CommitBranchStateHistoryProposedApplyConfiguration {
	ac := acv1alpha1.CommitBranchStateHistoryProposed().
		WithHydrated(commitShaStateToApplyConfig(s.Hydrated))
	for i := range s.CommitStatuses {
		ac.WithCommitStatuses(changeRequestPolicyCommitStatusPhaseToApplyConfig(s.CommitStatuses[i]))
	}
	return ac
}

func changeRequestPolicyCommitStatusPhaseToApplyConfig(c promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase) *acv1alpha1.ChangeRequestPolicyCommitStatusPhaseApplyConfiguration {
	ac := acv1alpha1.ChangeRequestPolicyCommitStatusPhase().
		WithKey(c.Key).
		WithPhase(c.Phase)
	if c.Url != "" {
		ac.WithUrl(c.Url)
	}
	if c.Description != "" {
		ac.WithDescription(c.Description)
	}
	return ac
}
