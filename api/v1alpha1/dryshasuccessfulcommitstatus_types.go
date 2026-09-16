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

// DryShaSuccessfulCommitStatusSpec defines the desired state of DryShaSuccessfulCommitStatus.
type DryShaSuccessfulCommitStatusSpec struct {
	// PromotionStrategyRef is a reference to the promotion strategy that this gate applies to. The controller
	// watches this PromotionStrategy and, for each environment, reports whether the dry commit being promoted
	// has already been successful in that environment's upstream (dependsOn) environments.
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
	// .Environment, .DryShaSuccessfulCommitStatus, .PromotionStrategy, .DependsOn, and .DependsOnQuery
	// (see controller docs).
	// +kubebuilder:validation:Optional
	URL URLConfig `json:"url,omitempty"`

	// HistoryDepth is how many first-parent commits of each environment's active branch are walked when
	// rebuilding the dry SHA record from the promotion-history git notes. A dry commit older than this
	// depth is not found in the record and the gate reports pending for it.
	// +optional
	// +kubebuilder:default:=20
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=200
	HistoryDepth int32 `json:"historyDepth,omitempty"`
}

// EffectiveHistoryDepth returns spec.historyDepth, or the default when it is unset.
func (s *DryShaSuccessfulCommitStatusSpec) EffectiveHistoryDepth() int {
	if s.HistoryDepth <= 0 {
		return DryShaSuccessfulDefaultHistoryDepth
	}
	return int(s.HistoryDepth)
}

// DryShaRecordSource names the git source that supplied the trailers for a DryShaRecord.
// +kubebuilder:validation:Enum:=note;commit-message;live
type DryShaRecordSource string

const (
	// DryShaRecordSourceNote means the entry came from the promotion-history git note on the merge commit.
	DryShaRecordSourceNote DryShaRecordSource = "note"
	// DryShaRecordSourceCommitMessage means the note was absent and the entry came from the merge commit's
	// own message trailers.
	DryShaRecordSourceCommitMessage DryShaRecordSource = "commit-message"
	// DryShaRecordSourceLive means the entry describes the dry commit currently running in the environment,
	// taken from live PromotionStrategy status. No promotion-history note exists for it yet, because a note
	// is only written when the next promotion merges.
	DryShaRecordSourceLive DryShaRecordSource = "live"
)

// DryShaRecord is one entry of an environment's dry SHA record, reconstructed from its active branch.
type DryShaRecord struct {
	// Sha is the dry commit that was running in the environment. For a git-derived entry this is the
	// promotion-history note's Sha-dry-active trailer, which records what the environment was running
	// immediately before that promotion merged.
	// Supports both SHA-1 (40 chars) and SHA-256 (64 chars) Git hash formats.
	// +required
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^([a-f0-9]{40}|[a-f0-9]{64})$`
	Sha string `json:"sha"`

	// MergeSha is the active branch commit this entry was derived from. Entries are ordered by
	// first-parent distance from the branch tip, so it doubles as the entry's position marker.
	// For a live entry it is the environment's active hydrated SHA.
	// +required
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^([a-f0-9]{40}|[a-f0-9]{64})$`
	MergeSha string `json:"mergeSha"`

	// Source names which git source supplied this entry's data.
	// +optional
	Source DryShaRecordSource `json:"source,omitempty"`

	// MergedAt is the merge time of the promotion that produced this entry (the note's
	// Pull-request-merge-time trailer). Informational; ordering uses the first-parent walk, not time.
	// +optional
	MergedAt metav1.Time `json:"mergedAt,omitempty"`

	// Successful is true when every active commit status recorded alongside Sha was successful. This is
	// the same success criterion DependentsSuccessfulCommitStatus applies to an upstream environment,
	// read from the recorded snapshot rather than from live state.
	// +required
	Successful bool `json:"successful"`
}

// DryShaSuccessfulCommitStatusUpstreamStatus reports whether a transitive upstream branch is satisfied
// for this environment's promotion target.
type DryShaSuccessfulCommitStatusUpstreamStatus struct {
	// Branch is the upstream environment branch name.
	// +required
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`

	// SatisfiedBySha is the dry commit whose recorded success satisfied this upstream. It equals the
	// target dry SHA when the target itself was successful, or a descendant dry SHA when a later
	// promotion on the upstream's active branch was. Omitted when the upstream is not satisfied.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^([a-f0-9]{40}|[a-f0-9]{64})?$`
	SatisfiedBySha string `json:"satisfiedBySha,omitempty"`

	// Reason explains the verdict for this upstream. It is normally set only when the upstream is not
	// satisfied, but a satisfied upstream also carries one when it was skipped because the proposed dry
	// commit renders no change there (a no-op hydration), since nothing in dryShaHistory records that.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Satisfied is true when the upstream has been successful for this environment's target dry SHA.
	// +required
	Satisfied bool `json:"satisfied"`
}

