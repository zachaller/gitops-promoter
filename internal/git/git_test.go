package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/git"
	"github.com/argoproj-labs/gitops-promoter/internal/utils/gitpaths"
)

func TestGit(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)
	RunSpecs(t, "Git Suite")
}

// Helper function to run git commands
func runGitCmd(dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	return string(output), err
}

var _ = Describe("GetBranchShas", func() {
	var tempRepoDir string

	BeforeEach(func() {
		// Create a temporary directory for the test repository
		var err error
		tempRepoDir, err = os.MkdirTemp("", "git-test-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if tempRepoDir != "" {
			Expect(os.RemoveAll(tempRepoDir)).To(Succeed())
		}
	})

	Context("When the branch does not exist on the remote", func() {
		It("should provide a clear error message from GetBranchShas", func() {
			By("Setting up a bare git repository")
			_, err := runGitCmd(tempRepoDir, "init", "--bare")
			Expect(err).NotTo(HaveOccurred())

			By("Creating an initial commit")
			workDir, err := os.MkdirTemp("", "git-work-*")
			Expect(err).NotTo(HaveOccurred())
			defer func() {
				Expect(os.RemoveAll(workDir)).To(Succeed())
			}()

			_, err = runGitCmd(workDir, "clone", tempRepoDir, ".")
			Expect(err).NotTo(HaveOccurred())

			_, err = runGitCmd(workDir, "config", "user.name", "Test User")
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "config", "user.email", "test@example.com")
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "config", "commit.gpgsign", "false")
			Expect(err).NotTo(HaveOccurred())

			err = os.WriteFile(filepath.Join(workDir, "hydrator.metadata"), []byte(`{"drySha": "abc123"}`), 0o644)
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "add", "hydrator.metadata")
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "commit", "-m", "Initial commit")
			Expect(err).NotTo(HaveOccurred())

			defaultBranch, err := runGitCmd(workDir, "rev-parse", "--abbrev-ref", "HEAD")
			Expect(err).NotTo(HaveOccurred())
			defaultBranch = strings.TrimSpace(defaultBranch)

			_, err = runGitCmd(workDir, "push", "origin", defaultBranch)
			Expect(err).NotTo(HaveOccurred())

			// Prepare EnvironmentOperations
			repo := &v1alpha1.GitRepository{
				Spec: v1alpha1.GitRepositorySpec{
					GitHub: &v1alpha1.GitHubRepo{
						Owner: "test-owner",
						Name:  "testrepo",
					},
					ScmProviderRef: v1alpha1.ScmProviderObjectReference{
						Kind: "ScmProvider",
						Name: "testprovider",
					},
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testrepo",
					Namespace: "default",
				},
			}
			gap := &fakeGitProvider{tempDirPath: tempRepoDir}
			g := git.NewEnvironmentOperations(repo, gap, defaultBranch)
			Expect(g.CloneRepo(GinkgoT().Context())).To(Succeed())

			// Call GetBranchShas with a non-existent branch
			_, err = g.GetBranchShas(GinkgoT().Context(), "environments/qal-usw2-eks-next")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to fetch branch"))

			// Having a missing branch is a common error, so we're ensuring the error message is clear.
			Expect(err.Error()).To(ContainSubstring("couldn't find remote ref"))
		})
	})
})

