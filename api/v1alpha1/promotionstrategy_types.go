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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PromotionPolicy selects which hydrated change an environment promotes when several are available.
//
// The hydrator pushes a hydrated commit to every environment's proposed branch for each dry commit,
// so the proposed branch is a stream of promotion candidates. This policy decides which candidate in
// that stream an environment actually promotes.
// +kubebuilder:validation:Enum:=Latest;LatestVerified;Sequential
type PromotionPolicy string

const (
	// PromotionPolicyLatest always promotes the newest candidate on the proposed branch. Because the
	// newest candidate is also the one that preceding environments have had the least time to verify,
	// a repository with high dry-commit churn can leave later environments permanently blocked: every
	// time the preceding environment finishes verifying a change, a newer candidate has already
	// replaced it. This is the default and matches the behavior of releases before the policy existed.
	PromotionPolicyLatest PromotionPolicy = "Latest"

	// PromotionPolicyLatestVerified promotes the newest candidate whose dry commit every preceding
	// environment has verified, skipping over any newer but unverified candidates. Under churn this
	// keeps later environments moving: they advance to the high-water mark of verified change instead
	// of chasing the branch tip.
	PromotionPolicyLatestVerified PromotionPolicy = "LatestVerified"

	// PromotionPolicySequential promotes the oldest candidate that has not been promoted yet, provided
	// every preceding environment has verified it. Unlike LatestVerified it never skips a candidate, so
	// every dry commit is promoted through the environment in order. Use it when each change must be
	// observed individually; expect the environment to fall further behind under churn.
	PromotionPolicySequential PromotionPolicy = "Sequential"
)

// PromotionStrategySpec defines the desired state of PromotionStrategy
type PromotionStrategySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// RepositoryReference indicates what repository to promote commits in.
	// +kubebuilder:validation:Required
	RepositoryReference ObjectReference `json:"gitRepositoryRef"`

	// ActiveCommitStatuses are commit statuses describing an actively running dry commit. If an active commit status
	// is failing for an environment, subsequent environments will not deploy the failing commit.
	//
	// The commit statuses specified in this field apply to all environments in the promotion sequence. You can also
	// specify commit statuses for individual environments in the `environments` field.
	// +kubebuilder:validation:Optional
	// +listType:=map
	// +listMapKey=key
	ActiveCommitStatuses []CommitStatusSelector `json:"activeCommitStatuses,omitempty"`

	// ProposedCommitStatuses are commit statuses describing a proposed dry commit, i.e. one that is not yet running
	// in a live environment. If a proposed commit status is failing for a given environment, the dry commit will not
	// be promoted to that environment.
	//
	// The commit statuses specified in this field apply to all environments in the promotion sequence. You can also
	// specify commit statuses for individual environments in the `environments` field.
	// +kubebuilder:validation:Optional
	// +listType:=map
	// +listMapKey=key
	ProposedCommitStatuses []CommitStatusSelector `json:"proposedCommitStatuses,omitempty"`

	// Environments is the sequence of environments that a dry commit will be promoted through.
	// +kubebuilder:validation:MinItems:=1
	// +kubebuilder:validation:MaxItems:=1000
	// +listType:=map
	// +listMapKey=branch
	Environments []Environment `json:"environments"`

	// ActivePath is the default repository subpath for this strategy's active state.
	// When set, proposed branches are created as <environment-branch>-next/<activePath>.
	// Individual environments can override this value via their own activePath field.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	ActivePath string `json:"activePath,omitempty"`

	// PullRequest configures SCM pull request behavior for all environments in this strategy.
	// +kubebuilder:validation:Optional
	PullRequest *PullRequestPolicySpec `json:"pullRequest,omitempty"`

	// PromotionPolicy is the default candidate selection policy for all environments in this strategy.
	// Individual environments can override it via their own promotionPolicy field. Defaults to Latest.
	// +kubebuilder:validation:Optional
	PromotionPolicy PromotionPolicy `json:"promotionPolicy,omitempty"`
}

