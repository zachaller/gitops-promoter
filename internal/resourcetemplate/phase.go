/*
Copyright 2026.

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

package resourcetemplate

import (
	"fmt"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/golang/groupcache/lru"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// maxCompiledExpressionCacheEntries bounds the compiled phase expression cache.
const maxCompiledExpressionCacheEntries = 1024

// PhaseEvaluator compiles and runs spec.phase.expression programs, caching compiled programs by expression text.
// Safe for concurrent use.
type PhaseEvaluator struct {
	cache *lru.Cache
	mu    sync.Mutex
}

// NewPhaseEvaluator returns an evaluator with an empty compile cache.
func NewPhaseEvaluator() *PhaseEvaluator {
	return &PhaseEvaluator{cache: lru.New(maxCompiledExpressionCacheEntries)}
}

// PhaseInput is the variable set exposed to spec.phase.expression. See KubernetesResourcePhaseSpec.
type PhaseInput struct {
	Resource                       map[string]any
	Environment                    *promoterv1alpha1.EnvironmentStatus
	PromotionStrategy              *promoterv1alpha1.PromotionStrategy
	KubernetesResourceCommitStatus *promoterv1alpha1.KubernetesResourceCommitStatus
	Branch                         string
	Sha                            string
	Phase                          string
}

func (in PhaseInput) env() map[string]any {
	// A typed nil map would still be non-nil inside expr, so pass an untyped nil when the resource is missing.
	var resource any
	if in.Resource != nil {
		resource = in.Resource
	}
	return map[string]any{
		"Resource":                       resource,
		"Branch":                         in.Branch,
		"Sha":                            in.Sha,
		"Phase":                          in.Phase,
		"Environment":                    in.Environment,
		"PromotionStrategy":              in.PromotionStrategy,
		"KubernetesResourceCommitStatus": in.KubernetesResourceCommitStatus,
	}
}

func (e *PhaseEvaluator) compile(expression string) (*vm.Program, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := e.cache.Get(expression); ok {
		if program, ok := v.(*vm.Program); ok {
			return program, nil
		}
	}
	program, err := expr.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("compile phase expression: %w", err)
	}
	e.cache.Add(expression, program)
	return program, nil
}

// EvaluatePhase runs expression against in and maps the result to a commit status phase. The expression may
// return a bool (true is success, false is pending), one of the phase strings, or an object { phase }.
func (e *PhaseEvaluator) EvaluatePhase(expression string, in PhaseInput) (promoterv1alpha1.CommitStatusPhase, error) {
	program, err := e.compile(expression)
	if err != nil {
		return promoterv1alpha1.CommitPhasePending, err
	}
	output, err := expr.Run(program, in.env())
	if err != nil {
		return promoterv1alpha1.CommitPhasePending, fmt.Errorf("evaluate phase expression: %w", err)
	}
	return phaseFromOutput(output)
}

func phaseFromOutput(output any) (promoterv1alpha1.CommitStatusPhase, error) {
	switch v := output.(type) {
	case bool:
		if v {
			return promoterv1alpha1.CommitPhaseSuccess, nil
		}
		return promoterv1alpha1.CommitPhasePending, nil
	case string:
		return parsePhase(v)
	case map[string]any:
		raw, ok := v["phase"]
		if !ok || raw == nil {
			return promoterv1alpha1.CommitPhasePending, nil
		}
		s, ok := raw.(string)
		if !ok {
			return promoterv1alpha1.CommitPhasePending, fmt.Errorf("phase expression object field phase must be a string, got %T", raw)
		}
		return parsePhase(s)
	default:
		return promoterv1alpha1.CommitPhasePending, fmt.Errorf("phase expression must return bool, string, or object { phase }, got %T", output)
	}
}

func parsePhase(s string) (promoterv1alpha1.CommitStatusPhase, error) {
	switch promoterv1alpha1.CommitStatusPhase(s) {
	case "", promoterv1alpha1.CommitPhasePending:
		return promoterv1alpha1.CommitPhasePending, nil
	case promoterv1alpha1.CommitPhaseSuccess:
		return promoterv1alpha1.CommitPhaseSuccess, nil
	case promoterv1alpha1.CommitPhaseFailure:
		return promoterv1alpha1.CommitPhaseFailure, nil
	default:
		return promoterv1alpha1.CommitPhasePending, fmt.Errorf("phase expression returned unknown phase %q; want success, pending, or failure", s)
	}
}
