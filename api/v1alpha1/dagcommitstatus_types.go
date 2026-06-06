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
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// DagCommitStatusSpec defines the desired state of DagCommitStatus.
//
// A DagCommitStatus declares a directed-acyclic dependency graph over the
// environments of a referenced PromotionStrategy. For each non-root environment
// in the graph, the controller produces one CommitStatus on that environment's
// proposed hydrated SHA, whose phase aggregates the active CommitStatuses on the
// parent (dependsOn) environments. Users opt in to gating by referencing the
// produced CommitStatus's Key in the PromotionStrategy's proposedCommitStatuses.
type DagCommitStatusSpec struct {
	// PromotionStrategyRef is a reference to the PromotionStrategy whose environments
	// are being graphed. Must live in the same namespace as this DagCommitStatus.
	// +kubebuilder:validation:Required
	PromotionStrategyRef ObjectReference `json:"promotionStrategyRef"`

	// Key is the CommitStatus key produced on each non-root environment's proposed
	// hydrated SHA, and the key users reference in proposedCommitStatuses to gate
	// promotion. Defaults to "promoter-previous-environment" for back-compat with
	// the synthesised gate that PromotionStrategy used to produce.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=promoter-previous-environment
	// +kubebuilder:validation:MinLength:=1
	// +kubebuilder:validation:MaxLength:=63
	// +kubebuilder:validation:Pattern:=([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]
	Key string `json:"key,omitempty"`

	// Environments declares the dependency graph. Every branch named here must
	// exist in the referenced PromotionStrategy's spec.environments. Every entry
	// in dependsOn must also appear in this list. Cycles are rejected.
	// +kubebuilder:validation:MinItems:=1
	// +listType:=map
	// +listMapKey=branch
	Environments []DagEnvironment `json:"environments"`
}

// DagEnvironment is one node in the DAG.
type DagEnvironment struct {
	// Branch is the active branch of the environment in the referenced PromotionStrategy.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`

	// DependsOn lists the parent branches that must be healthy before this
	// environment's promotion is gated as successful. An empty (or absent)
	// DependsOn means this is a root environment in the graph and no
	// CommitStatus is produced for it.
	// +kubebuilder:validation:Optional
	// +listType:=set
	DependsOn []string `json:"dependsOn,omitempty"`
}

// DagCommitStatusStatus defines the observed state of DagCommitStatus.
type DagCommitStatusStatus struct {
	// ObservedGeneration is the .metadata.generation that this status was reconciled from.
	// Because status is written via Server-Side Apply with ForceOwnership (which has no
	// optimistic-concurrency check), this field is the canonical way to detect stale
	// status writes: compare status.observedGeneration with metadata.generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions Represents the observations of the current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// UnreferencedEnvironments is the list of non-root branches in the graph whose
	// PromotionStrategy environment does not reference this DagCommitStatus's Key
	// in its proposedCommitStatuses (env-level or spec-level). Those environments
	// will promote ungated. Informational only — the controller does not raise a
	// condition for this.
	// +optional
	UnreferencedEnvironments []string `json:"unreferencedEnvironments,omitempty"`

	// Environments mirrors, for each non-root env in the graph, the disaggregated
	// per-parent active commit statuses that fed into the env's gate. This is the
	// canonical place to observe what the DAG saw.
	// +listType:=map
	// +listMapKey=branch
	// +optional
	Environments []DagEnvironmentStatus `json:"environments,omitempty"`
}

// DagEnvironmentStatus is the per-non-root-env aggregation result.
type DagEnvironmentStatus struct {
	// Branch is the gated (child) environment.
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`
	// AggregatePhase is the phase written to the produced CommitStatus on this env's
	// proposed hydrated SHA (success only when all parent chains are success).
	// +kubebuilder:validation:Enum:=pending;success;failure
	AggregatePhase CommitStatusPhase `json:"aggregatePhase"`
	// DrySha is the dry SHA that the parents were evaluated against, i.e. the
	// gated environment's effective hydrated dry SHA.
	// +optional
	DrySha string `json:"drySha,omitempty"`
	// Parents carries the per-parent disaggregated view: for each dependsOn branch,
	// which active CommitStatuses contributed and their individual phases.
	// +listType:=map
	// +listMapKey=branch
	// +optional
	Parents []DagParentStatus `json:"parents,omitempty"`
}

// DagParentStatus is the per-parent disaggregated view of contributing active commit statuses.
type DagParentStatus struct {
	// Branch is the dependsOn parent.
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`
	// Phase is this parent's rolled-up phase (worst over its CommitStatuses + chain).
	// +kubebuilder:validation:Enum:=pending;success;failure
	Phase CommitStatusPhase `json:"phase"`
	// CommitStatuses is the disaggregated list of active CommitStatuses on this parent
	// for the matched dry SHA.
	// +listType:=map
	// +listMapKey=key
	// +optional
	CommitStatuses []ChangeRequestPolicyCommitStatusPhase `json:"commitStatuses,omitempty"`
}

// GetConditions returns the conditions of the DagCommitStatus.
func (d *DagCommitStatus) GetConditions() *[]metav1.Condition {
	return &d.Status.Conditions
}

// SetObservedGeneration records the object generation that produced the current status.
func (d *DagCommitStatus) SetObservedGeneration(generation int64) {
	d.Status.ObservedGeneration = generation
}

// +kubebuilder:ac:generate=true
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// DagCommitStatus is the Schema for the dagcommitstatuses API.
// +kubebuilder:printcolumn:name="PromotionStrategy",type=string,JSONPath=`.spec.promotionStrategyRef.name`,priority=1
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type DagCommitStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DagCommitStatusSpec   `json:"spec,omitempty"`
	Status DagCommitStatusStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DagCommitStatusList contains a list of DagCommitStatus.
type DagCommitStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DagCommitStatus `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DagCommitStatus{}, &DagCommitStatusList{})
}