var _ = Describe("LsRemote", func() {
	var tempRepoDir string
	var workDir string

	BeforeEach(func() {
		// Create a temporary directory for the test repository
		var err error
		tempRepoDir, err = os.MkdirTemp("", "git-test-*")
		Expect(err).NotTo(HaveOccurred())

		By("Setting up a bare git repository")
		_, err = runGitCmd(tempRepoDir, "init", "--bare")
		Expect(err).NotTo(HaveOccurred())

		By("Creating a working directory with initial commit")
		workDir, err = os.MkdirTemp("", "git-work-*")
		Expect(err).NotTo(HaveOccurred())

		_, err = runGitCmd(workDir, "clone", tempRepoDir, ".")
		Expect(err).NotTo(HaveOccurred())

		_, err = runGitCmd(workDir, "config", "user.name", "Test User")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "config", "user.email", "test@example.com")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "config", "commit.gpgsign", "false")
		Expect(err).NotTo(HaveOccurred())

		err = os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test Repo"), 0o644)
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "add", "README.md")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "commit", "-m", "Initial commit")
		Expect(err).NotTo(HaveOccurred())

		defaultBranch, err := runGitCmd(workDir, "rev-parse", "--abbrev-ref", "HEAD")
		Expect(err).NotTo(HaveOccurred())
		defaultBranch = strings.TrimSpace(defaultBranch)

		_, err = runGitCmd(workDir, "push", "origin", defaultBranch)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if tempRepoDir != "" {
			Expect(os.RemoveAll(tempRepoDir)).To(Succeed())
		}
		if workDir != "" {
			Expect(os.RemoveAll(workDir)).To(Succeed())
		}
	})

	Context("When some branches are missing", func() {
		It("should provide a clear error message indicating which branches don't exist", func() {
			By("Creating only development and staging branches")
			_, err := runGitCmd(workDir, "checkout", "-b", "environment/development")
			Expect(err).NotTo(HaveOccurred())
			err = os.WriteFile(filepath.Join(workDir, "dev.txt"), []byte("dev"), 0o644)
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "add", "dev.txt")
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "commit", "-m", "Dev commit")
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "push", "origin", "environment/development")
			Expect(err).NotTo(HaveOccurred())

			_, err = runGitCmd(workDir, "checkout", "-b", "environment/staging")
			Expect(err).NotTo(HaveOccurred())
			err = os.WriteFile(filepath.Join(workDir, "staging.txt"), []byte("staging"), 0o644)
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "add", "staging.txt")
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "commit", "-m", "Staging commit")
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "push", "origin", "environment/staging")
			Expect(err).NotTo(HaveOccurred())

			By("Calling LsRemote with development, staging, and prod branches (prod doesn't exist)")
			repo := &v1alpha1.GitRepository{
				Spec: v1alpha1.GitRepositorySpec{
					GitHub: &v1alpha1.GitHubRepo{
						Owner: "test-owner",
						Name:  "testrepo",
					},
					ScmProviderRef: v1alpha1.ScmProviderObjectReference{
						Kind: "ScmProvider",
						Name: "testprovider",
					},
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testrepo",
					Namespace: "default",
				},
			}
			gap := &fakeGitProvider{tempDirPath: tempRepoDir}

			_, err = git.LsRemote(
				context.Background(),
				gap,
				repo,
				"environment/development",
				"environment/prod",
				"environment/staging",
			)
			Expect(err).To(HaveOccurred())

			By("Verifying the error message is helpful")
			Expect(err.Error()).To(ContainSubstring("missing branches: [environment/prod]"))
			Expect(err.Error()).To(ContainSubstring("(these branches may not exist yet"))
			Expect(err.Error()).To(ContainSubstring("check your PromotionStrategy"))
		})
	})

	Context("When multiple branches are missing", func() {
		It("should list all missing branches in the error message", func() {
			By("Creating only the development branch")
			_, err := runGitCmd(workDir, "checkout", "-b", "environment/development")
			Expect(err).NotTo(HaveOccurred())
			err = os.WriteFile(filepath.Join(workDir, "dev.txt"), []byte("dev"), 0o644)
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "add", "dev.txt")
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "commit", "-m", "Dev commit")
			Expect(err).NotTo(HaveOccurred())
			_, err = runGitCmd(workDir, "push", "origin", "environment/development")
			Expect(err).NotTo(HaveOccurred())

			By("Calling LsRemote with development, staging, and prod branches")
			repo := &v1alpha1.GitRepository{
				Spec: v1alpha1.GitRepositorySpec{
					GitHub: &v1alpha1.GitHubRepo{
						Owner: "test-owner",
						Name:  "testrepo",
					},
					ScmProviderRef: v1alpha1.ScmProviderObjectReference{
						Kind: "ScmProvider",
						Name: "testprovider",
					},
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testrepo",
					Namespace: "default",
				},
			}
			gap := &fakeGitProvider{tempDirPath: tempRepoDir}

			_, err = git.LsRemote(
				context.Background(),
				gap,
				repo,
				"environment/development",
				"environment/prod",
				"environment/staging",
			)
			Expect(err).To(HaveOccurred())

			By("Verifying all missing branches are listed")
			Expect(err.Error()).To(ContainSubstring("missing branches:"))
			Expect(err.Error()).To(ContainSubstring("environment/prod"))
			Expect(err.Error()).To(ContainSubstring("environment/staging"))
		})
	})
})

