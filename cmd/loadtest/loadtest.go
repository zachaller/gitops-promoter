package loadtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/google/go-github/v88/github"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/argoproj-labs/gitops-promoter/cmd/demo"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

// NewLoadTestCommand creates the `promoter loadtest` command tree: setup, teardown, and bump.
func NewLoadTestCommand(clientConfig clientcmd.ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "loadtest",
		Short: "Repeatably provision GitOps Promoter load-test environments",
		Long: "Creates (or tears down) one or more independent, fully-wired GitOps Promoter " +
			"test environments - a real GitHub repo, GitHub App secrets, a PromotionStrategy, " +
			"and a full set of commit-status gates - for generating reconciler load.",
	}
	cmd.AddCommand(newSetupCommand(clientConfig))
	cmd.AddCommand(newTeardownCommand(clientConfig))
	cmd.AddCommand(newBumpCommand(clientConfig))
	cmd.AddCommand(newChangeMgmtMockCommand())
	return cmd
}

// addCommonFlags registers the identity flags shared by setup/teardown/bump.
func addCommonFlags(cmd *cobra.Command, cfg *Config) {
	cmd.Flags().StringVar(&cfg.Name, "name", "loadtest", "Resource/repo name prefix")
	cmd.Flags().IntVar(&cfg.Count, "count", 1, "Number of independent instances")
	cmd.Flags().StringVar(&cfg.Namespace, "namespace", "default", "Kubernetes namespace for all resources")
	modeStr := string(ModeDirect)
	cmd.Flags().StringVar(&modeStr, "mode", modeStr,
		"Deploy mode: \"direct\" (kubectl-apply gates, tool acts as fake hydrator) or "+
			"\"argocd\" (push gates to the repo, Argo CD syncs + hydrates)")
	cmd.Flags().StringVar(&cfg.Owner, "owner", "",
		"GitHub org/user to create repos under (default: authenticated user)")
	cmd.Flags().StringVar(&cfg.GitHubToken, "github-token", "",
		"GitHub personal access token (falls back to GITHUB_TOKEN)")
	// cfg.Mode is set from modeStr in PreRunE since pflag has no Mode-typed Var.
	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		cfg.Mode = Mode(modeStr)
		return nil
	}
}

func newSetupCommand(clientConfig clientcmd.ClientConfig) *cobra.Command {
	cfg := &Config{}
	var timedDurationsFlag string

	cmd := &cobra.Command{
		Use:          "setup",
		Short:        "Create (or converge) one or more loadtest instances",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			durations, err := parseTimedDurations(timedDurationsFlag)
			if err != nil {
				return err
			}
			cfg.TimedDurations = durations

			ctx := ctrl.SetupSignalHandler()
			if err := resolveSetupCredentials(cfg); err != nil {
				return err
			}

			ghClient, username, err := demo.NewGitHubClient(ctx, cfg.GitHubToken)
			if err != nil {
				return fmt.Errorf("failed to create GitHub client: %w", err)
			}
			owner := cfg.Owner
			if owner == "" {
				owner = username
			}

			c, clientset, err := newClients(clientConfig)
			if err != nil {
				return err
			}

			if err := demo.CreateNamespace(ctx, clientset, cfg.Namespace); err != nil {
				return fmt.Errorf("failed to create namespace %s: %w", cfg.Namespace, err)
			}

			for _, inst := range Instances(cfg) {
				if err := setupInstance(ctx, cfg, c, clientset, ghClient, owner, inst); err != nil {
					return fmt.Errorf("instance %s: %w", inst.Name, err)
				}
			}
			color.Green("\nloadtest setup complete (%d instance(s), mode=%s)\n", cfg.Count, cfg.Mode)
			return nil
		},
	}

	addCommonFlags(cmd, cfg)
	cmd.Flags().Int64Var(&cfg.GitHubAppID, "github-app-id", 0, "GitHub App ID (falls back to GITHUB_APP_ID)")
	cmd.Flags().Int64Var(&cfg.GitHubInstallationID, "github-app-installation-id", 0,
		"GitHub App installation ID (optional; inferred from the repo owner if unset)")
	cmd.Flags().StringVar(&cfg.GitHubAppPrivateKeyPath, "github-app-private-key-path", "",
		"Path to the GitHub App private key .pem (falls back to GITHUB_APP_PRIVATE_KEY_PATH)")
	cmd.Flags().StringVar(&cfg.ChangeMgmtBaseURL, "change-mgmt-base-url",
		"http://localhost:8987/v1/change-management-service",
		"Base URL the change-management WebRequestCommitStatus trio calls "+
			"(run that mock service yourself; out of scope for this tool)")
	cmd.Flags().StringVar(&timedDurationsFlag, "timed-durations", "30s,1m,2m",
		"Comma-separated TimedCommitStatus durations for development,staging,production")
	return cmd
}