// Environment defines a single environment in the promotion sequence.
type Environment struct {
	// Branch is the name of the active branch for the environment.
	// Must not start with '-', contain ':', or contain '..'.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('-')",message="branch must not start with '-'"
	// +kubebuilder:validation:XValidation:rule="!self.contains(':')",message="branch must not contain ':'"
	// +kubebuilder:validation:XValidation:rule="!self.contains('..')",message="branch must not contain '..'"
	Branch string `json:"branch"`
	// AutoMerge determines whether the dry commit should be automatically merged into the next branch in the sequence.
	// If false, the dry commit will be proposed but not merged.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=true
	AutoMerge *bool `json:"autoMerge,omitempty"`
	// ActiveCommitStatuses are commit statuses describing an actively running dry commit. If an active commit status
	// is failing for an environment, subsequent environments will not deploy the failing commit.
	//
	// The commit statuses specified in this field apply to this environment only. You can also specify commit statuses
	// for all environments in the `spec.activeCommitStatuses` field.
	// +kubebuilder:validation:Optional
	// +listType:=map
	// +listMapKey=key
	ActiveCommitStatuses []CommitStatusSelector `json:"activeCommitStatuses,omitempty"`
	// ProposedCommitStatuses are commit statuses describing a proposed dry commit, i.e. one that is not yet running
	// in a live environment. If a proposed commit status is failing for a given environment, the dry commit will not
	// be promoted to that environment.
	//
	// The commit statuses specified in this field apply to this environment only. You can also specify commit statuses
	// for all environments in the `spec.proposedCommitStatuses` field.
	// +kubebuilder:validation:Optional
	// +listType:=map
	// +listMapKey=key
	ProposedCommitStatuses []CommitStatusSelector `json:"proposedCommitStatuses,omitempty"`

	// ActivePath optionally overrides the strategy-level activePath for this environment.
	// When set, this environment's CTP uses this path instead of spec.activePath.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	ActivePath string `json:"activePath,omitempty"`

	// PromotionPolicy optionally overrides the strategy-level promotionPolicy for this environment.
	// When empty, spec.promotionPolicy applies, which itself defaults to Latest.
	// +kubebuilder:validation:Optional
	PromotionPolicy PromotionPolicy `json:"promotionPolicy,omitempty"`
}

// GetPromotionPolicy resolves the effective promotion policy for this environment: the environment's
// own promotionPolicy if set, otherwise the strategy-level default, otherwise Latest.
func (ps *PromotionStrategySpec) GetPromotionPolicy(e Environment) PromotionPolicy {
	if e.PromotionPolicy != "" {
		return e.PromotionPolicy
	}
	if ps.PromotionPolicy != "" {
		return ps.PromotionPolicy
	}
	return PromotionPolicyLatest
}

// GetAutoMerge returns the value of the AutoMerge field, defaulting to true if the field is nil.
func (e *Environment) GetAutoMerge() bool {
	if e.AutoMerge == nil {
		return true
	}
	return *e.AutoMerge
}

// CommitStatusSelector is used to select commit statuses by their key.
type CommitStatusSelector struct {
	// +required
	// +kubebuilder:validation:MinLength:=1
	// +kubebuilder:validation:MaxLength:=63
	// +kubebuilder:validation:Pattern:=([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]
	Key string `json:"key"`
}

// ScmLabelsSpec configures dynamic SCM pull request labels via an expression.
type ScmLabelsSpec struct {
	// Expression is evaluated using the expr library (github.com/expr-lang/expr) against
	// ChangeTransferPolicy status and spec. It must return a list of SCM label name strings.
	//
	// Available variables:
	//   - Status: ChangeTransferPolicy status (Proposed/Active commit statuses, branch SHAs, etc.)
	//   - Spec: ChangeTransferPolicy spec (ActiveBranch, ProposedBranch, etc.)
	//   - PromotionStrategy: owning PromotionStrategy spec and status when available
	//
	// Each returned label name must satisfy the same validation as PullRequest.spec.labels
	// (non-empty, max 50 characters, no newlines, max 10 labels, unique).
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=8192
	Expression string `json:"expression"`
}

// PullRequestPolicySpec configures SCM pull request behavior for a promotion policy.
type PullRequestPolicySpec struct {
	// Labels configures dynamic SCM labels applied to promotion pull requests.
	// +kubebuilder:validation:Optional
	Labels *ScmLabelsSpec `json:"labels,omitempty"`
}

// PromotionStrategyStatus defines the observed state of PromotionStrategy
type PromotionStrategyStatus struct {
	// ObservedGeneration is the .metadata.generation that this status was reconciled from.
	// Because status is written via Server-Side Apply with ForceOwnership (which has no
	// optimistic-concurrency check), this field is the canonical way to detect stale
	// status writes: compare status.observedGeneration with metadata.generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Environments holds the status of each environment in the promotion sequence.
	// +listType:=map
	// +listMapKey=branch
	Environments []EnvironmentStatus `json:"environments"`

	// Conditions Represents the observations of the current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// InstanceID mirrors metadata.labels[promoter.argoproj.io/instance-id] stamped on each
	// reconcile attempt by this install's controller, including when Ready=False; omitted
	// when the resource has no instance-id label (default install).
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$`
	InstanceID *string `json:"instanceID,omitempty"`
}

// GetConditions returns the conditions of the PromotionStrategy.
func (ps *PromotionStrategy) GetConditions() *[]metav1.Condition {
	return &ps.Status.Conditions
}

// SetObservedGeneration records the object generation that produced the current status.
func (ps *PromotionStrategy) SetObservedGeneration(generation int64) {
	ps.Status.ObservedGeneration = generation
}

// SetStatusInstanceID records the instance-id label mirrored into status on each reconcile attempt.
func (ps *PromotionStrategy) SetStatusInstanceID(v *string) {
	ps.Status.InstanceID = v
}

// EnvironmentStatus defines the observed state of an environment in a PromotionStrategy.
type EnvironmentStatus struct {
	// Branch is the name of the active branch for the environment.
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`
	// Proposed is the state of the proposed branch for the environment.
	Proposed CommitBranchState `json:"proposed"`
	// Active is the state of the active branch for the environment.
	Active CommitBranchState `json:"active"`

	// PullRequest is the state of the pull request that was created for this environment.
	PullRequest *PullRequestCommonStatus `json:"pullRequest,omitempty"`

	// Candidate is the tip of the hydrator's proposed branch for this environment: the newest change
	// that exists, whether or not it is eligible for promotion. It is only populated when the
	// environment's promotion policy selects candidates, because otherwise it is identical to Proposed.
	// Comparing it with Proposed shows how far behind the newest change this environment is running.
	// +kubebuilder:validation:Optional
	Candidate *PromotionCandidateState `json:"candidate,omitempty"`

	// Verification mirrors the owning ChangeTransferPolicy status.verification and adds Current for
	// the change the environment is running right now when it is healthy on it. Later environments
	// consult the effective record — DryShas plus Current when present — when selecting candidates.
	// +kubebuilder:validation:Optional
	Verification *EnvironmentVerificationStatus `json:"verification,omitempty"`

	// History defines the history of promoted changes done by the PromotionStrategy for each environment.
	// You can think of it as a list of PRs merged by GitOps Promoter. It will not include changes that were
	// manually merged. The history length is hard-coded to be at most 5 entries. This may change in the future.
	// History is constructed on a best-effort basis and should be used for informational purposes only.
	// History is in reverse chronological order (newest is first).
	History []History `json:"history,omitempty"`
}