var _ = Describe("PromoterHistoryNotes", func() {
	var (
		remoteDir string
		workDir   string
		commitSha string
		ops       *git.EnvironmentOperations
	)

	BeforeEach(func() {
		var err error

		remoteDir, err = os.MkdirTemp("", "remote-*")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(remoteDir, "init", "--bare")
		Expect(err).NotTo(HaveOccurred())

		workDir, err = os.MkdirTemp("", "work-*")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "clone", remoteDir, workDir)
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "config", "user.email", "test@test.com")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "config", "user.name", "Test")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "config", "commit.gpgsign", "false")
		Expect(err).NotTo(HaveOccurred())

		_, err = runGitCmd(workDir, "commit", "--allow-empty", "-m", "initial")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "push", "origin", "HEAD:main")
		Expect(err).NotTo(HaveOccurred())
		out, err := runGitCmd(workDir, "rev-parse", "HEAD")
		Expect(err).NotTo(HaveOccurred())
		commitSha = strings.TrimSpace(out)

		// Register working clone path using the same key format as CloneRepo:
		// GetGitHttpsRepoUrl(*gitRepo) + activeBranch
		gitpaths.Set(remoteDir+"main", workDir)

		fakeProvider := &fakeGitProvider{tempDirPath: remoteDir}
		gitRepo := &v1alpha1.GitRepository{}
		ops = git.NewEnvironmentOperations(gitRepo, fakeProvider, "main")
	})

	AfterEach(func() {
		gitpaths.Delete(remoteDir + "main")
		os.RemoveAll(remoteDir)
		os.RemoveAll(workDir)
	})

	It("returns empty map when no note exists", func() {
		trailers, err := ops.GetPromoterHistoryNote(context.Background(), commitSha)
		Expect(err).NotTo(HaveOccurred())
		Expect(trailers).To(BeEmpty())
	})

	It("round-trips a note written in trailer format", func() {
		content := "Pull-request-id: 42\nPull-request-url: https://github.com/org/repo/pull/42\n"
		err := ops.WritePromoterHistoryNote(context.Background(), commitSha, content)
		Expect(err).NotTo(HaveOccurred())

		trailers, err := ops.GetPromoterHistoryNote(context.Background(), commitSha)
		Expect(err).NotTo(HaveOccurred())
		Expect(trailers).To(HaveKeyWithValue("Pull-request-id", ContainElement("42")))
		Expect(trailers).To(HaveKeyWithValue("Pull-request-url", ContainElement("https://github.com/org/repo/pull/42")))
	})

	It("FetchPromoterHistoryNotes succeeds when the ref does not exist yet", func() {
		err := ops.FetchPromoterHistoryNotes(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})
})

type fakeGitProvider struct {
	tempDirPath string
}

func (f *fakeGitProvider) GetGitHttpsRepoUrl(repo v1alpha1.GitRepository) string {
	// Return the local bare repo path for testing
	return f.tempDirPath
}
func (f *fakeGitProvider) GetUser(ctx context.Context) (string, error)  { return "user", nil }
func (f *fakeGitProvider) GetToken(ctx context.Context) (string, error) { return "token", nil }
