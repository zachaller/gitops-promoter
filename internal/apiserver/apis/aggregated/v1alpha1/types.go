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
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PromotionStatusList is a list of PromotionStatus objects.
type PromotionStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`

	Items []PromotionStatus `json:"items" protobuf:"bytes,2,rep,name=items"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PromotionStatus is an aggregated view of a PromotionStrategy and all its related resources.
// It provides a unified view of commit statuses, pull requests, change transfer policies,
// and external commit status managers (ArgoCDCommitStatus, TimedCommitStatus) for a given
// PromotionStrategy.
type PromotionStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`

	// Spec contains the specification for the aggregated promotion status.
	// +optional
	Spec PromotionStatusSpec `json:"spec,omitempty" protobuf:"bytes,2,opt,name=spec"`

	// Status contains the observed state of the aggregated promotion status.
	// +optional
	Status PromotionStatusStatus `json:"status,omitempty" protobuf:"bytes,3,opt,name=status"`
}

// PromotionStatusSpec defines the specification for an aggregated promotion status.
type PromotionStatusSpec struct {
	// PromotionStrategyRef is a reference to the PromotionStrategy this status aggregates.
	// +optional
	PromotionStrategyRef ObjectReference `json:"promotionStrategyRef,omitempty" protobuf:"bytes,1,opt,name=promotionStrategyRef"`
}

// ObjectReference is a reference to a Kubernetes object.
type ObjectReference struct {
	// Name is the name of the referenced object.
	// +optional
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	// Namespace is the namespace of the referenced object.
	// +optional
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,2,opt,name=namespace"`
}

// PromotionStatusStatus defines the observed state of an aggregated promotion status.
type PromotionStatusStatus struct {
	// Environments holds the aggregated status of each environment in the promotion sequence.
	// +optional
	// +listType=map
	// +listMapKey=branch
	Environments []AggregatedEnvironmentStatus `json:"environments,omitempty" protobuf:"bytes,1,rep,name=environments"`

	// Conditions represent the observations of the current state.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,2,rep,name=conditions"`
}

// AggregatedEnvironmentStatus defines the aggregated status of an environment.
type AggregatedEnvironmentStatus struct {
	// Branch is the name of the active branch for the environment.
	Branch string `json:"branch" protobuf:"bytes,1,opt,name=branch"`

	// ActiveCommit contains information about the currently active commit.
	// +optional
	ActiveCommit CommitInfo `json:"activeCommit,omitempty" protobuf:"bytes,2,opt,name=activeCommit"`

	// ProposedCommit contains information about the proposed commit (if any).
	// +optional
	ProposedCommit *CommitInfo `json:"proposedCommit,omitempty" protobuf:"bytes,3,opt,name=proposedCommit"`

	// PullRequest contains the status of the pull request for this environment (if any).
	// +optional
	PullRequest *PullRequestInfo `json:"pullRequest,omitempty" protobuf:"bytes,4,opt,name=pullRequest"`

	// CommitStatuses contains all commit statuses for this environment.
	// +optional
	// +listType=map
	// +listMapKey=name
	CommitStatuses []CommitStatusInfo `json:"commitStatuses,omitempty" protobuf:"bytes,5,rep,name=commitStatuses"`

	// ChangeTransferPolicies contains all change transfer policies affecting this environment.
	// +optional
	// +listType=map
	// +listMapKey=name
	ChangeTransferPolicies []ChangeTransferPolicyInfo `json:"changeTransferPolicies,omitempty" protobuf:"bytes,6,rep,name=changeTransferPolicies"`
}

// CommitInfo contains information about a commit.
type CommitInfo struct {
	// Sha is the commit SHA.
	// +optional
	Sha string `json:"sha,omitempty" protobuf:"bytes,1,opt,name=sha"`

	// DrySha is the dry commit SHA (if applicable).
	// +optional
	DrySha string `json:"drySha,omitempty" protobuf:"bytes,2,opt,name=drySha"`

	// HydratedSha is the hydrated commit SHA (if applicable).
	// +optional
	HydratedSha string `json:"hydratedSha,omitempty" protobuf:"bytes,3,opt,name=hydratedSha"`
}

// PullRequestInfo contains information about a pull request.
type PullRequestInfo struct {
	// Number is the PR number in the SCM.
	// +optional
	Number int `json:"number,omitempty" protobuf:"varint,3,opt,name=number"`

	// Name is the name of the PullRequest resource.
	// +optional
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`

	// ID is the unique identifier of the pull request in the SCM.
	// +optional
	ID string `json:"id,omitempty" protobuf:"bytes,2,opt,name=id"`

	// State is the current state of the pull request.
	// +optional
	State string `json:"state,omitempty" protobuf:"bytes,4,opt,name=state"`

	// URL is the URL to the pull request in the SCM.
	// +optional
	URL string `json:"url,omitempty" protobuf:"bytes,5,opt,name=url"`
}

// CommitStatusInfo contains information about a commit status.
type CommitStatusInfo struct {
	// Name is the name of the CommitStatus resource.
	Name string `json:"name" protobuf:"bytes,1,opt,name=name"`

	// Key is the commit status key.
	// +optional
	Key string `json:"key,omitempty" protobuf:"bytes,2,opt,name=key"`

	// Phase is the current phase of the commit status.
	// +optional
	Phase string `json:"phase,omitempty" protobuf:"bytes,3,opt,name=phase"`

	// Source indicates the source of this commit status.
	// +optional
	Source CommitStatusSource `json:"source,omitempty" protobuf:"bytes,4,opt,name=source"`

	// Description provides additional details about the status.
	// +optional
	Description string `json:"description,omitempty" protobuf:"bytes,5,opt,name=description"`

	// URL is an optional URL for more details.
	// +optional
	URL string `json:"url,omitempty" protobuf:"bytes,6,opt,name=url"`
}

// CommitStatusSource indicates the source of a commit status.
type CommitStatusSource struct {
	// Type indicates the type of commit status source.
	// Valid values are: "CommitStatus", "ArgoCDCommitStatus", "TimedCommitStatus", "External"
	// +optional
	Type string `json:"type,omitempty" protobuf:"bytes,1,opt,name=type"`

	// Name is the name of the source resource (if applicable).
	// +optional
	Name string `json:"name,omitempty" protobuf:"bytes,2,opt,name=name"`
}

// ChangeTransferPolicyInfo contains information about a change transfer policy.
type ChangeTransferPolicyInfo struct {
	// Name is the name of the ChangeTransferPolicy resource.
	Name string `json:"name" protobuf:"bytes,1,opt,name=name"`

	// ActiveCommitStatuses lists the active commit status keys.
	// +optional
	// +listType=atomic
	ActiveCommitStatuses []string `json:"activeCommitStatuses,omitempty" protobuf:"bytes,2,rep,name=activeCommitStatuses"`

	// ProposedCommitStatuses lists the proposed commit status keys.
	// +optional
	// +listType=atomic
	ProposedCommitStatuses []string `json:"proposedCommitStatuses,omitempty" protobuf:"bytes,3,rep,name=proposedCommitStatuses"`
}