// MaxLastHealthyDryShas is the number of verified dry commits retained per environment in
// VerificationState.DryShas and EnvironmentVerificationStatus.DryShas. The list is a lookback window
// for downstream environments: it has to be long enough that a change stays eligible for promotion
// while slower downstream environments work through their queue, and short enough to keep the status
// object small. Keep it in sync with the MaxItems validation on those fields.
const MaxLastHealthyDryShas = 50

// EnvironmentVerificationStatus mirrors an environment's ChangeTransferPolicy status.verification on
// the PromotionStrategy and adds Current for the change the environment is running right now when it
// is healthy on it. Current is composed at reconcile time rather than stored on the CTP because it
// is a claim about the present.
type EnvironmentVerificationStatus struct {
	// DryShas are copied from the owning ChangeTransferPolicy status.verification.dryShas: changes
	// this environment was healthy on when it promoted past them, newest first.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=50
	DryShas []HealthyDryShas `json:"dryShas,omitempty"`

	// ObservedActiveSha is copied from the owning ChangeTransferPolicy status.verification.observedActiveSha.
	// Supports both SHA-1 (40 chars) and SHA-256 (64 chars) Git hash formats.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^([a-f0-9]{40}|[a-f0-9]{64})?$`
	ObservedActiveSha string `json:"observedActiveSha,omitempty"`

	// Current is the change the environment is running right now when every active commit status is
	// passing and that change is not already in DryShas. Omitted when the environment is not healthy
	// on its active change or when the active change is already recorded in DryShas.
	// +kubebuilder:validation:Optional
	Current *HealthyDryShas `json:"current,omitempty"`
}

// HasVerifiedDrySha reports whether the environment has ever been observed healthy on the given dry SHA.
func (v *EnvironmentVerificationStatus) HasVerifiedDrySha(drySha string) bool {
	if drySha == "" || v == nil {
		return false
	}
	if v.Current != nil && v.Current.Sha == drySha {
		return true
	}
	for _, healthy := range v.DryShas {
		if healthy.Sha == drySha {
			return true
		}
	}
	return false
}

// EffectiveVerifiedDryShas returns the dry commits this environment vouches for: DryShas with Current
// prepended when it names a change not already recorded. Entries are newest first.
func (v *EnvironmentVerificationStatus) EffectiveVerifiedDryShas() []HealthyDryShas {
	if v == nil {
		return nil
	}
	if v.Current == nil {
		return v.DryShas
	}
	for _, entry := range v.DryShas {
		if entry.Sha == v.Current.Sha {
			return v.DryShas
		}
	}
	return append([]HealthyDryShas{*v.Current}, v.DryShas...)
}

// HealthyDryShas records a dry commit that an environment verified.
type HealthyDryShas struct {
	// Sha is the commit SHA of the dry commit that was observed to be healthy.
	// Supports both SHA-1 (40 chars) and SHA-256 (64 chars) Git hash formats.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^([a-f0-9]{40}|[a-f0-9]{64})$`
	Sha string `json:"sha"`
	// Time is the time at which the environment was first observed to be healthy on this dry SHA.
	Time metav1.Time `json:"time"`
}

// +kubebuilder:ac:generate=true
// +kubebuilder:externalDocs:url="https://gitops-promoter.readthedocs.io/en/stable/crd-specs/#promotionstrategy",description="CRD reference (examples and behavior)"
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// PromotionStrategy is the Schema for the promotionstrategies API
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type PromotionStrategy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromotionStrategySpec   `json:"spec"`
	Status PromotionStrategyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PromotionStrategyList contains a list of PromotionStrategy
type PromotionStrategyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionStrategy `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PromotionStrategy{}, &PromotionStrategyList{})
		return nil
	})
}
