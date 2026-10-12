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

// ProgressiveOrderCommitStatusSpec defines the desired state of ProgressiveOrderCommitStatus.
type ProgressiveOrderCommitStatusSpec struct {
	// PromotionStrategyRef is a reference to the promotion strategy that this progressive order commit status
	// applies to. The controller watches this PromotionStrategy and, for each environment, reports whether every
	// dependsOn upstream (as declared on the PromotionStrategy) has made healthy progress beyond what the
	// environment is currently running.
	// +required
	PromotionStrategyRef ObjectReference `json:"promotionStrategyRef"`

	// Key is the commit status key this controller writes on each environment's proposed hydrated SHA.
	// The PromotionStrategy controller injects this key onto every ChangeTransferPolicy's proposedCommitStatuses.
	// Must be lowercase alphanumeric with hyphens, 1–63 characters (pattern: ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$).
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=^[a-z0-9]([-a-z0-9]*[a-z0-9])?$
	Key string `json:"key"`

	// URL generates the URL to use on the per-environment CommitStatus (SCM details link), for
	// example a link into the Promoter UI that highlights this environment's dependsOn upstreams.
	// Optional; when empty, no URL is set on the child CommitStatus. The template receives
	// .Environment, .ProgressiveOrderCommitStatus, .PromotionStrategy, .DependsOn, and .DependsOnQuery
	// (see controller docs).
	// +kubebuilder:validation:Optional
	URL URLConfig `json:"url,omitempty"`
}

// ProgressiveOrderCommitStatusWatermark records the newest dry commit an environment was observed
// running with every active commit status passing.
type ProgressiveOrderCommitStatusWatermark struct {
	// Sha is the dry commit SHA.
	// Supports both SHA-1 (40 chars) and SHA-256 (64 chars) Git hash formats.
	// +required
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^([a-f0-9]{40}|[a-f0-9]{64})$`
	Sha string `json:"sha"`

	// CommitTime is the commit time of the dry commit. Watermarks are compared by commit time.
	// +required
	CommitTime metav1.Time `json:"commitTime"`

	// ObservedAt is when the controller first observed the environment healthy on this dry commit.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}

// ProgressiveOrderCommitStatusUpstreamStatus reports whether a direct dependsOn upstream has made
// healthy progress beyond what this environment is running.
type ProgressiveOrderCommitStatusUpstreamStatus struct {
	// Branch is the upstream environment branch name.
	// +required
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`

	// Satisfied is true when the upstream's last healthy dry commit is newer than this environment's
	// active dry commit and the upstream is not currently failing.
	// +required
	Satisfied bool `json:"satisfied"`

	// Reason explains why the upstream is not satisfied. Omitted when satisfied is true.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// ProgressiveOrderCommitStatusEnvironmentStatus defines observed state for one environment branch.
type ProgressiveOrderCommitStatusEnvironmentStatus struct {
	// Branch is the environment branch name.
	// +required
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`

	// Gate report fields (phase, description, url, reportedSha) mirror the child CommitStatus spec.
	// When there is no in-flight proposed change, the controller copies the last child CommitStatus
	// report instead of re-evaluating the gate.
	GateEnvironmentCommitStatus `json:",inline"`

	// ActiveCommitStatuses is a verbatim copy of the PromotionStrategy environment's active commit statuses.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	// +listType=map
	// +listMapKey=key
	ActiveCommitStatuses []ChangeRequestPolicyCommitStatusPhase `json:"activeCommitStatuses,omitempty"`

	// LastHealthyDry is the progress watermark for this environment: the newest dry commit it has been
	// observed running with all active commit statuses passing. It is persisted across reconciles so that
	// downstream environments can keep promoting while this environment is busy rolling out newer commits.
	// It is reset if the environment's active dry commit moves backwards (for example after a rollback).
	// +optional
	LastHealthyDry *ProgressiveOrderCommitStatusWatermark `json:"lastHealthyDry,omitempty"`

	// Upstreams lists the direct dependsOn upstream branches and whether each is satisfied.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	// +listType=atomic
	Upstreams []ProgressiveOrderCommitStatusUpstreamStatus `json:"upstreams,omitempty"`
}

// ProgressiveOrderCommitStatusStatus defines the observed state of ProgressiveOrderCommitStatus.
type ProgressiveOrderCommitStatusStatus struct {
	// ObservedGeneration is the .metadata.generation that this status was reconciled from.
	// Because status is written via Server-Side Apply with ForceOwnership (which has no
	// optimistic-concurrency check), this field is the canonical way to detect stale
	// status writes: compare status.observedGeneration with metadata.generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of an object's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// InstanceID mirrors metadata.labels[promoter.argoproj.io/instance-id] stamped on each
	// reconcile attempt by this install's controller, including when Ready=False; omitted
	// when the resource has no instance-id label (default install).
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$`
	InstanceID *string `json:"instanceID,omitempty"`

	// Environments reports observed gate state, progress watermark, and upstream state per dependency-graph branch.
	// +optional
	// +kubebuilder:validation:MaxItems=500
	// +listType=map
	// +listMapKey=branch
	Environments []ProgressiveOrderCommitStatusEnvironmentStatus `json:"environments,omitempty"`
}

// +kubebuilder:ac:generate=true
// +kubebuilder:externalDocs:url="https://gitops-promoter.readthedocs.io/en/stable/crd-specs/#progressiveordercommitstatus",description="CRD reference (examples and behavior)"
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ProgressiveOrderCommitStatus is the Schema for the progressiveordercommitstatuses API.
//
// It is an ordering gate (referenced from PromotionStrategy.spec.orderCommitStatusRef) that, unlike
// DependentsSuccessfulCommitStatus, does not require upstreams to be healthy on the exact dry commit an
// environment is promoting. Instead it remembers the newest dry commit each environment was healthy on and lets
// an environment promote as soon as every upstream has proven healthy progress past what that environment runs.
// This keeps higher environments moving in high-churn repositories where upstreams rarely settle on the latest commit.
type ProgressiveOrderCommitStatus struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ProgressiveOrderCommitStatus
	// +required
	Spec ProgressiveOrderCommitStatusSpec `json:"spec"`

	// status defines the observed state of ProgressiveOrderCommitStatus
	// +optional
	Status ProgressiveOrderCommitStatusStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ProgressiveOrderCommitStatusList contains a list of ProgressiveOrderCommitStatus
type ProgressiveOrderCommitStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ProgressiveOrderCommitStatus `json:"items"`
}

// GetConditions returns the conditions of the ProgressiveOrderCommitStatus.
func (p *ProgressiveOrderCommitStatus) GetConditions() *[]metav1.Condition {
	return &p.Status.Conditions
}

// SetObservedGeneration records the object generation that produced the current status.
func (p *ProgressiveOrderCommitStatus) SetObservedGeneration(generation int64) {
	p.Status.ObservedGeneration = generation
}

// SetStatusInstanceID records the instance-id label mirrored into status on each reconcile attempt.
func (p *ProgressiveOrderCommitStatus) SetStatusInstanceID(v *string) {
	p.Status.InstanceID = v
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ProgressiveOrderCommitStatus{}, &ProgressiveOrderCommitStatusList{})
		return nil
	})
}
