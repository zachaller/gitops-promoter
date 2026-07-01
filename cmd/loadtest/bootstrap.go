package loadtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"

	"github.com/argoproj-labs/gitops-promoter/internal/git"
)

const (
	gitCommitterName  = "gitops-promoter-loadtest"
	gitCommitterEmail = "loadtest@gitops-promoter.local"
)

// cloneURL builds the HTTPS clone URL for owner/repoName with the token embedded for
// authentication. Never log or wrap errors that include this value - it contains a credential.
func cloneURL(cfg *Config, owner, repoName string) string {
	return fmt.Sprintf("https://%s@github.com/%s/%s.git", cfg.GitHubToken, owner, repoName)
}

// displayURL is the credential-free form of a repo URL, safe for logs and error messages.
func displayURL(owner, repoName string) string {
	return fmt.Sprintf("https://github.com/%s/%s", owner, repoName)
}

// runGit runs git in dir with args, returning trimmed stdout. On error, the returned error
// includes stderr but never the raw args (which may contain a tokenized URL).
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// cloneRepo clones owner/repoName into a fresh temp directory and configures a bot identity.
// The caller is responsible for calling the returned cleanup function.
func cloneRepo(ctx context.Context, cfg *Config, owner, repoName string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "gitops-promoter-loadtest-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp clone dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	if _, err := runGit(ctx, dir, "clone", cloneURL(cfg, owner, repoName), "."); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to clone %s: %w", displayURL(owner, repoName), err)
	}
	if _, err := runGit(ctx, dir, "config", "user.name", gitCommitterName); err != nil {
		cleanup()
		return "", nil, err
	}
	if _, err := runGit(ctx, dir, "config", "user.email", gitCommitterEmail); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// defaultBranch returns the repository's default branch name (e.g. "main").
func defaultBranch(ctx context.Context, dir string) (string, error) {
	branch, err := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to determine default branch: %w", err)
	}
	return branch, nil
}

// hydratorMetadataJSON renders the minimal hydrator.metadata file contents recognized by
// internal/git.HydratorMetadata (json tag "drySha").
func hydratorMetadataJSON(drySha string) string {
	return fmt.Sprintf(`{"drySha": %q}`, drySha)
}

// writeFile writes content to <dir>/<relPath>, creating parent directories as needed.
func writeFile(dir, relPath, content string) error {
	fullPath := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", relPath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", relPath, err)
	}
	return nil
}

// pushGitNote adds and pushes a hydrator-metadata git note (refs/notes/hydrator.metadata) for
// commitSha, recording drySha. Mirrors internal/controller/suite_test.go's pushGitNote, against
// a real GitHub remote instead of the gitkit test server.
func pushGitNote(ctx context.Context, dir, commitSha, drySha string) error {
	noteContent := fmt.Sprintf(`{"drySha": %q}`, drySha)
	noteRefArg := "--ref=" + git.HydratorNotesRef
	if _, err := runGit(ctx, dir, "notes", noteRefArg, "add", "-f", "-m", noteContent, commitSha); err != nil {
		return fmt.Errorf("failed to add git note: %w", err)
	}
	if _, err := runGit(ctx, dir, "push", "--force", "origin", git.HydratorNotesRef); err != nil {
		return fmt.Errorf("failed to push git note: %w", err)
	}
	return nil
}

// createBootstrapBranch force-creates branchName pointing at baseSha, writes a root
// hydrator.metadata recording drySha, commits, and force-pushes. Force-push/force-create make
// this safe to re-run against an existing instance (setup is idempotent, not additive).
func createBootstrapBranch(
	ctx context.Context, dir, branchName, baseSha, drySha, commitMessage string,
) (headSha string, err error) {
	if _, err := runGit(ctx, dir, "checkout", "-B", branchName, baseSha); err != nil {
		return "", fmt.Errorf("failed to create branch %q: %w", branchName, err)
	}
	if err := writeFile(dir, "hydrator.metadata", hydratorMetadataJSON(drySha)); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, dir, "add", "hydrator.metadata"); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, dir, "commit", "--allow-empty", "-m", commitMessage); err != nil {
		return "", fmt.Errorf("failed to commit on branch %q: %w", branchName, err)
	}
	headSha, err = runGit(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := runGit(ctx, dir, "push", "--force", "-u", "origin", branchName); err != nil {
		return "", fmt.Errorf("failed to push branch %q: %w", branchName, err)
	}
	return headSha, nil
}

// BootstrapRepo prepares a freshly created repository for GitOps Promoter:
//   - commits a root hydrator.metadata + the promotion-app/touch marker file on the dry branch
//   - creates each environment's active branch (environments/<env>), each with hydrator.metadata
//     pointing at the dry commit
//   - in ModeDirect, also creates the proposed (*-next) branches directly, acting as a one-shot
//     fake hydrator, since nothing else will (Argo CD's sourceHydrator plays that role in
//     ModeArgoCD instead, once its Application syncs).
//
// Returns the dry branch's HEAD SHA (the "drySha" bootstrapped into every environment).
func BootstrapRepo(ctx context.Context, cfg *Config, owner string, inst Instance) (drySha string, err error) {
	dir, cleanup, err := cloneRepo(ctx, cfg, owner, inst.RepoName)
	if err != nil {
		return "", err
	}
	defer cleanup()

	dry, err := defaultBranch(ctx, dir)
	if err != nil {
		return "", err
	}

	if err := writeFile(dir, TouchFileName, ""); err != nil {
		return "", err
	}
	if err := writeFile(dir, "hydrator.metadata", hydratorMetadataJSON("")); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, dir, "add", PromotionAppDir, "hydrator.metadata"); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, dir, "commit", "--allow-empty", "-m", "chore: bootstrap loadtest dry branch"); err != nil {
		return "", fmt.Errorf("failed to commit dry branch bootstrap: %w", err)
	}
	if _, err := runGit(ctx, dir, "push", "origin", dry); err != nil {
		return "", fmt.Errorf("failed to push dry branch: %w", err)
	}
	drySha, err = runGit(ctx, dir, "rev-parse", dry)
	if err != nil {
		return "", err
	}

	for _, env := range environments {
		activeBranch := EnvironmentBranch(env)
		if _, err := createBootstrapBranch(ctx, dir, activeBranch, drySha, drySha,
			"chore: bootstrap "+activeBranch); err != nil {
			return "", err
		}
		color.Green("  bootstrapped %s @ %s\n", activeBranch, drySha[:min(7, len(drySha))])

		if cfg.Mode != ModeDirect {
			continue
		}

		nextBranch := EnvironmentNextBranch(env)
		headSha, err := createBootstrapBranch(ctx, dir, nextBranch, drySha, drySha,
			fmt.Sprintf("chore: bootstrap %s (fake hydrator)", nextBranch))
		if err != nil {
			return "", err
		}
		if err := pushGitNote(ctx, dir, headSha, drySha); err != nil {
			return "", err
		}
		color.Green("  bootstrapped %s @ %s (fake hydrator)\n", nextBranch, headSha[:min(7, len(headSha))])
	}

	// Leave the clone on the dry branch so any caller inspecting it afterwards sees a sane state.
	if _, err := runGit(ctx, dir, "checkout", dry); err != nil {
		return "", err
	}

	return drySha, nil
}
