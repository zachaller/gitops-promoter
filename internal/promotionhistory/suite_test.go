package promotionhistory

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPromotionHistory(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PromotionHistory Suite")
}
