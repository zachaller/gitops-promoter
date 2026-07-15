package utils_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

const (
	testInstanceID = "wave-0"
)

var _ = Describe("StampInstanceIDLabel", func() {
	It("returns an empty map when labels is nil and instanceID is nil", func() {
		labels := utils.StampInstanceIDLabel(nil, nil)
		Expect(labels).NotTo(BeNil())
		Expect(labels).To(BeEmpty())
	})

	It("preserves existing labels when instanceID is nil", func() {
		labels := utils.StampInstanceIDLabel(map[string]string{"k": "v"}, nil)
		Expect(labels).To(Equal(map[string]string{"k": "v"}))
	})

	It("stamps instance-id when instanceID is set", func() {
		labels := utils.StampInstanceIDLabel(map[string]string{"k": "v"}, ptr.To(testInstanceID))
		Expect(labels[promoterv1alpha1.InstanceIDLabel]).To(Equal(testInstanceID))
		Expect(labels["k"]).To(Equal("v"))
	})
})
