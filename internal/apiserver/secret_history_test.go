package apiserver

import (
	corev1 "k8s.io/api/core/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("secretCredentialDataEqual", func() {
	It("returns true when promoter credential keys are unchanged", func() {
		a := &corev1.Secret{Data: map[string][]byte{"token": []byte("a")}}
		b := &corev1.Secret{Data: map[string][]byte{"token": []byte("a"), "noise": []byte("x")}}
		Expect(secretCredentialDataEqual(a, b)).To(BeTrue())
	})

	It("returns false when a credential key changes", func() {
		a := &corev1.Secret{Data: map[string][]byte{"token": []byte("a")}}
		b := &corev1.Secret{Data: map[string][]byte{"token": []byte("b")}}
		Expect(secretCredentialDataEqual(a, b)).To(BeFalse())
	})
})
