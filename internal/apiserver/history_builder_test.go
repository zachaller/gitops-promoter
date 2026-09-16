package apiserver

import (
	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("historyShellFromPS", func() {
	It("mirrors PromotionStrategy metadata and owner refs", func() {
		ps := &promoterv1alpha1.PromotionStrategy{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "demo",
				Namespace:         "ns",
				UID:               "ps-uid-123",
				CreationTimestamp: metav1.Now(),
				Labels:            map[string]string{"app": "demo"},
			},
			Spec: promoterv1alpha1.PromotionStrategySpec{
				Environments: []promoterv1alpha1.Environment{
					{Branch: "env-a"},
					{Branch: "env-b"},
				},
			},
		}

		h := historyShellFromPS(ps, "42")
		Expect(h.Name).To(Equal("demo"))
		Expect(h.Namespace).To(Equal("ns"))
		Expect(h.ResourceVersion).To(Equal("42"))
		Expect(h.UID).To(Equal(historyUID(ps.UID)))
		Expect(h.OwnerReferences).To(HaveLen(1))
		Expect(h.OwnerReferences[0].Kind).To(Equal("PromotionStrategy"))
		Expect(h.OwnerReferences[0].UID).To(Equal(ps.UID))
		Expect(h.Environments).To(HaveLen(2))
		Expect(h.Environments[0].Branch).To(Equal("env-a"))
		Expect(h.Environments[1].Branch).To(Equal("env-b"))
		Expect(h.Environments[0].History).To(BeEmpty())
	})
})
