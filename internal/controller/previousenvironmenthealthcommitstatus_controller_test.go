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

package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

var _ = Describe("PreviousEnvironmentHealthCommitStatus Controller", func() {
	Context("isPreviousEnvironmentPending", func() {
		// Use fixed times for tests to ensure consistent time comparisons
		olderTime := metav1.NewTime(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))
		newerTime := metav1.NewTime(time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC))

		// Helper to create a HydratorMetadata pointer, or nil if empty
		makeNote := func(drySha string) *promoterv1alpha1.HydratorMetadata {
			if drySha == "" {
				return nil
			}
			return &promoterv1alpha1.HydratorMetadata{DrySha: drySha}
		}

		// Helper to create environment status with specific values
		makeEnvStatusWithTime := func(activeDrySha, proposedDrySha, noteDrySha string, activeTime metav1.Time) promoterv1alpha1.EnvironmentStatus {
			return promoterv1alpha1.EnvironmentStatus{
				Active: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        activeDrySha,
						CommitTime: activeTime,
					},
					CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
						{Key: "health", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
					},
				},
				Proposed: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        proposedDrySha,
						CommitTime: olderTime, // Set proposed commit time to olderTime by default
					},
					Note: makeNote(noteDrySha),
				},
			}
		}

		// Helper that uses the older time by default (for backward compatibility)
		makeEnvStatus := func(activeDrySha, proposedDrySha, noteSha string) promoterv1alpha1.EnvironmentStatus {
			return makeEnvStatusWithTime(activeDrySha, proposedDrySha, noteSha, olderTime)
		}

		DescribeTable("should correctly determine if previous environment is pending",
			func(prevActiveDry, prevProposedDry, prevNoteDry, currActiveDry, currProposedDry, currNoteSha string, expectPending bool, expectReasonContains string) {
				prevEnvStatus := makeEnvStatus(prevActiveDry, prevProposedDry, prevNoteDry)
				currEnvStatus := makeEnvStatus(currActiveDry, currProposedDry, currNoteSha)

				isPending, reason := isPreviousEnvironmentPending(prevEnvStatus, currEnvStatus)

				Expect(isPending).To(Equal(expectPending), "isPending mismatch")
				if expectReasonContains != "" {
					Expect(reason).To(ContainSubstring(expectReasonContains), "reason mismatch")
				}
			},
			// Scenario 1: Out-of-order hydration - staging hydrates before dev
			// Dev hasn't hydrated yet (note and proposed still show OLD)
			Entry("blocks when previous env hasn't hydrated yet (with git notes)",
				"OLD", "OLD", "OLD", // prev: active=OLD, proposed=OLD, note=OLD
				"OLD", "ABC", "ABC", // curr: active=OLD, proposed=ABC, note=ABC
				true, "Waiting for the hydrator to finish processing the proposed dry commit"),

			// Scenario 2: Normal flow - dev has hydrated and merged
			Entry("allows when previous env has merged the proposed dry SHA",
				"ABC", "ABC", "ABC", // prev: active=ABC, proposed=ABC, note=ABC
				"OLD", "ABC", "ABC", // curr: active=OLD, proposed=ABC, note=ABC
				false, ""),

			// Scenario 3: Git note - dev has no manifest changes
			// Dev's hydrator updated the note but didn't create a new commit
			Entry("allows when previous env has no changes to merge (git note)",
				"OLD", "OLD", "ABC", // prev: active=OLD, proposed=OLD, note=ABC (note updated, no new commit)
				"OLD", "ABC", "ABC", // curr: active=OLD, proposed=ABC, note=ABC
				false, ""),

			// Scenario 4: Legacy hydrator (no git notes) - dev hasn't hydrated
			Entry("blocks when previous env hasn't hydrated (no git notes)",
				"OLD", "OLD", "", // prev: active=OLD, proposed=OLD, note="" (empty)
				"OLD", "ABC", "", // curr: active=OLD, proposed=ABC, note="" (empty for legacy)
				true, "Waiting for the hydrator to finish processing the proposed dry commit"),

			// Scenario 5: Legacy hydrator - dev has hydrated but not merged
			Entry("blocks when previous env has hydrated but not merged (no git notes)",
				"OLD", "ABC", "", // prev: active=OLD, proposed=ABC, note="" (empty)
				"OLD", "ABC", "", // curr: active=OLD, proposed=ABC, note="" (empty for legacy)
				true, "Waiting for previous environment to be promoted"),

			// Scenario 6: Legacy hydrator - dev has hydrated and merged
			Entry("allows when previous env has merged (no git notes)",
				"ABC", "ABC", "", // prev: active=ABC, proposed=ABC, note="" (empty)
				"OLD", "ABC", "", // curr: active=OLD, proposed=ABC, note="" (empty for legacy)
				false, ""),

			// Scenario 7: Dev hydrated but not yet merged (with git notes)
			Entry("blocks when previous env has hydrated but not merged (with git notes)",
				"OLD", "ABC", "ABC", // prev: active=OLD, proposed=ABC, note=ABC
				"OLD", "ABC", "ABC", // curr: active=OLD, proposed=ABC, note=ABC
				true, "Waiting for previous environment to be promoted"),

			// Scenario 8: Mismatch between note and proposed (edge case)
			// Note shows newer SHA than proposed (hydrator updated note for even newer commit)
			Entry("blocks when note shows different SHA than what we're promoting",
				"OLD", "OLD", "DEF", // prev: active=OLD, proposed=OLD, note=DEF (different!)
				"OLD", "ABC", "ABC", // curr: active=OLD, proposed=ABC, note=ABC
				true, "Waiting for the hydrator to finish processing the proposed dry commit"),
		)

		// Test for when previous environment has already moved past the proposed dry SHA
		// AND both environments have the same Note.DrySha (confirming they've seen the same dry commits)
		//
		// Example scenario:
		// 1. Dry commit ABC is made (commitTime: 10:00)
		// 2. All environments get hydrated for ABC
		// 3. Before production merges ABC, someone makes dry commit DEF (commitTime: 10:05)
		// 4. Staging hydrates and merges DEF
		// 5. Production is still trying to promote ABC, but staging is ahead
		// 6. Both have Note.DrySha = DEF (both hydrated up to the latest), so allow promotion
		It("allows when previous env has already merged a newer commit with matching Note.DrySha", func() {
			// Previous env (staging) has merged a newer commit (DEF at newerTime)
			prevEnvStatus := makeEnvStatusWithTime("DEF", "DEF", "DEF", newerTime)
			// Current env (production) is trying to promote an older commit (ABC at olderTime)
			// Both have Note.DrySha = "DEF", meaning they've both been hydrated up to the same point
			currEnvStatus := promoterv1alpha1.EnvironmentStatus{
				Active: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "OLD",
						CommitTime: olderTime,
					},
					CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
						{Key: "health", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
					},
				},
				Proposed: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "ABC",
						CommitTime: olderTime, // ABC was made before DEF
					},
					Note: &promoterv1alpha1.HydratorMetadata{
						DrySha: "DEF", // But hydrator has processed up to DEF
					},
				},
			}

			isPending, reason := isPreviousEnvironmentPending(prevEnvStatus, currEnvStatus)

			Expect(isPending).To(BeFalse(), "should allow promotion when previous env is ahead and Note.DrySha matches")
			Expect(reason).To(BeEmpty())
		})

		It("blocks when Note.DrySha doesn't match between environments", func() {
			// Previous env (staging) has Note.DrySha "XYZ" while production's is "ABC"
			// This means they haven't been hydrated for the same dry commits
			prevEnvStatus := makeEnvStatusWithTime("DEF", "DEF", "XYZ", newerTime)
			currEnvStatus := promoterv1alpha1.EnvironmentStatus{
				Active: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "OLD",
						CommitTime: olderTime,
					},
					CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
						{Key: "health", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
					},
				},
				Proposed: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "ABC",
						CommitTime: olderTime,
					},
					Note: &promoterv1alpha1.HydratorMetadata{
						DrySha: "ABC", // Different from staging's XYZ
					},
				},
			}

			isPending, reason := isPreviousEnvironmentPending(prevEnvStatus, currEnvStatus)

			Expect(isPending).To(BeTrue(), "should block when Note.DrySha doesn't match")
			Expect(reason).To(ContainSubstring("hydrator to finish processing"))
		})

		// Test for legacy hydrators (no git notes) when previous env is ahead
		It("allows when previous env is ahead with matching Proposed.Dry.Sha (legacy hydrator)", func() {
			// Previous env (staging) has merged a newer commit (DEF at newerTime)
			// Both environments use legacy hydrator (no Note.DrySha), so we compare Proposed.Dry.Sha
			prevEnvStatus := promoterv1alpha1.EnvironmentStatus{
				Active: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "DEF",
						CommitTime: newerTime,
					},
					CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
						{Key: "health", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
					},
				},
				Proposed: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha: "DEF",
					},
					// Note.DrySha is empty (legacy hydrator, no git notes)
				},
			}
			currEnvStatus := promoterv1alpha1.EnvironmentStatus{
				Active: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "OLD",
						CommitTime: olderTime,
					},
					CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
						{Key: "health", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
					},
				},
				Proposed: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "DEF", // Same as staging's Proposed.Dry.Sha
						CommitTime: olderTime,
					},
					// Note.DrySha is empty (legacy hydrator, no git notes)
				},
			}

			isPending, reason := isPreviousEnvironmentPending(prevEnvStatus, currEnvStatus)

			Expect(isPending).To(BeFalse(), "should allow promotion when previous env is ahead and Proposed.Dry.Sha matches (legacy)")
			Expect(reason).To(BeEmpty())
		})

		It("blocks when previous env is ahead but commit statuses are not passing", func() {
			// Previous env has merged a newer commit but health check is pending
			prevEnvStatus := promoterv1alpha1.EnvironmentStatus{
				Active: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "DEF",
						CommitTime: newerTime,
					},
					CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
						{Key: "health", Phase: string(promoterv1alpha1.CommitPhasePending)},
					},
				},
				Proposed: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha: "DEF",
					},
					Note: &promoterv1alpha1.HydratorMetadata{
						DrySha: "DEF",
					},
				},
			}
			currEnvStatus := promoterv1alpha1.EnvironmentStatus{
				Active: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "OLD",
						CommitTime: olderTime,
					},
					CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
						{Key: "health", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
					},
				},
				Proposed: promoterv1alpha1.CommitBranchState{
					Dry: promoterv1alpha1.CommitShaState{
						Sha:        "ABC",
						CommitTime: olderTime,
					},
					Note: &promoterv1alpha1.HydratorMetadata{
						DrySha: "DEF", // Note.DrySha matches staging
					},
				},
			}

			isPending, reason := isPreviousEnvironmentPending(prevEnvStatus, currEnvStatus)

			Expect(isPending).To(BeTrue(), "should block when previous env commit statuses are not passing")
			Expect(reason).To(ContainSubstring("commit status"))
		})
	})
})
