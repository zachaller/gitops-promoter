package webhookreceiver_test

import (
	"context"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"testing"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	"github.com/argoproj-labs/gitops-promoter/internal/webhookreceiver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	testEnv         *envtest.Environment
	k8sClient       client.Client
	ctx             context.Context
	cancel          context.CancelFunc
	scheme          = utils.GetScheme()
	webhookRecvPort int
)

func TestWebhookReceiver(t *testing.T) {
	t.Parallel()

	RegisterFailHandler(Fail)

	c, _ := GinkgoConfiguration()

	RunSpecs(t, "WebhookReceiver Suite", c)
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.31.0-%s-%s", goruntime.GOOS, goruntime.GOARCH)),
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	ctx, cancel = context.WithCancel(context.Background())

	// Use a port offset to avoid conflicts with other test suites running in parallel.
	webhookRecvPort = 3399 + GinkgoParallelProcess()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})
	Expect(err).NotTo(HaveOccurred())

	// Register the field index on .status.id so findPullRequestByID can use MatchingFields.
	Expect(mgr.GetFieldIndexer().IndexField(ctx, &promoterv1alpha1.PullRequest{}, ".status.id", func(obj client.Object) []string {
		pr := obj.(*promoterv1alpha1.PullRequest)
		if pr.Status.ID == "" {
			return nil
		}
		return []string{pr.Status.ID}
	})).To(Succeed())

	k8sClient = mgr.GetClient()

	wr := webhookreceiver.NewWebhookReceiver(mgr, nil)

	// Start the webhook receiver as an HTTP server.
	go func() {
		defer GinkgoRecover()
		err := wr.Start(ctx, fmt.Sprintf(":%d", webhookRecvPort))
		Expect(err).To(Succeed())
	}()

	// Start the manager (required for the cache / field indexer to work).
	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()

	By("waiting for manager cache to sync")
	cache := mgr.GetCache()
	Eventually(func() bool {
		return cache.WaitForCacheSync(ctx)
	}, 30).Should(BeTrue(), "manager cache should sync")
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	if testEnv != nil {
		Expect(testEnv.Stop()).To(Succeed())
	}
})
