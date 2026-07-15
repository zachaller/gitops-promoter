package settings_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/settings"
)

func TestSettings(t *testing.T) {
	t.Parallel()

	RegisterFailHandler(Fail)
	RunSpecs(t, "Settings Suite")
}

func newManagerWithLiveInstanceID(live *string) *settings.Manager {
	cc := &promoterv1alpha1.ControllerConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      settings.ControllerConfigurationName,
			Namespace: "default",
		},
		Spec: promoterv1alpha1.ControllerConfigurationSpec{
			InstanceID: live,
		},
	}
	scheme := runtime.NewScheme()
	Expect(promoterv1alpha1.AddToScheme(scheme)).To(Succeed())
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cc).Build()
	return settings.NewManager(cl, cl, settings.ManagerConfig{ControllerNamespace: "default"})
}

var _ = Describe("StartupInstanceID", func() {
	It("returns nil before initialization (default install)", func() {
		Expect(settings.StartupInstanceID()).To(BeNil())
	})

	It("returns the value set for tests and restores the previous value", func() {
		restore := settings.SetStartupInstanceIDForTest(ptr.To("wave-0"))
		Expect(settings.StartupInstanceID()).To(HaveValue(Equal("wave-0")))
		restore()
		Expect(settings.StartupInstanceID()).To(BeNil())
	})
})

var _ = Describe("EnsureInstanceIDStable", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("returns nil when startup and live instance IDs are both unset", func() {
		mgr := newManagerWithLiveInstanceID(nil)
		Expect(mgr.EnsureInstanceIDStable(ctx)).To(Succeed())
	})

	It("returns nil when startup and live instance IDs match", func() {
		restore := settings.SetStartupInstanceIDForTest(ptr.To("wave-0"))
		defer restore()

		mgr := newManagerWithLiveInstanceID(ptr.To("wave-0"))
		Expect(mgr.EnsureInstanceIDStable(ctx)).To(Succeed())
	})

	It("returns an error when the live instance ID drifts from startup", func() {
		mgr := newManagerWithLiveInstanceID(ptr.To("wave-0"))
		err := mgr.EnsureInstanceIDStable(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("drifted since startup"))
		Expect(err.Error()).To(ContainSubstring("<unset>"))
		Expect(err.Error()).To(ContainSubstring("wave-0"))
	})

	It("returns an error when the live instance ID is cleared after multi-install startup", func() {
		restore := settings.SetStartupInstanceIDForTest(ptr.To("wave-0"))
		defer restore()

		mgr := newManagerWithLiveInstanceID(nil)
		err := mgr.EnsureInstanceIDStable(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("drifted since startup"))
	})

	It("returns an error when the ControllerConfiguration cannot be read", func() {
		scheme := runtime.NewScheme()
		Expect(promoterv1alpha1.AddToScheme(scheme)).To(Succeed())
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		mgr := settings.NewManager(cl, cl, settings.ManagerConfig{ControllerNamespace: "default"})

		err := mgr.EnsureInstanceIDStable(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("read live ControllerConfiguration instanceID"))
	})
})

var _ = Describe("GetInstanceID", func() {
	It("returns the live spec.instanceID", func() {
		mgr := newManagerWithLiveInstanceID(ptr.To("wave-1"))
		live, err := mgr.GetInstanceID(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(live).To(HaveValue(Equal("wave-1")))
	})
})
