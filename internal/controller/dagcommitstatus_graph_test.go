/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

func psBranchesSet(branches ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(branches))
	for _, b := range branches {
		out[b] = struct{}{}
	}
	return out
}

func TestBuildDagGraph_LinearChain(t *testing.T) {
	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "dev"},
		{Branch: "staging", DependsOn: []string{"dev"}},
		{Branch: "prod", DependsOn: []string{"staging"}},
	}
	g, err := buildDagGraph(envs, psBranchesSet("dev", "staging", "prod"))
	require.NoError(t, err)
	require.NotNil(t, g)

	parents, err := parentBranchesOf(g, "prod")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"staging"}, parents)
}

func TestBuildDagGraph_Diamond(t *testing.T) {
	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "dev"},
		{Branch: "qa1", DependsOn: []string{"dev"}},
		{Branch: "qa2", DependsOn: []string{"dev"}},
		{Branch: "prod", DependsOn: []string{"qa1", "qa2"}},
	}
	g, err := buildDagGraph(envs, psBranchesSet("dev", "qa1", "qa2", "prod"))
	require.NoError(t, err)

	parents, err := parentBranchesOf(g, "prod")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"qa1", "qa2"}, parents)
}

func TestBuildDagGraph_RejectsCycle(t *testing.T) {
	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "a", DependsOn: []string{"c"}},
		{Branch: "b", DependsOn: []string{"a"}},
		{Branch: "c", DependsOn: []string{"b"}},
	}
	_, err := buildDagGraph(envs, psBranchesSet("a", "b", "c"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

func TestBuildDagGraph_RejectsSelfRef(t *testing.T) {
	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "dev", DependsOn: []string{"dev"}},
	}
	_, err := buildDagGraph(envs, psBranchesSet("dev"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "depend on itself")
}

func TestBuildDagGraph_RejectsUnknownDependsOn(t *testing.T) {
	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "dev"},
		{Branch: "prod", DependsOn: []string{"ghost"}},
	}
	_, err := buildDagGraph(envs, psBranchesSet("dev", "prod"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown branch")
}

func TestBuildDagGraph_RejectsBranchNotInPromotionStrategy(t *testing.T) {
	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "dev"},
		{Branch: "missing-from-ps", DependsOn: []string{"dev"}},
	}
	_, err := buildDagGraph(envs, psBranchesSet("dev"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not declared in the referenced PromotionStrategy")
}

// makeEnvStatus is a tiny constructor for EnvironmentStatus that exercises the chain helpers.
func makeEnvStatus(branch, activeDrySha, proposedDrySha, hydratedNoteSha string, activeStatuses []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase, commitTime time.Time) promoterv1alpha1.EnvironmentStatus {
	es := promoterv1alpha1.EnvironmentStatus{
		Branch: branch,
		Active: promoterv1alpha1.CommitBranchState{
			Dry: promoterv1alpha1.CommitShaState{
				Sha:        activeDrySha,
				CommitTime: metav1.NewTime(commitTime),
			},
			CommitStatuses: activeStatuses,
		},
		Proposed: promoterv1alpha1.CommitBranchState{
			Dry: promoterv1alpha1.CommitShaState{Sha: proposedDrySha},
		},
	}
	if hydratedNoteSha != "" {
		es.Proposed.Note = &promoterv1alpha1.HydratorMetadata{DrySha: hydratedNoteSha}
	}
	return es
}

// TestEvaluateParentChain_DiamondAllSuccess verifies that prod with two diamond parents
// resolves to success when both parent chains are healthy.
func TestEvaluateParentChain_DiamondAllSuccess(t *testing.T) {
	now := time.Now()
	target := "drysha-target"
	successCS := []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{{Key: "health", Phase: "success"}}

	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "dev"},
		{Branch: "qa1", DependsOn: []string{"dev"}},
		{Branch: "qa2", DependsOn: []string{"dev"}},
		{Branch: "prod", DependsOn: []string{"qa1", "qa2"}},
	}
	g, err := buildDagGraph(envs, psBranchesSet("dev", "qa1", "qa2", "prod"))
	require.NoError(t, err)

	statusByBranch := map[string]promoterv1alpha1.EnvironmentStatus{
		"dev":  makeEnvStatus("dev", target, target, "", successCS, now),
		"qa1":  makeEnvStatus("qa1", target, target, "", successCS, now),
		"qa2":  makeEnvStatus("qa2", target, target, "", successCS, now),
		"prod": makeEnvStatus("prod", "older", target, "", nil, now),
	}

	for _, parent := range []string{"qa1", "qa2"} {
		res := evaluateParentChain(g, statusByBranch, parent, target, metav1.NewTime(now))
		assert.False(t, res.Pending, "%s chain should not be pending", parent)
		assert.Equal(t, promoterv1alpha1.CommitPhaseSuccess, res.Phase)
	}
}

