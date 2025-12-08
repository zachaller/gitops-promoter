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

//nolint:revive // max-public-structs is expected for API types
package aggregated

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PromotionStatusList is a list of PromotionStatus objects.
type PromotionStatusList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []PromotionStatus
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PromotionStatus is an aggregated view of a PromotionStrategy and all its related resources.
// It provides a unified view of commit statuses, pull requests, change transfer policies,
// and external commit status managers (ArgoCDCommitStatus, TimedCommitStatus) for a given
// PromotionStrategy.
type PromotionStatus struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	// Spec contains the specification for the aggregated promotion status.
	Spec PromotionStatusSpec

	// Status contains the observed state of the aggregated promotion status.
	Status PromotionStatusStatus
}

// PromotionStatusSpec defines the specification for an aggregated promotion status.
type PromotionStatusSpec struct {
	// PromotionStrategyRef is a reference to the PromotionStrategy this status aggregates.
	PromotionStrategyRef ObjectReference
}

// ObjectReference is a reference to a Kubernetes object.
type ObjectReference struct {
	// Name is the name of the referenced object.
	Name string
	// Namespace is the namespace of the referenced object.
	Namespace string
}

// PromotionStatusStatus defines the observed state of an aggregated promotion status.
type PromotionStatusStatus struct {
	// Environments holds the aggregated status of each environment in the promotion sequence.
	Environments []AggregatedEnvironmentStatus

	// Conditions represent the observations of the current state.
	Conditions []metav1.Condition
}

// AggregatedEnvironmentStatus defines the aggregated status of an environment.
//
//nolint:revive // stuttering is acceptable for API types to be explicit
type AggregatedEnvironmentStatus struct {
	// Branch is the name of the active branch for the environment.
	Branch string

	// ActiveCommit contains information about the currently active commit.
	ActiveCommit CommitInfo

	// ProposedCommit contains information about the proposed commit (if any).
	ProposedCommit *CommitInfo

	// PullRequest contains the status of the pull request for this environment (if any).
	PullRequest *PullRequestInfo

	// CommitStatuses contains all commit statuses for this environment.
	CommitStatuses []CommitStatusInfo

	// ChangeTransferPolicies contains all change transfer policies affecting this environment.
	ChangeTransferPolicies []ChangeTransferPolicyInfo
}

// CommitInfo contains information about a commit.
type CommitInfo struct {
	// Sha is the commit SHA.
	Sha string

	// DrySha is the dry commit SHA (if applicable).
	DrySha string

	// HydratedSha is the hydrated commit SHA (if applicable).
	HydratedSha string
}

// PullRequestInfo contains information about a pull request.
type PullRequestInfo struct {
	// Number is the PR number in the SCM.
	Number int

	// Name is the name of the PullRequest resource.
	Name string

	// ID is the unique identifier of the pull request in the SCM.
	ID string

	// State is the current state of the pull request.
	State string

	// URL is the URL to the pull request in the SCM.
	URL string
}

// CommitStatusInfo contains information about a commit status.
type CommitStatusInfo struct {
	// Name is the name of the CommitStatus resource.
	Name string

	// Key is the commit status key.
	Key string

	// Phase is the current phase of the commit status.
	Phase string

	// Source indicates the source of this commit status.
	Source CommitStatusSource

	// Description provides additional details about the status.
	Description string

	// URL is an optional URL for more details.
	URL string
}

// CommitStatusSource indicates the source of a commit status.
type CommitStatusSource struct {
	// Type indicates the type of commit status source.
	// Valid values are: "CommitStatus", "ArgoCDCommitStatus", "TimedCommitStatus", "External"
	Type string

	// Name is the name of the source resource (if applicable).
	Name string
}

// ChangeTransferPolicyInfo contains information about a change transfer policy.
type ChangeTransferPolicyInfo struct {
	// Name is the name of the ChangeTransferPolicy resource.
	Name string

	// ActiveCommitStatuses lists the active commit status keys.
	ActiveCommitStatuses []string

	// ProposedCommitStatuses lists the proposed commit status keys.
	ProposedCommitStatuses []string
}
