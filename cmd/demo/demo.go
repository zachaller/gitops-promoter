package demo

import (
	"context"
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
)

var setupLog = ctrl.Log.WithName("setup")

// ArgoCDConfig holds ArgoCD configuration
type ArgoCDConfig struct {
	Upstream string `yaml:"upstream"`
}

// GitOpsPromoterConfig holds GitOps Promoter configuration
type GitOpsPromoterConfig struct {
	Upstream string `yaml:"upstream"`
}

// Config structure for config.yaml
type Config struct {
	ArgoCD         ArgoCDConfig         `yaml:"argocd"`
	GitOpsPromoter GitOpsPromoterConfig `yaml:"gitops-promoter"`
}

// NewDemoCommand creates a new demo command for setting up a gitops-promoter demo repository
func NewDemoCommand() *cobra.Command {
	var repoName string

	cmd := &cobra.Command{
		Use:          "demo",
		Short:        "Setup a new gitops-promoter demo repository",
		Long:         `This command will guide you through setting up a demo environment for GitOps Promoter.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			installer, err := NewInstaller()
			if err != nil {
				return err
			}

			// Prompt for credentials
			prompter := NewInteractivePrompter()
			prompter.PrintCLIInformation()
			if err := validateClusterSafety(); err != nil {
				return err
			}
			credentials, err := prompter.GetCredentials()
			if err != nil {
				return fmt.Errorf("failed to prompt for credentials: %w", err)
			}

			client, username, err := NewGitHubClient(ctx, credentials.Token)
			if err != nil {
				return fmt.Errorf("failed to create GitHub client: %w", err)
			}
			color.Green("Current github user: %s\n", username)

			if err := installer.SetupCluster(ctx); err != nil {
				return fmt.Errorf("failed to setup cluster: %w", err)
			}

			// 3. Create the repository
			color.Green("Creating repository %s/%s...\n", username, repoName)
			repo, err := CreateOrGetRepository(ctx, client, username, repoName)
			if err != nil {
				return err
			}

			color.Green("Repository available at: %s\n", repo.GetHTMLURL())

			// 4. Create files in the repo
			if err := UploadManifests(ctx, client, repo, Replacements{
				RepoName: repoName,
				Username: username,
				AppID:    credentials.AppID,
			}); err != nil {
				return fmt.Errorf("failed to upload manifests: %w", err)
			}

			// Create promotion strategy secret
			k8sClient, err := createK8sClient()
			if err != nil {
				return fmt.Errorf("failed to create k8s client: %w", err)
			}

			// create helm-guestbook-ps ns
			err = CreateNamespace(ctx, k8sClient, "helm-guestbook-ps")
			if err != nil {
				return fmt.Errorf("failed to create namespace: %w", err)
			}

			secretData := map[string]string{"githubAppPrivateKey": credentials.PrivateKey}
			err = CreateOrUpdateSecret(
				ctx, k8sClient, "helm-guestbook-ps", "github-demo-secret",
				secretData, map[string]string{},
			)
			if err != nil {
				return fmt.Errorf("failed to create promotion strategy github app secret: %w", err)
			}

			// Create repo secrets
			if err := CreateRepoSecrets(ctx, k8sClient, credentials, username, repoName); err != nil {
				return err
			}
			color.Green("Repo secrets created!")

			// Create base app
			if err := installer.ApplyBaseApp(ctx, username, repoName); err != nil {
				return err
			}
			color.Green("Base app applied!")

			// Copy helm-guestbook directory to the repo
			err = CopyEmbeddedDirToRepo(ctx, client, username, repoName, "helm-guestbook")
			if err != nil {
				return fmt.Errorf("failed to copy directory: %w", err)
			}

			color.Green("Application installed!")

			// Update requeue interval
			err = installer.PatchControllerConfiguration(ctx)
			if err != nil {
				return err
			}
			color.Green("Updating requeue interval")

			// Force refresh
			if err := installer.RefreshApp(ctx, "helm-guestbook-ps"); err != nil {
				return err
			}

			if err := installer.PortForward(ctx); err != nil {
				return err
			}

			color.Green("Installation complete!")
			return nil
		},
	}

	cmd.Flags().StringVar(&repoName, "repo-name", "gitops-promoter-examples", "Name of the repository to create")

	return cmd
}
