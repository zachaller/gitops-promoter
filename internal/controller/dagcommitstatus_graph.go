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
	"errors"
	"fmt"

	"github.com/dominikbraun/graph"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

// buildDagGraph builds a directed graph from a DagCommitStatus's environments.
// Edges go from parent (dependsOn entry) to child (the environment).
// Returns an error if:
//   - a branch in dependsOn is not declared as an environment in the DAG
//   - an edge would create a cycle (graph.PreventCycles)
//   - an edge is a self-loop
//
// Validation against the referenced PromotionStrategy (every DAG branch must
// exist in the PS) is performed by the caller against psBranches.
func buildDagGraph(envs []promoterv1alpha1.DagEnvironment, psBranches map[string]struct{}) (graph.Graph[string, string], error) {
	g := graph.New(graph.StringHash, graph.Directed(), graph.PreventCycles())

	// Index DAG branches for parent existence checks.
	dagBranches := make(map[string]struct{}, len(envs))
	for _, env := range envs {
		dagBranches[env.Branch] = struct{}{}
	}

	for _, env := range envs {
		if env.Branch == "" {
			return nil, errors.New("environment branch must not be empty")
		}
		if _, ok := psBranches[env.Branch]; !ok {
			return nil, fmt.Errorf("branch %q is not declared in the referenced PromotionStrategy", env.Branch)
		}
		if err := g.AddVertex(env.Branch); err != nil {
			return nil, fmt.Errorf("failed to add vertex %q: %w", env.Branch, err)
		}
	}

	for _, env := range envs {
		for _, parent := range env.DependsOn {
			if parent == env.Branch {
				return nil, fmt.Errorf("environment %q cannot depend on itself", env.Branch)
			}
			if _, ok := dagBranches[parent]; !ok {
				return nil, fmt.Errorf("environment %q depends on unknown branch %q", env.Branch, parent)
			}
			if err := g.AddEdge(parent, env.Branch); err != nil {
				if errors.Is(err, graph.ErrEdgeCreatesCycle) {
					return nil, fmt.Errorf("dependency %s -> %s would create a cycle", parent, env.Branch)
				}
				return nil, fmt.Errorf("failed to add edge %s -> %s: %w", parent, env.Branch, err)
			}
		}
	}

	return g, nil
}

// parentBranchesOf returns the parent branches of a given vertex in the DAG.
func parentBranchesOf(g graph.Graph[string, string], branch string) ([]string, error) {
	predecessorMap, err := g.PredecessorMap()
	if err != nil {
		return nil, fmt.Errorf("failed to compute predecessor map: %w", err)
	}
	preds := predecessorMap[branch]
	out := make([]string, 0, len(preds))
	for parent := range preds {
		out = append(out, parent)
	}
	return out, nil
}

// nonRootBranches returns the branches in the DAG that have at least one parent.
// Order is the same as the DAG spec's environments order.
func nonRootBranches(envs []promoterv1alpha1.DagEnvironment) []string {
	out := make([]string, 0, len(envs))
	for _, env := range envs {
		if len(env.DependsOn) == 0 {
			continue
		}
		out = append(out, env.Branch)
	}
	return out
}

// parentChainResult is the per-parent-chain aggregation of a single dependsOn edge.
type parentChainResult struct {
	// Pending is true if the chain is not (yet) successful.
	Pending bool
	// Reason describes why the chain is pending, if it is.
	Reason string
	// Phase is the per-parent rolled-up phase used for the disaggregated status mirror.
	Phase promoterv1alpha1.CommitStatusPhase
	// CommitStatuses is the parent's active commit statuses (at the matched dry SHA),
	// surfaced for the disaggregated status mirror.
	CommitStatuses []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase
}

