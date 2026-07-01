package demo

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v88/github"
	"golang.org/x/oauth2"
)

// NewGitHubClient creates an authenticated GitHub client from a personal access token and
// returns the client along with the authenticated user's login (the default repo owner).
func NewGitHubClient(ctx context.Context, token string) (*github.Client, string, error) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client, err := github.NewClient(github.WithHTTPClient(tc))
	if err != nil {
		return nil, "", fmt.Errorf("failed to create GitHub client: %w", err)
	}

	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return nil, "", fmt.Errorf("failed to get current user: %w", err)
	}

	return client, user.GetLogin(), nil
}

// CreateOrGetRepository creates a private GitHub repository named repoName under owner, or
// returns the existing repository if one with that name already exists.
func CreateOrGetRepository(
	ctx context.Context, client *github.Client, owner, repoName string,
) (*github.Repository, error) {
	repo, _, err := client.Repositories.Create(ctx, "", &github.Repository{
		Name:        new(repoName),
		Description: new("GitOps Promoter repository"),
		Private:     new(true),
		AutoInit:    new(true), // Creates with README
	})
	if err != nil {
		// Check if repo already exists
		if !strings.Contains(err.Error(), "already exists") {
			return nil, fmt.Errorf("failed to create repository: %w", err)
		}
		setupLog.Info("Repository already exists, fetching...")
		repo, _, err = client.Repositories.Get(ctx, owner, repoName)
		if err != nil {
			return nil, fmt.Errorf("failed to get existing repository: %w", err)
		}
	}

	return repo, nil
}
