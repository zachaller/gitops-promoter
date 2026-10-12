package utils_test

import (
	"fmt"
	"sync"

	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("test rendering a template", func() {
	tests := map[string]struct {
		data     any
		template string
		expected string
		options  []string
		wantErr  bool
	}{
		"can render template successfully": {
			template: "Name: {{ .Name }}",
			data: map[string]string{
				"Name": "John",
			},
			expected: "Name: John",
		},
		"can render using sprig functions": {
			template: "Name: {{ trunc 1 .Name }}",
			data: map[string]string{
				"Name": "John",
			},
			expected: "Name: J",
		},
		"cannot render using sensitive sprig functions": {
			template: "{{ env HOME }}",
			wantErr:  true,
		},
		"can render template with options": {
			template: "{{ .Foo }}",
			data: map[string]string{
				"Bar": "John",
			},
			options:  []string{"missingkey=zero"},
			expected: "",
			wantErr:  false,
		},
	}

	for name, test := range tests {
		It(name, func() {
			result, err := utils.RenderStringTemplate(test.template, test.data, test.options...)
			if test.wantErr {
				Expect(err).To(HaveOccurred())
			} else {
				Expect(err).ToNot(HaveOccurred())
			}
			Expect(result).To(Equal(test.expected))
		})
	}
})

var _ = Describe("rendering a cached template", func() {
	It("renders the same template with different data", func() {
		const tmpl = "Name: {{ .Name }}"
		for _, name := range []string{"John", "Jane", "John"} {
			result, err := utils.RenderStringTemplate(tmpl, map[string]string{"Name": name})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("Name: " + name))
		}
	})

	It("does not share a cached template between different options", func() {
		const tmpl = "{{ .Foo }}"
		data := map[string]string{"Bar": "John"}

		result, err := utils.RenderStringTemplate(tmpl, data, "missingkey=zero")
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(""))

		_, err = utils.RenderStringTemplate(tmpl, data, "missingkey=error")
		Expect(err).To(HaveOccurred())

		result, err = utils.RenderStringTemplate(tmpl, data)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal("<no value>"))
	})

	It("returns the parse error every time for an invalid template", func() {
		for range 2 {
			_, err := utils.RenderStringTemplate("{{ .Name ", nil)
			Expect(err).To(MatchError(ContainSubstring("failed to parse template")))
		}
	})

	It("renders a cached template concurrently", func() {
		const tmpl = "{{ .N }}-{{ upper .S }}"
		var wg sync.WaitGroup
		results := make([]string, 50)
		errs := make([]error, 50)
		for i := range results {
			wg.Go(func() {
				results[i], errs[i] = utils.RenderStringTemplate(tmpl, map[string]any{"N": i, "S": "x"})
			})
		}
		wg.Wait()
		for i := range results {
			Expect(errs[i]).ToNot(HaveOccurred())
			Expect(results[i]).To(Equal(fmt.Sprintf("%d-X", i)))
		}
	})
})