// evaluateParentChain checks one parent of a gated environment, walking the chain
// upstream as needed. The semantics mirror the original isPreviousEnvironmentPending,
// generalised to graph traversal: at each node, if the node is a no-op with no
// pending changes and is healthy, the recursion fans out across that node's parents.
//
// Parameters:
//   - g: the dependency graph.
//   - statusByBranch: map from PromotionStrategy environment branch to its EnvironmentStatus.
//   - parentBranch: the dependsOn parent currently being evaluated.
//   - targetDrySha: the dry SHA that the gated (downstream) environment was hydrated for.
//   - currentActiveCommitTime: the gated environment's active dry commit time, used for the
//     "previous env's commit time must be equal-or-newer" sanity check.
func evaluateParentChain(
	g graph.Graph[string, string],
	statusByBranch map[string]promoterv1alpha1.EnvironmentStatus,
	parentBranch string,
	targetDrySha string,
	currentActiveCommitTime metav1.Time,
) parentChainResult {
	envStatus, ok := statusByBranch[parentBranch]
	if !ok {
		return parentChainResult{
			Pending: true,
			Reason:  fmt.Sprintf("Waiting for parent environment %q to appear in PromotionStrategy status", parentBranch),
			Phase:   promoterv1alpha1.CommitPhasePending,
		}
	}

	envHydratedForDrySha := getEffectiveHydratedDrySha(envStatus)
	envProposedDrySha := envStatus.Proposed.Dry.Sha

	// The hydrator hasn't processed the same dry SHA yet — we have to wait.
	if envHydratedForDrySha != targetDrySha {
		return parentChainResult{
			Pending:        true,
			Reason:         "Waiting for the hydrator to finish processing the proposed dry commit",
			Phase:          promoterv1alpha1.CommitPhasePending,
			CommitStatuses: envStatus.Active.CommitStatuses,
		}
	}

	envMergedTarget := envStatus.Active.Dry.Sha == targetDrySha

	if envMergedTarget {
		envDryShaEqualOrNewer := envStatus.Active.Dry.CommitTime.Equal(&metav1.Time{Time: currentActiveCommitTime.Time}) ||
			envStatus.Active.Dry.CommitTime.After(currentActiveCommitTime.Time)
		if !envDryShaEqualOrNewer {
			return parentChainResult{
				Pending:        true,
				Reason:         "Previous environment's commit is older than current environment's commit",
				Phase:          promoterv1alpha1.CommitPhasePending,
				CommitStatuses: envStatus.Active.CommitStatuses,
			}
		}

		isPending, reason := checkCommitStatusesPassing(envStatus.Active.CommitStatuses, envStatus.Branch)
		phase := promoterv1alpha1.CommitPhaseSuccess
		if isPending {
			phase = promoterv1alpha1.CommitPhasePending
		}
		return parentChainResult{
			Pending:        isPending,
			Reason:         reason,
			Phase:          phase,
			CommitStatuses: envStatus.Active.CommitStatuses,
		}
	}

	envIsNoOp := envHydratedForDrySha != envProposedDrySha
	envHasPendingChanges := envStatus.Active.Dry.Sha != envProposedDrySha

	if !envIsNoOp || envHasPendingChanges {
		return parentChainResult{
			Pending:        true,
			Reason:         "Waiting for previous environment to be promoted",
			Phase:          promoterv1alpha1.CommitPhasePending,
			CommitStatuses: envStatus.Active.CommitStatuses,
		}
	}

	if isPend, reason := checkCommitStatusesPassing(envStatus.Active.CommitStatuses, envStatus.Branch); isPend {
		return parentChainResult{
			Pending:        true,
			Reason:         reason,
			Phase:          promoterv1alpha1.CommitPhasePending,
			CommitStatuses: envStatus.Active.CommitStatuses,
		}
	}

	// No-op + healthy: recurse across this node's own parents. If it has no parents,
	// the chain is considered satisfied (root reached).
	grandparents, err := parentBranchesOf(g, parentBranch)
	if err != nil {
		return parentChainResult{
			Pending:        true,
			Reason:         fmt.Sprintf("Failed to walk graph parents: %v", err),
			Phase:          promoterv1alpha1.CommitPhasePending,
			CommitStatuses: envStatus.Active.CommitStatuses,
		}
	}
	if len(grandparents) == 0 {
		return parentChainResult{
			Pending:        false,
			Phase:          promoterv1alpha1.CommitPhaseSuccess,
			CommitStatuses: envStatus.Active.CommitStatuses,
		}
	}
	for _, gp := range grandparents {
		res := evaluateParentChain(g, statusByBranch, gp, targetDrySha, currentActiveCommitTime)
		if res.Pending {
			return parentChainResult{
				Pending:        true,
				Reason:         res.Reason,
				Phase:          promoterv1alpha1.CommitPhasePending,
				CommitStatuses: envStatus.Active.CommitStatuses,
			}
		}
	}
	return parentChainResult{
		Pending:        false,
		Phase:          promoterv1alpha1.CommitPhaseSuccess,
		CommitStatuses: envStatus.Active.CommitStatuses,
	}
}

// flattenParentCommitStatuses returns the union of all parents' active CommitStatuses
// keyed by the existing per-key map shape, for back-compat with the existing
// CommitStatusPreviousEnvironmentStatusesAnnotation. With multiple parents,
// keys may collide; later parents win — by then the canonical place to look is
// DagCommitStatus.status.environments[].parents[].
func flattenParentCommitStatuses(parents []promoterv1alpha1.DagParentStatus) map[string]string {
	out := make(map[string]string)
	for _, p := range parents {
		for _, cs := range p.CommitStatuses {
			out[cs.Key] = cs.Phase
		}
	}
	return out
}

// hasReferencedKey reports whether the gated env in the PromotionStrategy
// references `key` in its proposedCommitStatuses (env-level or spec-level).
func hasReferencedKey(ps *promoterv1alpha1.PromotionStrategy, branch, key string) bool {
	for _, sel := range ps.Spec.ProposedCommitStatuses {
		if sel.Key == key {
			return true
		}
	}
	_, env := utils.GetEnvironmentByBranch(*ps, branch)
	if env == nil {
		return false
	}
	for _, sel := range env.ProposedCommitStatuses {
		if sel.Key == key {
			return true
		}
	}
	return false
}