func newTeardownCommand(clientConfig clientcmd.ClientConfig) *cobra.Command {
	cfg := &Config{}

	cmd := &cobra.Command{
		Use:          "teardown",
		Short:        "Delete one or more loadtest instances, including their GitHub repos",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			ctx := ctrl.SetupSignalHandler()
			if err := resolveGitHubToken(cfg); err != nil {
				return err
			}

			ghClient, username, err := demo.NewGitHubClient(ctx, cfg.GitHubToken)
			if err != nil {
				return fmt.Errorf("failed to create GitHub client: %w", err)
			}
			owner := cfg.Owner
			if owner == "" {
				owner = username
			}

			c, _, err := newClients(clientConfig)
			if err != nil {
				return err
			}

			for _, inst := range Instances(cfg) {
				if err := TeardownInstance(ctx, cfg, c, ghClient, owner, inst); err != nil {
					return fmt.Errorf("instance %s: %w", inst.Name, err)
				}
			}
			color.Green("\nloadtest teardown complete (%d instance(s))\n", cfg.Count)
			return nil
		},
	}

	addCommonFlags(cmd, cfg)
	return cmd
}

func newBumpCommand(clientConfig clientcmd.ClientConfig) *cobra.Command {
	_ = clientConfig // teardown/setup need a k8s client; bump only needs git + GitHub, kept for signature symmetry.
	cfg := &Config{}
	bumpCfg := &BumpConfig{Config: cfg}
	var intervalFlag time.Duration

	cmd := &cobra.Command{
		Use:   "bump",
		Short: "Commit a no-op change to the dry branch to drive a promotion cycle",
		Long: "Commits a fresh timestamp into promotion-app/touch on the dry branch and pushes " +
			"it. In direct mode this also replays the fake-hydrator step against every *-next " +
			"branch, since nothing else will. In argocd mode Argo CD's sourceHydrator does that " +
			"on its own sync cadence.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			bumpCfg.Interval = intervalFlag
			ctx := ctrl.SetupSignalHandler()
			if err := resolveGitHubToken(cfg); err != nil {
				return err
			}

			_, username, err := demo.NewGitHubClient(ctx, cfg.GitHubToken)
			if err != nil {
				return fmt.Errorf("failed to create GitHub client: %w", err)
			}
			owner := cfg.Owner
			if owner == "" {
				owner = username
			}

			return Bump(ctx, bumpCfg, owner)
		},
	}

	addCommonFlags(cmd, cfg)
	cmd.Flags().DurationVar(&intervalFlag, "interval", 0, "Pause between ticks; 0 = bump once and exit")
	cmd.Flags().IntVar(&bumpCfg.Iterations, "iterations", 0, "Number of ticks when --interval is set; 0 = unlimited")
	return cmd
}

// newClients builds a controller-runtime client (promoter + core scheme) and a client-go
// clientset from the root command's kubeconfig flags.
func newClients(clientConfig clientcmd.ClientConfig) (client.Client, kubernetes.Interface, error) {
	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get client config: %w", err)
	}
	c, err := client.New(restConfig, client.Options{Scheme: utils.GetScheme()})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create controller-runtime client: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create clientset: %w", err)
	}
	return c, clientset, nil
}

// errMissingGitHubToken is returned when no GitHub token was resolved from a flag, environment
// variable, or (for setup only) the interactive prompt.
var errMissingGitHubToken = errors.New(
	"missing GitHub token: set --github-token or the GITHUB_TOKEN environment variable",
)

// resolveGitHubToken resolves cfg.GitHubToken from the flag or GITHUB_TOKEN. Used by
// teardown/bump, which don't need the GitHub App ID/private key.
func resolveGitHubToken(cfg *Config) error {
	if cfg.GitHubToken == "" {
		cfg.GitHubToken = os.Getenv("GITHUB_TOKEN")
	}
	if cfg.GitHubToken == "" {
		return errMissingGitHubToken
	}
	return nil
}

// resolveSetupCredentials resolves the token, GitHub App ID, and private key needed by setup:
// flags first, then environment variables, then (if anything is still missing) an interactive
// prompt reusing cmd/demo's InteractivePrompter.
func resolveSetupCredentials(cfg *Config) error {
	if cfg.GitHubToken == "" {
		cfg.GitHubToken = os.Getenv("GITHUB_TOKEN")
	}
	if cfg.GitHubAppID == 0 {
		if err := resolveAppIDFromEnv(cfg); err != nil {
			return err
		}
	}
	if cfg.GitHubAppPrivateKeyPath == "" {
		cfg.GitHubAppPrivateKeyPath = os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")
	}

	if err := promptForMissingCredentials(cfg); err != nil {
		return err
	}
	if err := loadPrivateKeyIfNeeded(cfg); err != nil {
		return err
	}

	if cfg.GitHubToken == "" {
		return errMissingGitHubToken
	}
	return nil
}

// resolveAppIDFromEnv sets cfg.GitHubAppID from GITHUB_APP_ID when set.
func resolveAppIDFromEnv(cfg *Config) error {
	v := os.Getenv("GITHUB_APP_ID")
	if v == "" {
		return nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid GITHUB_APP_ID %q: %w", v, err)
	}
	cfg.GitHubAppID = id
	return nil
}

