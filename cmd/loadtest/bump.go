package loadtest

import (
	"context"
	"fmt"
	"time"

	"github.com/fatih/color"
	"golang.org/x/sync/errgroup"
)

// BumpConfig holds the flags specific to `promoter loadtest bump`.
type BumpConfig struct {
	*Config
	// Interval is the pause between ticks. Zero means "bump once and exit".
	Interval time.Duration
	// Iterations bounds the number of ticks when Interval > 0. Zero means unlimited (run until
	// ctx is cancelled, e.g. via SIGINT/SIGTERM).
	Iterations int
}

// Bump runs the bump loop described in the plan's "Load generation" section: each tick commits
// a fresh timestamp into promotion-app/touch on the dry branch and pushes; in ModeDirect it then
// replays the fake-hydrator step against every *-next branch (nothing else will, since there's
// no real hydrator running); in ModeArgoCD it does neither, leaving that to Argo CD's
// sourceHydrator on its own sync cadence.
func Bump(ctx context.Context, cfg *BumpConfig, owner string) error {
	instances := Instances(cfg.Config)

	tick := func(iteration int) error {
		g, gctx := errgroup.WithContext(ctx)
		for _, inst := range instances {
			g.Go(func() error {
				sha, err := bumpInstance(gctx, cfg.Config, owner, inst)
				if err != nil {
					return fmt.Errorf("instance %s: %w", inst.Name, err)
				}
				color.Green("[tick %d] %s bumped -> %s at %s\n", iteration, inst.Name, sha[:min(7, len(sha))],
					time.Now().Format(time.RFC3339))
				return nil
			})
		}
		return g.Wait()
	}

	if cfg.Interval <= 0 {
		return tick(1)
	}

	for i := 1; cfg.Iterations == 0 || i <= cfg.Iterations; i++ {
		if err := tick(i); err != nil {
			return err
		}
		if cfg.Iterations != 0 && i >= cfg.Iterations {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.Interval):
		}
	}
	return nil
}

// bumpInstance performs one bump tick for a single instance, returning the new dry SHA.
func bumpInstance(ctx context.Context, cfg *Config, owner string, inst Instance) (string, error) {
	dir, cleanup, err := cloneRepo(ctx, cfg, owner, inst.RepoName)
	if err != nil {
		return "", err
	}
	defer cleanup()

	dry, err := defaultBranch(ctx, dir)
	if err != nil {
		return "", err
	}

	if err := writeFile(dir, TouchFileName, time.Now().UTC().Format(time.RFC3339Nano)+"\n"); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, dir, "add", TouchFileName); err != nil {
		return "", err
	}
	commitMsg := "chore: loadtest bump " + time.Now().UTC().Format(time.RFC3339)
	if _, err := runGit(ctx, dir, "commit", "-m", commitMsg); err != nil {
		return "", fmt.Errorf("failed to commit bump: %w", err)
	}
	if _, err := runGit(ctx, dir, "push", "origin", dry); err != nil {
		return "", fmt.Errorf("failed to push bump: %w", err)
	}
	drySha, err := runGit(ctx, dir, "rev-parse", dry)
	if err != nil {
		return "", err
	}

	if cfg.Mode != ModeDirect {
		return drySha, nil
	}

	// No real hydrator is running in direct mode: replay the fake-hydrator step against every
	// *-next branch so the new dry commit actually reaches PromotionStrategy/CTP.
	for _, env := range environments {
		nextBranch := EnvironmentNextBranch(env)
		headSha, err := createBootstrapBranch(ctx, dir, nextBranch, drySha, drySha,
			fmt.Sprintf("chore: bump %s (fake hydrator)", nextBranch))
		if err != nil {
			return "", err
		}
		if err := pushGitNote(ctx, dir, headSha, drySha); err != nil {
			return "", err
		}
	}

	return drySha, nil
}
