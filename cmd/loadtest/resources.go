package loadtest

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// applyObject creates obj, or updates it in place (preserving resourceVersion) if it already
// exists. Setup is intentionally not additive: re-running it against an existing instance
// converges that instance's spec to whatever this run computes.
func applyObject(ctx context.Context, c client.Client, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object) //nolint:forcetypeassert // client.Object types are always convertible
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := c.Create(ctx, obj); err != nil {
			return fmt.Errorf("failed to create %s %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind,
				obj.GetNamespace(), obj.GetName(), err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("failed to get %s %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetNamespace(), obj.GetName(), err)
	}

	obj.SetResourceVersion(existing.GetResourceVersion())
	if err := c.Update(ctx, obj); err != nil {
		return fmt.Errorf("failed to update %s %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}

// BuildScmProvider builds the ScmProvider CR. Bootstrap-only: never promoted, always applied
// directly regardless of Config.Mode.
func BuildScmProvider(cfg *Config, inst Instance) *promoterv1alpha1.ScmProvider {
	return &promoterv1alpha1.ScmProvider{
		TypeMeta:   promoterTypeMeta("ScmProvider"),
		ObjectMeta: metav1.ObjectMeta{Name: inst.ScmProviderName(), Namespace: inst.Namespace},
		Spec: promoterv1alpha1.ScmProviderSpec{
			SecretRef: &corev1.LocalObjectReference{Name: inst.SecretName()},
			GitHub: &promoterv1alpha1.GitHub{
				AppID:          cfg.GitHubAppID,
				InstallationID: cfg.GitHubInstallationID,
			},
		},
	}
}

// BuildGitRepository builds the GitRepository CR. Bootstrap-only: never promoted, always
// applied directly regardless of Config.Mode.
func BuildGitRepository(inst Instance, owner string) *promoterv1alpha1.GitRepository {
	return &promoterv1alpha1.GitRepository{
		TypeMeta:   promoterTypeMeta("GitRepository"),
		ObjectMeta: metav1.ObjectMeta{Name: inst.RepoName, Namespace: inst.Namespace},
		Spec: promoterv1alpha1.GitRepositorySpec{
			GitHub: &promoterv1alpha1.GitHubRepo{
				Owner: owner,
				Name:  inst.RepoName,
			},
			ScmProviderRef: promoterv1alpha1.ScmProviderObjectReference{
				Kind: "ScmProvider",
				Name: inst.ScmProviderName(),
			},
		},
	}
}

// BuildPromotionStrategy builds the PromotionStrategy CR wiring together every gate this tool
// creates: revert-check (global proposed), timer (per-environment active), the change-management
// trio (production only), and - in ModeArgoCD - argocd-health (global active).
func BuildPromotionStrategy(cfg *Config, inst Instance) *promoterv1alpha1.PromotionStrategy {
	autoMergeTrue := true

	proposed := []promoterv1alpha1.CommitStatusSelector{{Key: RevertCheckKey}}
	var active []promoterv1alpha1.CommitStatusSelector
	if cfg.Mode == ModeArgoCD {
		active = append(active, promoterv1alpha1.CommitStatusSelector{Key: ArgoCDHealthKey})
	}

	envs := make([]promoterv1alpha1.Environment, 0, len(environments))
	for i, env := range environments {
		e := promoterv1alpha1.Environment{
			Branch:    EnvironmentBranch(env),
			AutoMerge: &autoMergeTrue,
			ActiveCommitStatuses: []promoterv1alpha1.CommitStatusSelector{
				{Key: TimerKey},
			},
		}
		if i == len(environments)-1 {
			// Production: mirror the existing argocon-demo/ttt.yaml wiring for the
			// change-management trio.
			e.ProposedCommitStatuses = []promoterv1alpha1.CommitStatusSelector{
				{Key: ChangeManagementOpenKey},
				{Key: ChangeManagementApprovalKey},
			}
			e.ActiveCommitStatuses = append(e.ActiveCommitStatuses,
				promoterv1alpha1.CommitStatusSelector{Key: ChangeManagementCloseKey})
		}
		envs = append(envs, e)
	}

	return &promoterv1alpha1.PromotionStrategy{
		TypeMeta:   promoterTypeMeta("PromotionStrategy"),
		ObjectMeta: metav1.ObjectMeta{Name: inst.PromotionStrategyName(), Namespace: inst.Namespace},
		Spec: promoterv1alpha1.PromotionStrategySpec{
			RepositoryReference:    promoterv1alpha1.ObjectReference{Name: inst.RepoName},
			ActiveCommitStatuses:   active,
			ProposedCommitStatuses: proposed,
			Environments:           envs,
		},
	}
}

// BuildTimedCommitStatus builds the TimedCommitStatus CR gating each environment with
// Config.TimedDurations.
func BuildTimedCommitStatus(cfg *Config, inst Instance) *promoterv1alpha1.TimedCommitStatus {
	envs := make([]promoterv1alpha1.TimedCommitStatusEnvironments, 0, len(environments))
	for i, env := range environments {
		envs = append(envs, promoterv1alpha1.TimedCommitStatusEnvironments{
			Branch:   EnvironmentBranch(env),
			Duration: metav1.Duration{Duration: cfg.TimedDurations[i]},
		})
	}
	return &promoterv1alpha1.TimedCommitStatus{
		TypeMeta:   promoterTypeMeta("TimedCommitStatus"),
		ObjectMeta: metav1.ObjectMeta{Name: inst.TimedCommitStatusName(), Namespace: inst.Namespace},
		Spec: promoterv1alpha1.TimedCommitStatusSpec{
			PromotionStrategyRef: promoterv1alpha1.ObjectReference{Name: inst.PromotionStrategyName()},
			Key:                  TimerKey,
			Environments:         envs,
		},
	}
}

// BuildGitCommitStatus builds the GitCommitStatus CR that blocks promotion when the active
// commit's message contains the word "revert" (case-insensitive).
func BuildGitCommitStatus(inst Instance) *promoterv1alpha1.GitCommitStatus {
	return &promoterv1alpha1.GitCommitStatus{
		TypeMeta:   promoterTypeMeta("GitCommitStatus"),
		ObjectMeta: metav1.ObjectMeta{Name: inst.GitCommitStatusName(), Namespace: inst.Namespace},
		Spec: promoterv1alpha1.GitCommitStatusSpec{
			PromotionStrategyRef: promoterv1alpha1.ObjectReference{Name: inst.PromotionStrategyName()},
			Key:                  RevertCheckKey,
			Description:          "Blocks promotion if the active commit message contains \"revert\"",
			Target:               "active",
			Expression:           `!(lower(Commit.Subject) contains "revert" || lower(Commit.Body) contains "revert")`,
		},
	}
}

// BuildArgoCDCommitStatus builds the ArgoCDCommitStatus CR (ModeArgoCD only), selecting
// Applications labeled loadtest-instance=<inst.Name>.
func BuildArgoCDCommitStatus(inst Instance) *promoterv1alpha1.ArgoCDCommitStatus {
	return &promoterv1alpha1.ArgoCDCommitStatus{
		TypeMeta:   promoterTypeMeta("ArgoCDCommitStatus"),
		ObjectMeta: metav1.ObjectMeta{Name: inst.ArgoCDCommitStatusName(), Namespace: inst.Namespace},
		Spec: promoterv1alpha1.ArgoCDCommitStatusSpec{
			PromotionStrategyRef: promoterv1alpha1.ObjectReference{Name: inst.PromotionStrategyName()},
			Key:                  ArgoCDHealthKey,
			ApplicationSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{instanceLabelKey: inst.Name},
			},
		},
	}
}

// instanceLabelKey labels every Argo CD Application this tool creates so ArgoCDCommitStatus's
// applicationSelector can find them.
const instanceLabelKey = "gitops-promoter.io/loadtest-instance"

func promoterTypeMeta(kind string) metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: "promoter.argoproj.io/v1alpha1", Kind: kind}
}
