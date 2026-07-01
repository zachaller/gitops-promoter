package loadtest

import (
	"errors"
	"fmt"
	"time"
)

// Mode selects how the gating CRDs are deployed and who performs hydration.
type Mode string

const (
	// ModeDirect applies the gating CRDs directly to the cluster and has the tool itself
	// act as a one-shot "fake hydrator" for the *-next branches. No Argo CD is involved.
	ModeDirect Mode = "direct"
	// ModeArgoCD pushes the gating CRDs into the repo's promotion-app/ directory and relies
	// on Argo CD (sourceHydrator + a plain-sync Application) to deploy and hydrate them.
	ModeArgoCD Mode = "argocd"
)

// environments is the fixed set of environments every loadtest instance is wired with,
// matching the branch naming already used in the argocon-demo / ttt.yaml reference (plural
// "environments/").
var environments = []string{"development", "staging", "production"}

// EnvironmentBranch returns the active branch name for env, e.g. "environments/development".
func EnvironmentBranch(env string) string {
	return "environments/" + env
}

// EnvironmentNextBranch returns the proposed ("-next") branch name for env.
func EnvironmentNextBranch(env string) string {
	return EnvironmentBranch(env) + "-next"
}

// Config holds all flags shared across the setup/teardown/bump subcommands.
//
// Field order groups same-size fields together (fieldalignment): strings, then the enum-typed
// Mode, then ints, then the fixed-size TimedDurations array.
type Config struct {
	// Name is the resource/repo name prefix.
	Name string
	// Namespace is the Kubernetes namespace all resources are created in.
	Namespace string
	// Owner is the GitHub org/user repos are created under.
	Owner string
	// GitHubToken is a personal access token used for repo creation and git push/clone.
	GitHubToken string
	// GitHubAppPrivateKeyPath is a path to the GitHub App's private key (.pem).
	GitHubAppPrivateKeyPath string
	// GitHubAppPrivateKey is the resolved contents of GitHubAppPrivateKeyPath, read once at
	// startup and reused for every instance's Secret.
	GitHubAppPrivateKey string
	// ChangeMgmtBaseURL is the base URL the change-management WebRequestCommitStatus trio
	// calls. Defaults to a locally-run mock service; running that service is out of scope
	// for this tool.
	ChangeMgmtBaseURL string
	// Mode selects direct vs argocd deployment (see Mode).
	Mode Mode
	// Count is the number of independent instances to operate on.
	Count int
	// GitHubAppID is the GitHub App ID used by the ScmProvider for SCM API calls.
	GitHubAppID int64
	// GitHubInstallationID is optional; when zero it is inferred from the repo owner.
	GitHubInstallationID int64
	// TimedDurations are the durations (development, staging, production) used by the
	// TimedCommitStatus gate. Deliberately short by default so the gate flips during a session.
	TimedDurations [3]time.Duration
}

// Validate checks that the config is internally consistent.
func (c *Config) Validate() error {
	if c.Name == "" {
		return errors.New("--name must not be empty")
	}
	if c.Count < 1 {
		return errors.New("--count must be >= 1")
	}
	if c.Mode != ModeDirect && c.Mode != ModeArgoCD {
		return fmt.Errorf("--mode must be %q or %q, got %q", ModeDirect, ModeArgoCD, c.Mode)
	}
	return nil
}

// Instance holds all the derived, per-instance names for a single loadtest environment.
//
// Field order groups strings together and the int Index last (fieldalignment).
type Instance struct {
	// Name is the instance name, e.g. "loadtest" (Count==1) or "loadtest-2" (Count>1).
	Name string
	// RepoName is the GitHub repository name for this instance.
	RepoName string
	// Namespace is the Kubernetes namespace this instance's resources live in.
	Namespace string
	// Index is the 1-based instance index (only meaningful when Config.Count > 1).
	Index int
}

// Instances computes the ordered list of instances for cfg, one per --count.
func Instances(cfg *Config) []Instance {
	instances := make([]Instance, 0, cfg.Count)
	for i := 1; i <= cfg.Count; i++ {
		name := cfg.Name
		if cfg.Count > 1 {
			name = fmt.Sprintf("%s-%d", cfg.Name, i)
		}
		instances = append(instances, Instance{
			Index:     i,
			Name:      name,
			RepoName:  name + "-repo",
			Namespace: cfg.Namespace,
		})
	}
	return instances
}

// SecretName is the name of the Kubernetes Secret holding the GitHub App private key.
func (inst Instance) SecretName() string { return inst.Name + "-scm-secret" }

// ScmProviderName is the name of the ScmProvider CR.
func (inst Instance) ScmProviderName() string { return inst.Name + "-scm" }

// PromotionStrategyName is the name of the PromotionStrategy CR.
func (inst Instance) PromotionStrategyName() string { return inst.Name + "-ps" }

// TimedCommitStatusName is the name of the TimedCommitStatus CR.
func (inst Instance) TimedCommitStatusName() string { return inst.Name + "-timer" }

// GitCommitStatusName is the name of the revert-check GitCommitStatus CR.
func (inst Instance) GitCommitStatusName() string { return inst.Name + "-revert-check" }

// ArgoCDCommitStatusName is the name of the ArgoCDCommitStatus CR (argocd mode only).
func (inst Instance) ArgoCDCommitStatusName() string { return inst.Name + "-argocd-health" }

// WebRequestCommitStatusName is the name of one of the three change-management
// WebRequestCommitStatus CRs (key is one of "open", "approval", "close").
func (inst Instance) WebRequestCommitStatusName(key string) string {
	return inst.Name + "-change-management-" + key
}

const (
	// RevertCheckKey is the commit-status key for the revert-keyword GitCommitStatus gate.
	RevertCheckKey = "revert-check"
	// TimerKey is the commit-status key for the TimedCommitStatus gate.
	TimerKey = "timer"
	// ArgoCDHealthKey is the commit-status key for the ArgoCDCommitStatus gate.
	ArgoCDHealthKey = "argocd-health"
	// ChangeManagementOpenKey is the commit-status key for the change-management-open WRCS.
	ChangeManagementOpenKey = "change-management-open"
	// ChangeManagementApprovalKey is the commit-status key for the change-management-approval WRCS.
	ChangeManagementApprovalKey = "change-management-approval"
	// ChangeManagementCloseKey is the commit-status key for the change-management-close WRCS.
	ChangeManagementCloseKey = "change-management-close"
)

// PromotionAppDir is the directory (relative to the repo root) that groups all the gating
// CRD manifests together, mirroring the argocon-gitops-promoter-hydrate-demo reference repo.
const PromotionAppDir = "promotion-app"

// TouchFileName is the empty marker file bumped (touched) to make a cheap no-op dry commit.
const TouchFileName = PromotionAppDir + "/touch"
