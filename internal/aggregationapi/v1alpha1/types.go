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

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PromotionStrategyView is a read-only aggregated view of a PromotionStrategy and all its related resources.
// This resource is computed on-demand and does not exist in etcd.
type PromotionStrategyView struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec mirrors the PromotionStrategy spec
	Spec promoterv1alpha1.PromotionStrategySpec `json:"spec,omitempty"`

	// Status mirrors the PromotionStrategy status
	Status promoterv1alpha1.PromotionStrategyStatus `json:"status,omitempty"`

	// Aggregated contains all related resources for this PromotionStrategy
	Aggregated AggregatedResources `json:"aggregated,omitempty"`
}

// AggregatedResources contains all resources related to a PromotionStrategy.
type AggregatedResources struct {
	// GitRepository is the GitRepository referenced by the PromotionStrategy
	GitRepository *GitRepositoryRef `json:"gitRepository,omitempty"`

	// ChangeTransferPolicies contains the ChangeTransferPolicy for each environment
	ChangeTransferPolicies []ChangeTransferPolicyRef `json:"changeTransferPolicies,omitempty"`

	// CommitStatuses contains all commit status resources that reference this PromotionStrategy
	CommitStatuses CommitStatusAggregation `json:"commitStatuses,omitempty"`

	// PullRequests contains all PullRequest resources owned by ChangeTransferPolicies of this PromotionStrategy
	PullRequests []PullRequestRef `json:"pullRequests,omitempty"`
}

// GitRepositoryRef contains the GitRepository resource data.
type GitRepositoryRef struct {
	// Name is the name of the GitRepository
	Name string `json:"name"`
	// Namespace is the namespace of the GitRepository
	Namespace string `json:"namespace"`
	// Spec is the spec of the GitRepository
	Spec promoterv1alpha1.GitRepositorySpec `json:"spec,omitempty"`
	// Status is the status of the GitRepository
	Status promoterv1alpha1.GitRepositoryStatus `json:"status,omitempty"`
}

// ChangeTransferPolicyRef contains the ChangeTransferPolicy resource data.
type ChangeTransferPolicyRef struct {
	// Name is the name of the ChangeTransferPolicy
	Name string `json:"name"`
	// Namespace is the namespace of the ChangeTransferPolicy
	Namespace string `json:"namespace"`
	// Branch is the active branch this ChangeTransferPolicy manages
	Branch string `json:"branch"`
	// Spec is the spec of the ChangeTransferPolicy
	Spec promoterv1alpha1.ChangeTransferPolicySpec `json:"spec,omitempty"`
	// Status is the status of the ChangeTransferPolicy
	Status promoterv1alpha1.ChangeTransferPolicyStatus `json:"status,omitempty"`
}

// CommitStatusAggregation groups all commit status types.
type CommitStatusAggregation struct {
	// ArgoCD contains ArgoCDCommitStatus resources
	ArgoCD []ArgoCDCommitStatusRef `json:"argoCD,omitempty"`
	// Git contains GitCommitStatus resources
	Git []GitCommitStatusRef `json:"git,omitempty"`
	// Timed contains TimedCommitStatus resources
	Timed []TimedCommitStatusRef `json:"timed,omitempty"`
	// CommitStatuses contains CommitStatus resources (the low-level status resources)
	CommitStatuses []CommitStatusRef `json:"commitStatuses,omitempty"`
}

// ResourceMetadata contains common metadata fields for aggregated resources.
// This mirrors the structure of metav1.ObjectMeta but only includes the fields
// needed for identification and ownership tracking.
type ResourceMetadata struct {
	// Name is the name of the resource
	Name string `json:"name"`
	// Namespace is the namespace of the resource
	Namespace string `json:"namespace"`
	// UID is the unique identifier of the resource
	UID string `json:"uid,omitempty"`
	// OwnerReferences contains the owner references of the resource
	OwnerReferences []metav1.OwnerReference `json:"ownerReferences,omitempty"`
}

// ArgoCDCommitStatusRef contains the ArgoCDCommitStatus resource data.
type ArgoCDCommitStatusRef struct {
	// Metadata contains identifying information for the resource
	Metadata ResourceMetadata `json:"metadata"`
	// Spec is the spec of the ArgoCDCommitStatus
	Spec promoterv1alpha1.ArgoCDCommitStatusSpec `json:"spec,omitempty"`
	// Status is the status of the ArgoCDCommitStatus
	Status promoterv1alpha1.ArgoCDCommitStatusStatus `json:"status,omitempty"`
}

// GitCommitStatusRef contains the GitCommitStatus resource data.
type GitCommitStatusRef struct {
	// Metadata contains identifying information for the resource
	Metadata ResourceMetadata `json:"metadata"`
	// Spec is the spec of the GitCommitStatus
	Spec promoterv1alpha1.GitCommitStatusSpec `json:"spec,omitempty"`
	// Status is the status of the GitCommitStatus
	Status promoterv1alpha1.GitCommitStatusStatus `json:"status,omitempty"`
}

// TimedCommitStatusRef contains the TimedCommitStatus resource data.
type TimedCommitStatusRef struct {
	// Metadata contains identifying information for the resource
	Metadata ResourceMetadata `json:"metadata"`
	// Spec is the spec of the TimedCommitStatus
	Spec promoterv1alpha1.TimedCommitStatusSpec `json:"spec,omitempty"`
	// Status is the status of the TimedCommitStatus
	Status promoterv1alpha1.TimedCommitStatusStatus `json:"status,omitempty"`
}

// CommitStatusRef contains the CommitStatus resource data.
type CommitStatusRef struct {
	// Metadata contains identifying information for the resource.
	// OwnerReferences can be used to find the parent commit status manager
	// (e.g., TimedCommitStatus, GitCommitStatus, ArgoCDCommitStatus).
	Metadata ResourceMetadata `json:"metadata"`
	// Spec is the spec of the CommitStatus
	Spec promoterv1alpha1.CommitStatusSpec `json:"spec,omitempty"`
	// Status is the status of the CommitStatus
	Status promoterv1alpha1.CommitStatusStatus `json:"status,omitempty"`
}

// PullRequestRef contains the PullRequest resource data.
type PullRequestRef struct {
	// Name is the name of the PullRequest
	Name string `json:"name"`
	// Namespace is the namespace of the PullRequest
	Namespace string `json:"namespace"`
	// Branch is the target branch this PullRequest is for
	Branch string `json:"branch"`
	// Spec is the spec of the PullRequest
	Spec promoterv1alpha1.PullRequestSpec `json:"spec,omitempty"`
	// Status is the status of the PullRequest
	Status promoterv1alpha1.PullRequestStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PromotionStrategyViewList contains a list of PromotionStrategyView.
type PromotionStrategyViewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionStrategyView `json:"items"`
}