// DryShaSuccessfulCommitStatusEnvironmentStatus defines observed state for one environment branch.
type DryShaSuccessfulCommitStatusEnvironmentStatus struct {
	// Branch is the environment branch name.
	// +required
	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`

	// Gate report fields (phase, description, url, reportedSha) mirror the child CommitStatus spec.
	// When there is no in-flight proposed change, the controller copies the last child CommitStatus
	// report instead of re-evaluating the gate.
	GateEnvironmentCommitStatus `json:",inline"`

	// RebuiltFromSha is the active branch tip that dryShaHistory was walked from. While the environment's
	// live active hydrated SHA still equals this, the cached record is current and no git work is done.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^([a-f0-9]{40}|[a-f0-9]{64})?$`
	RebuiltFromSha string `json:"rebuiltFromSha,omitempty"`

	// RebuiltAt is when the active branch walk that produced dryShaHistory last ran.
	// +optional
	RebuiltAt metav1.Time `json:"rebuiltAt,omitempty"`

	// DryShaHistory is a cache of the dry commits this environment has run, newest first: index 0 is
	// closest to the active branch tip. It is rebuilt from the promotion-history git notes on the active
	// branch and is safe to delete — the next reconcile regenerates it from git.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=201
	DryShaHistory []DryShaRecord `json:"dryShaHistory,omitempty"`

	// Upstreams lists all transitive ancestor branches and whether each is satisfied for this
	// environment's promotion target.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=500
	Upstreams []DryShaSuccessfulCommitStatusUpstreamStatus `json:"upstreams,omitempty"`
}

// DryShaSuccessfulCommitStatusStatus defines the observed state of DryShaSuccessfulCommitStatus.
type DryShaSuccessfulCommitStatusStatus struct {
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

	// Environments reports observed gate state and the cached dry SHA record per dependency-graph branch.
	// +optional
	// +kubebuilder:validation:MaxItems=500
	// +listType=map
	// +listMapKey=branch
	Environments []DryShaSuccessfulCommitStatusEnvironmentStatus `json:"environments,omitempty"`
}

// +kubebuilder:ac:generate=true
// +kubebuilder:externalDocs:url="https://gitops-promoter.readthedocs.io/en/stable/crd-specs/#dryshasuccessfulcommitstatus",description="CRD reference (examples and behavior)"
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="PromotionStrategy",type=string,JSONPath=`.spec.promotionStrategyRef.name`,priority=1
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`

// DryShaSuccessfulCommitStatus is the Schema for the dryshasuccessfulcommitstatuses API
type DryShaSuccessfulCommitStatus struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of DryShaSuccessfulCommitStatus
	// +required
	Spec DryShaSuccessfulCommitStatusSpec `json:"spec"`

	// status defines the observed state of DryShaSuccessfulCommitStatus
	// +optional
	Status DryShaSuccessfulCommitStatusStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DryShaSuccessfulCommitStatusList contains a list of DryShaSuccessfulCommitStatus
type DryShaSuccessfulCommitStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DryShaSuccessfulCommitStatus `json:"items"`
}

// GetConditions returns the conditions of the DryShaSuccessfulCommitStatus.
func (d *DryShaSuccessfulCommitStatus) GetConditions() *[]metav1.Condition {
	return &d.Status.Conditions
}

// SetObservedGeneration records the object generation that produced the current status.
func (d *DryShaSuccessfulCommitStatus) SetObservedGeneration(generation int64) {
	d.Status.ObservedGeneration = generation
}

// SetStatusInstanceID records the instance-id label mirrored into status on each reconcile attempt.
func (d *DryShaSuccessfulCommitStatus) SetStatusInstanceID(v *string) {
	d.Status.InstanceID = v
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &DryShaSuccessfulCommitStatus{}, &DryShaSuccessfulCommitStatusList{})
		return nil
	})
}
