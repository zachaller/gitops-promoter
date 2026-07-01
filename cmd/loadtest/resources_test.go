package loadtest

import (
	"testing"
	"time"

	"github.com/expr-lang/expr"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

func TestInstances(t *testing.T) {
	t.Parallel()
	t.Run("count 1 uses the bare name", func(t *testing.T) {
		t.Parallel()
		instances := Instances(&Config{Name: "loadtest", Namespace: "default", Count: 1})
		if len(instances) != 1 {
			t.Fatalf("expected 1 instance, got %d", len(instances))
		}
		if instances[0].Name != "loadtest" {
			t.Errorf("expected name %q, got %q", "loadtest", instances[0].Name)
		}
		if instances[0].RepoName != "loadtest-repo" {
			t.Errorf("expected repo name %q, got %q", "loadtest-repo", instances[0].RepoName)
		}
	})

	t.Run("count > 1 suffixes with the index", func(t *testing.T) {
		t.Parallel()
		instances := Instances(&Config{Name: "loadtest", Namespace: "default", Count: 3})
		if len(instances) != 3 {
			t.Fatalf("expected 3 instances, got %d", len(instances))
		}
		wantNames := []string{"loadtest-1", "loadtest-2", "loadtest-3"}
		for i, inst := range instances {
			if inst.Name != wantNames[i] {
				t.Errorf("instance %d: expected name %q, got %q", i, wantNames[i], inst.Name)
			}
			if inst.Index != i+1 {
				t.Errorf("instance %d: expected index %d, got %d", i, i+1, inst.Index)
			}
		}
	})
}

func TestBuildPromotionStrategyWiring(t *testing.T) {
	t.Parallel()
	baseCfg := &Config{Name: "loadtest", Namespace: "default", Count: 1}
	inst := Instances(baseCfg)[0]

	t.Run("direct mode has no argocd-health gate", func(t *testing.T) {
		t.Parallel()
		cfg := *baseCfg
		cfg.Mode = ModeDirect
		ps := BuildPromotionStrategy(&cfg, inst)
		for _, cs := range ps.Spec.ActiveCommitStatuses {
			if cs.Key == ArgoCDHealthKey {
				t.Errorf("expected no global %q gate in direct mode", ArgoCDHealthKey)
			}
		}
	})

	t.Run("argocd mode gates every environment on argocd-health", func(t *testing.T) {
		t.Parallel()
		cfg := *baseCfg
		cfg.Mode = ModeArgoCD
		ps := BuildPromotionStrategy(&cfg, inst)
		found := false
		for _, cs := range ps.Spec.ActiveCommitStatuses {
			if cs.Key == ArgoCDHealthKey {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a global %q gate in argocd mode", ArgoCDHealthKey)
		}
	})

	t.Run("only production carries the change-management trio", func(t *testing.T) {
		t.Parallel()
		cfg := *baseCfg
		cfg.Mode = ModeDirect
		ps := BuildPromotionStrategy(&cfg, inst)
		if len(ps.Spec.Environments) != 3 {
			t.Fatalf("expected 3 environments, got %d", len(ps.Spec.Environments))
		}
		for i, env := range ps.Spec.Environments {
			hasOpen := selectorHasKey(env.ProposedCommitStatuses, ChangeManagementOpenKey)
			isLast := i == len(ps.Spec.Environments)-1
			if hasOpen != isLast {
				t.Errorf("environment %q: expected change-management-open presence=%v, got %v",
					env.Branch, isLast, hasOpen)
			}
		}
	})

	t.Run("every environment carries the revert-check and timer gates", func(t *testing.T) {
		t.Parallel()
		cfg := *baseCfg
		cfg.Mode = ModeDirect
		ps := BuildPromotionStrategy(&cfg, inst)
		if !selectorHasKey(ps.Spec.ProposedCommitStatuses, RevertCheckKey) {
			t.Error("expected a global revert-check proposed gate")
		}
		for _, env := range ps.Spec.Environments {
			if !selectorHasKey(env.ActiveCommitStatuses, TimerKey) {
				t.Errorf("environment %q: expected a timer active gate", env.Branch)
			}
		}
	})
}

func selectorHasKey(selectors []promoterv1alpha1.CommitStatusSelector, key string) bool {
	for _, s := range selectors {
		if s.Key == key {
			return true
		}
	}
	return false
}

func TestParseTimedDurations(t *testing.T) {
	t.Parallel()
	t.Run("valid input", func(t *testing.T) {
		t.Parallel()
		got, err := parseTimedDurations("30s,1m,2m")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := [3]time.Duration{30 * time.Second, time.Minute, 2 * time.Minute}
		if got != want {
			t.Errorf("expected %v, got %v", want, got)
		}
	})

	t.Run("wrong count", func(t *testing.T) {
		t.Parallel()
		if _, err := parseTimedDurations("30s,1m"); err == nil {
			t.Error("expected an error for wrong number of durations")
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		t.Parallel()
		if _, err := parseTimedDurations("30s,1m,not-a-duration"); err == nil {
			t.Error("expected an error for an invalid duration")
		}
	})
}

// TestRevertCheckExpression semantically exercises the GitCommitStatus expression (not just
// its syntax) against sample commit data, the same way the controller would evaluate it.
func TestRevertCheckExpression(t *testing.T) {
	t.Parallel()
	gcs := BuildGitCommitStatus(Instance{Name: "loadtest", Namespace: "default"})

	tests := []struct {
		name    string
		subject string
		body    string
		want    bool
	}{
		{"normal commit passes", "fix: handle nil pointer", "", true},
		{"revert in subject blocks", "Revert \"fix: handle nil pointer\"", "", false},
		{"lowercase revert in subject blocks", "revert the last change", "", false},
		{"revert in body blocks", "fix: handle nil pointer", "This reverts commit abc123.", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]any{
				"Commit": map[string]any{
					"Subject": tt.subject,
					"Body":    tt.body,
				},
			}
			program, err := expr.Compile(gcs.Spec.Expression, expr.Env(env), expr.AsBool())
			if err != nil {
				t.Fatalf("failed to compile: %v", err)
			}
			out, err := expr.Run(program, env)
			if err != nil {
				t.Fatalf("failed to run: %v", err)
			}
			got, ok := out.(bool)
			if !ok {
				t.Fatalf("expected a bool result, got %T", out)
			}
			if got != tt.want {
				t.Errorf("expected %v, got %v", tt.want, got)
			}
		})
	}
}
