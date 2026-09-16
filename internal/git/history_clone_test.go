package git_test

import (
	"fmt"
	"os"
	"strings"

	"github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CloneRepoForHistory", func() {
	var bareDir string
	var workDir string
	var branch string

	BeforeEach(func() {
		var err error
		bareDir, err = os.MkdirTemp("", "history-clone-bare-*")
		Expect(err).NotTo(HaveOccurred())
		workDir, err = os.MkdirTemp("", "history-clone-work-*")
		Expect(err).NotTo(HaveOccurred())

		_, err = runGitCmd(bareDir, "init", "--bare")
		Expect(err).NotTo(HaveOccurred())

		_, err = runGitCmd(workDir, "clone", bareDir, ".")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "config", "user.name", "test")
		Expect(err).NotTo(HaveOccurred())
		_, err = runGitCmd(workDir, "config", "user.email", "t@t.com")
		Expect(err).NotTo(HaveOccurred())

		for i := 0; i < 20; i++ {
			_, err = runGitCmd(workDir, "commit", "--allow-empty", "-m", fmt.Sprintf("commit %d", i))
			Expect(err).NotTo(HaveOccurred())
		}
		branch, err = runGitCmd(workDir, "rev-parse", "--abbrev-ref", "HEAD")
		Expect(err).NotTo(HaveOccurred())
		branch = strings.TrimSpace(branch)
		_, err = runGitCmd(workDir, "push", "-u", "origin", branch)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(bareDir)
		_ = os.RemoveAll(workDir)
	})

	It("clones a single branch with bounded depth", func() {
		repo := &v1alpha1.GitRepository{
			Name: "r", Namespace: "default",
			Spec: v1alpha1.GitRepositorySpec{
				GitHub: &v1alpha1.GitHubRepo{Owner: "o", Name: "r"},
			},
		}
		g := git.NewEnvironmentOperations(repo, &fakeGitProvider{tempDirPath: bareDir}, "history-clone-test")
		ctx := GinkgoT().Context()
		Expect(g.CloneRepoForHistory(ctx, branch, 3)).To(Succeed())

		shas, err := g.GetRevListFirstParent(ctx, "origin/"+branch, 3)
		Expect(err).NotTo(HaveOccurred())
		Expect(shas).To(HaveLen(3))

		clonePath := g.ClonePath()
		out, err := runGitCmd(clonePath, "config", "--get", "remote.origin.promisor")
		Expect(err).To(HaveOccurred(), "history clone should not configure a promisor partial clone")
		Expect(strings.TrimSpace(out)).To(BeEmpty())
	})
})
