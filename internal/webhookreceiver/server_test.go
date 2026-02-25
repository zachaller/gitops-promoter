package webhookreceiver_test

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/webhookreceiver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("DetectProvider", func() {
	var wr *webhookreceiver.WebhookReceiver

	BeforeEach(func() {
		wr = &webhookreceiver.WebhookReceiver{}
	})

	tests := map[string]struct {
		headers        map[string]string
		expectedResult string
	}{
		"GitHub webhook with X-GitHub-Event": {
			headers: map[string]string{
				"X-GitHub-Event": "push",
			},
			expectedResult: webhookreceiver.ProviderGitHub,
		},
		"GitHub webhook with X-GitHub-Delivery": {
			headers: map[string]string{
				"X-GitHub-Delivery": "12345",
			},
			expectedResult: webhookreceiver.ProviderGitHub,
		},
		"GitLab webhook with X-Gitlab-Event": {
			headers: map[string]string{
				"X-Gitlab-Event": "Push Hook",
			},
			expectedResult: webhookreceiver.ProviderGitLab,
		},
		"GitLab webhook with X-Gitlab-Token": {
			headers: map[string]string{
				"X-Gitlab-Token": "secret",
			},
			expectedResult: webhookreceiver.ProviderGitLab,
		},
		"Forgejo webhook with X-Forgejo-Event": {
			headers: map[string]string{
				"X-Forgejo-Event": "push",
			},
			expectedResult: webhookreceiver.ProviderForgejo,
		},
		"Gitea webhook with X-Gitea-Event": {
			headers: map[string]string{
				"X-Gitea-Event": "push",
			},
			expectedResult: webhookreceiver.ProviderGitea,
		},
		"Bitbucket Cloud webhook with X-Hook-UUID": {
			headers: map[string]string{
				"X-Hook-UUID": "12345-abcde",
			},
			expectedResult: webhookreceiver.ProviderBitbucketCloud,
		},
		"Unknown provider - no headers": {
			headers:        map[string]string{},
			expectedResult: webhookreceiver.ProviderUnknown,
		},
		"Unknown provider - wrong headers": {
			headers: map[string]string{
				"X-Custom-Header": "value",
			},
			expectedResult: webhookreceiver.ProviderUnknown,
		},
	}

	for name, test := range tests {
		It(name, func() {
			req, err := http.NewRequest(http.MethodPost, "/", nil)
			Expect(err).NotTo(HaveOccurred())

			for key, value := range test.headers {
				req.Header.Set(key, value)
			}

			result := wr.DetectProvider(req)
			Expect(result).To(Equal(test.expectedResult))
		})
	}

	It("should detect GitHub first when multiple provider headers are present", func() {
		req, err := http.NewRequest(http.MethodPost, "/", nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("X-Github-Event", "push")
		req.Header.Set("X-Gitlab-Event", "Push Hook")

		result := wr.DetectProvider(req)
		Expect(result).To(Equal(webhookreceiver.ProviderGitHub))
	})
})

// buildPRMergePayload builds a GitHub pull_request webhook payload for a merged PR.
func buildPRMergePayload(prNumber int, mergeCommitSHA string, merged bool) string {
	mergedStr := "false"
	if merged {
		mergedStr = "true"
	}
	return fmt.Sprintf(`{
		"action": "closed",
		"pull_request": {
			"number": %d,
			"merged": %s,
			"merge_commit_sha": "%s"
		}
	}`, prNumber, mergedStr, mergeCommitSHA)
}

// sendWebhookRequest sends a POST to the webhook receiver and returns the response.
func sendWebhookRequest(payload string, headers map[string]string) *http.Response {
	webhookURL := fmt.Sprintf("http://localhost:%d/", webhookRecvPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewBufferString(payload))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	return resp
}

// createTestPullRequest creates a PullRequest in the test namespace and sets its Status.ID.
func createTestPullRequest(name, namespace, statusID string) *promoterv1alpha1.PullRequest {
	pr := &promoterv1alpha1.PullRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: promoterv1alpha1.PullRequestSpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{
				Name: "test-repo",
			},
			Title:        "Test PR",
			TargetBranch: "main",
			SourceBranch: "feature",
			MergeSha:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			State:        promoterv1alpha1.PullRequestOpen,
		},
	}
	Expect(k8sClient.Create(ctx, pr)).To(Succeed())

	// Set status.id via status subresource update.
	pr.Status.ID = statusID
	Expect(k8sClient.Status().Update(ctx, pr)).To(Succeed())

	return pr
}

var _ = Describe("When a GitHub PR merge event is received", func() {
	const testNamespace = "default"

	It("patches the external-merge-commit-sha annotation when PR number matches", func() {
		prName := "test-pr-merged"
		pr := createTestPullRequest(prName, testNamespace, "42")

		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, pr)
		})

		payload := buildPRMergePayload(42, "abc123def456", true)
		resp := sendWebhookRequest(payload, map[string]string{
			"X-Github-Event":    "pull_request",
			"X-Github-Delivery": "test-delivery-merge-1",
		})
		defer func() { _ = resp.Body.Close() }()

		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		// Wait for the annotation to be patched.
		Eventually(func(g Gomega) {
			var updatedPR promoterv1alpha1.PullRequest
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: prName, Namespace: testNamespace}, &updatedPR)).To(Succeed())
			g.Expect(updatedPR.Annotations).To(HaveKeyWithValue(
				promoterv1alpha1.ExternalMergeCommitSHAAnnotation, "abc123def456",
			))
		}, 30*time.Second, 500*time.Millisecond).Should(Succeed())
	})

	It("returns 204 and does nothing when no PullRequest matches the PR number", func() {
		// Use a PR number that has no matching PullRequest resource.
		payload := buildPRMergePayload(99999, "deadbeef", true)
		resp := sendWebhookRequest(payload, map[string]string{
			"X-Github-Event":    "pull_request",
			"X-Github-Delivery": "test-delivery-merge-2",
		})
		defer func() { _ = resp.Body.Close() }()

		// Should still respond 204 — non-fatal, just no-op.
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
	})

	It("ignores closed-but-not-merged PR events", func() {
		prName := "test-pr-closed-only"
		pr := createTestPullRequest(prName, testNamespace, "77")

		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, pr)
		})

		// merged=false means it was closed without merging.
		payload := buildPRMergePayload(77, "shouldnotappear", false)
		resp := sendWebhookRequest(payload, map[string]string{
			"X-Github-Event":    "pull_request",
			"X-Github-Delivery": "test-delivery-merge-3",
		})
		defer func() { _ = resp.Body.Close() }()

		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		// Verify that the annotation is NOT set.
		Consistently(func(g Gomega) {
			var updatedPR promoterv1alpha1.PullRequest
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: prName, Namespace: testNamespace}, &updatedPR)).To(Succeed())
			g.Expect(updatedPR.Annotations).NotTo(HaveKey(promoterv1alpha1.ExternalMergeCommitSHAAnnotation))
		}, 2*time.Second, 500*time.Millisecond).Should(Succeed())
	})
})