// promptForMissingCredentials runs the interactive prompter (reusing cmd/demo's) if any of
// token/App ID/private-key-path are still unresolved, filling in only the missing ones.
func promptForMissingCredentials(cfg *Config) error {
	if cfg.GitHubToken != "" && cfg.GitHubAppID != 0 && cfg.GitHubAppPrivateKeyPath != "" {
		return nil
	}
	prompter := demo.NewInteractivePrompter()
	prompter.PrintCLIInformation()
	creds, err := prompter.GetCredentials()
	if err != nil {
		return fmt.Errorf("failed to prompt for credentials: %w", err)
	}
	if cfg.GitHubToken == "" {
		cfg.GitHubToken = creds.Token
	}
	if cfg.GitHubAppID == 0 {
		id, err := strconv.ParseInt(creds.AppID, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid GitHub App ID %q: %w", creds.AppID, err)
		}
		cfg.GitHubAppID = id
	}
	if cfg.GitHubAppPrivateKeyPath == "" {
		cfg.GitHubAppPrivateKeyPath = creds.PrivateKeyPath
		cfg.GitHubAppPrivateKey = creds.PrivateKey
	}
	return nil
}

// loadPrivateKeyIfNeeded reads GitHubAppPrivateKeyPath into GitHubAppPrivateKey when the
// interactive prompt didn't already provide the key content directly.
func loadPrivateKeyIfNeeded(cfg *Config) error {
	if cfg.GitHubAppPrivateKey != "" {
		return nil
	}
	content, err := os.ReadFile(cfg.GitHubAppPrivateKeyPath)
	if err != nil {
		return fmt.Errorf("failed to read GitHub App private key from %s: %w", cfg.GitHubAppPrivateKeyPath, err)
	}
	cfg.GitHubAppPrivateKey = string(content)
	return nil
}

// parseTimedDurations parses a "dev,staging,prod" comma-separated duration string.
func parseTimedDurations(s string) ([3]time.Duration, error) {
	var out [3]time.Duration
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return out, fmt.Errorf("--timed-durations must have exactly 3 comma-separated values, got %d", len(parts))
	}
	for i, p := range parts {
		d, err := time.ParseDuration(strings.TrimSpace(p))
		if err != nil {
			return out, fmt.Errorf("invalid duration %q in --timed-durations: %w", p, err)
		}
		out[i] = d
	}
	return out, nil
}

// setupInstance provisions everything for a single instance: the GitHub repo, secret,
// bootstrap-only CRs (ScmProvider/GitRepository), the git bootstrap, and finally the gating
// CRDs - applied directly (ModeDirect) or pushed to promotion-app/ and synced via Argo CD
// (ModeArgoCD).
func setupInstance(
	ctx context.Context, cfg *Config, c client.Client, clientset kubernetes.Interface,
	ghClient *github.Client, owner string, inst Instance,
) error {
	color.Cyan("\nSetting up %s...\n", inst.Name)

	repo, err := demo.CreateOrGetRepository(ctx, ghClient, owner, inst.RepoName)
	if err != nil {
		return fmt.Errorf("failed to create/get repository: %w", err)
	}
	color.Green("  repository: %s\n", repo.GetHTMLURL())

	secretData := map[string]string{"githubAppPrivateKey": cfg.GitHubAppPrivateKey}
	err = demo.CreateOrUpdateSecret(ctx, clientset, inst.Namespace, inst.SecretName(), secretData, map[string]string{})
	if err != nil {
		return fmt.Errorf("failed to create secret: %w", err)
	}

	if err := applyObject(ctx, c, BuildScmProvider(cfg, inst)); err != nil {
		return err
	}
	if err := applyObject(ctx, c, BuildGitRepository(inst, owner)); err != nil {
		return err
	}

	color.Cyan("  bootstrapping git branches...\n")
	if _, err := BootstrapRepo(ctx, cfg, owner, inst); err != nil {
		return fmt.Errorf("failed to bootstrap git branches: %w", err)
	}

	switch cfg.Mode {
	case ModeDirect:
		if err := applyObject(ctx, c, BuildPromotionStrategy(cfg, inst)); err != nil {
			return err
		}
		if err := applyObject(ctx, c, BuildTimedCommitStatus(cfg, inst)); err != nil {
			return err
		}
		if err := applyObject(ctx, c, BuildGitCommitStatus(inst)); err != nil {
			return err
		}
		for _, wrcs := range BuildWebRequestCommitStatuses(cfg, inst) {
			if err := applyObject(ctx, c, wrcs); err != nil {
				return err
			}
		}
	case ModeArgoCD:
		color.Cyan("  pushing promotion-app/ to repo...\n")
		if err := PushPromotionApp(ctx, cfg, owner, inst, gatingResources(cfg, inst)); err != nil {
			return err
		}
		if err := applyObject(ctx, c, BuildArgoCDCommitStatus(inst)); err != nil {
			return err
		}
		repoURL := displayURL(owner, inst.RepoName)
		if err := ApplyArgoCDApplications(ctx, c, inst, repoURL); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown mode %q", cfg.Mode)
	}

	color.Green("Instance %s ready (mode=%s)\n", inst.Name, cfg.Mode)
	return nil
}