// TestEvaluateParentChain_DiamondOneFailing verifies that if one of two diamond parents
// is pending, the gated env's chain reflects pending.
func TestEvaluateParentChain_DiamondOneFailing(t *testing.T) {
	now := time.Now()
	target := "drysha-target"
	successCS := []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{{Key: "health", Phase: "success"}}
	pendingCS := []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{{Key: "health", Phase: "pending"}}

	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "dev"},
		{Branch: "qa1", DependsOn: []string{"dev"}},
		{Branch: "qa2", DependsOn: []string{"dev"}},
		{Branch: "prod", DependsOn: []string{"qa1", "qa2"}},
	}
	g, err := buildDagGraph(envs, psBranchesSet("dev", "qa1", "qa2", "prod"))
	require.NoError(t, err)

	statusByBranch := map[string]promoterv1alpha1.EnvironmentStatus{
		"dev":  makeEnvStatus("dev", target, target, "", successCS, now),
		"qa1":  makeEnvStatus("qa1", target, target, "", successCS, now),
		"qa2":  makeEnvStatus("qa2", target, target, "", pendingCS, now),
		"prod": makeEnvStatus("prod", "older", target, "", nil, now),
	}

	qa2 := evaluateParentChain(g, statusByBranch, "qa2", target, metav1.NewTime(now))
	assert.True(t, qa2.Pending)
	assert.Equal(t, promoterv1alpha1.CommitPhasePending, qa2.Phase)
}

// TestEvaluateParentChain_HydratorBehind verifies that if a parent hasn't been hydrated
// for the target dry SHA, the chain is pending with the hydrator-waiting reason.
func TestEvaluateParentChain_HydratorBehind(t *testing.T) {
	now := time.Now()
	target := "drysha-target"
	successCS := []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{{Key: "health", Phase: "success"}}

	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "dev"},
		{Branch: "staging", DependsOn: []string{"dev"}},
	}
	g, err := buildDagGraph(envs, psBranchesSet("dev", "staging"))
	require.NoError(t, err)

	statusByBranch := map[string]promoterv1alpha1.EnvironmentStatus{
		"dev":     makeEnvStatus("dev", "older", "older", "", successCS, now),
		"staging": makeEnvStatus("staging", "older", target, "", nil, now),
	}

	res := evaluateParentChain(g, statusByBranch, "dev", target, metav1.NewTime(now))
	assert.True(t, res.Pending)
	assert.Contains(t, res.Reason, "hydrator")
}

// TestEvaluateParentChain_NoOpRecursesUpstream verifies the no-op + recurse behavior:
// when a node is a no-op for the target and is healthy, evaluation walks its parents.
func TestEvaluateParentChain_NoOpRecursesUpstream(t *testing.T) {
	now := time.Now()
	target := "drysha-target"
	successCS := []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{{Key: "health", Phase: "success"}}

	envs := []promoterv1alpha1.DagEnvironment{
		{Branch: "dev"},
		{Branch: "staging", DependsOn: []string{"dev"}},
		{Branch: "prod", DependsOn: []string{"staging"}},
	}
	g, err := buildDagGraph(envs, psBranchesSet("dev", "staging", "prod"))
	require.NoError(t, err)

	// staging is a no-op for the target: hydrated for `target` (via Note) but its proposed.dry
	// still says "older"; staging's active matches "older" (no pending change). Dev actually
	// merged the target and is healthy.
	statusByBranch := map[string]promoterv1alpha1.EnvironmentStatus{
		"dev":     makeEnvStatus("dev", target, target, "", successCS, now),
		"staging": makeEnvStatus("staging", "older", "older", target, successCS, now),
		"prod":    makeEnvStatus("prod", "ancient", target, "", nil, now),
	}

	res := evaluateParentChain(g, statusByBranch, "staging", target, metav1.NewTime(now))
	assert.False(t, res.Pending, "no-op staging with healthy dev upstream should not be pending; reason=%s", res.Reason)
	assert.Equal(t, promoterv1alpha1.CommitPhaseSuccess, res.Phase)
}

func TestHasReferencedKey(t *testing.T) {
	ps := &promoterv1alpha1.PromotionStrategy{
		Spec: promoterv1alpha1.PromotionStrategySpec{
			ProposedCommitStatuses: []promoterv1alpha1.CommitStatusSelector{{Key: "spec-level"}},
			Environments: []promoterv1alpha1.Environment{
				{Branch: "dev"},
				{Branch: "prod", ProposedCommitStatuses: []promoterv1alpha1.CommitStatusSelector{{Key: "env-level"}}},
			},
		},
	}
	assert.True(t, hasReferencedKey(ps, "prod", "spec-level"))
	assert.True(t, hasReferencedKey(ps, "prod", "env-level"))
	assert.False(t, hasReferencedKey(ps, "prod", "missing"))
	assert.True(t, hasReferencedKey(ps, "dev", "spec-level"))
	// dev does not have an env-level reference; spec-level only is what matters here.
	assert.False(t, hasReferencedKey(ps, "dev", "env-level"))
}

func TestFlattenParentCommitStatuses(t *testing.T) {
	parents := []promoterv1alpha1.DagParentStatus{
		{
			Branch: "qa1",
			CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				{Key: "health", Phase: "success"},
			},
		},
		{
			Branch: "qa2",
			CommitStatuses: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				{Key: "perf", Phase: "pending"},
			},
		},
	}
	flat := flattenParentCommitStatuses(parents)
	assert.Equal(t, "success", flat["health"])
	assert.Equal(t, "pending", flat["perf"])
	assert.Len(t, flat, 2)
}
