package loadtest

import (
	"context"
	"fmt"
	"net/http"

	"github.com/fatih/color"
	"github.com/google/go-github/v88/github"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// deleteIfExists deletes obj, treating "already gone" as success so teardown is safe to re-run.
func deleteIfExists(ctx context.Context, c client.Client, obj client.Object) error {
	if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete %s %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}

// TeardownInstance deletes every Kubernetes resource this tool created for inst, then (per
// Config) the GitHub repository itself.
func TeardownInstance(
	ctx context.Context, cfg *Config, c client.Client, ghClient *github.Client, owner string, inst Instance,
) error {
	color.Cyan("Tearing down %s...\n", inst.Name)

	for _, wrcs := range BuildWebRequestCommitStatuses(cfg, inst) {
		if err := deleteIfExists(ctx, c, wrcs); err != nil {
			return err
		}
	}
	if err := deleteIfExists(ctx, c, BuildGitCommitStatus(inst)); err != nil {
		return err
	}
	if err := deleteIfExists(ctx, c, BuildTimedCommitStatus(cfg, inst)); err != nil {
		return err
	}
	if cfg.Mode == ModeArgoCD {
		if err := deleteIfExists(ctx, c, BuildArgoCDCommitStatus(inst)); err != nil {
			return err
		}
		if err := deleteIfExists(ctx, c, BuildPromotionAppApplication(inst, "")); err != nil {
			return err
		}
		for _, app := range BuildWorkloadApplications(inst, "") {
			if err := deleteIfExists(ctx, c, app); err != nil {
				return err
			}
		}
	}
	if err := deleteIfExists(ctx, c, BuildPromotionStrategy(cfg, inst)); err != nil {
		return err
	}
	if err := deleteIfExists(ctx, c, BuildGitRepository(inst, owner)); err != nil {
		return err
	}
	if err := deleteIfExists(ctx, c, BuildScmProvider(cfg, inst)); err != nil {
		return err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: inst.SecretName(), Namespace: inst.Namespace}}
	if err := deleteIfExists(ctx, c, secret); err != nil {
		return err
	}

	color.Cyan("Deleting GitHub repository %s/%s...\n", owner, inst.RepoName)
	if _, err := ghClient.Repositories.Delete(ctx, owner, inst.RepoName); err != nil {
		var ghErr *github.ErrorResponse
		if ok := isGitHubNotFound(err, &ghErr); !ok {
			return fmt.Errorf("failed to delete repository %s: %w", displayURL(owner, inst.RepoName), err)
		}
	}

	color.Green("Tore down %s\n", inst.Name)
	return nil
}

// isGitHubNotFound reports whether err is a go-github 404 (repo already gone).
func isGitHubNotFound(err error, target **github.ErrorResponse) bool {
	ghErr, ok := err.(*github.ErrorResponse) //nolint:errorlint // go-github returns concrete *ErrorResponse, not wrapped
	if !ok {
		return false
	}
	*target = ghErr
	return ghErr.Response != nil && ghErr.Response.StatusCode == http.StatusNotFound
}
