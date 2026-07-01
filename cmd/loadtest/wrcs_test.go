package loadtest

import (
	"testing"
	"text/template"
	"time"

	"github.com/expr-lang/expr"
	sprig "github.com/go-task/slim-sprig/v3"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// TestWebRequestCommitStatusExpressionsCompile compiles every CEL expression embedded in the
// change-management WebRequestCommitStatus trio against expr-lang, the same library the
// controller uses (internal/webrequest.ExpressionEvaluator). This doesn't exercise controller
// semantics, but it does catch the #1 way hand-edited multi-line expression strings break:
// syntax errors introduced while wrapping long lines for lll (mismatched braces/parens, stray
// operators, etc.).
func TestWebRequestCommitStatusExpressionsCompile(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Name:              "loadtest",
		Namespace:         "default",
		Mode:              ModeDirect,
		ChangeMgmtBaseURL: "http://localhost:8987/v1/change-management-service",
		Count:             1,
		TimedDurations:    [3]time.Duration{30 * time.Second, time.Minute, 2 * time.Minute},
	}
	inst := Instances(cfg)[0]

	for _, wrcs := range BuildWebRequestCommitStatuses(cfg, inst) {
		t.Run(wrcs.Name, func(t *testing.T) {
			t.Parallel()
			checkExpr(t, "success.when.variables", wrcs.Spec.Success.When.Variables)
			checkExprString(t, "success.when.expression", wrcs.Spec.Success.When.Expression)

			trigger := wrcs.Spec.Mode.Trigger
			if trigger == nil {
				t.Fatal("expected spec.mode.trigger to be set")
			}
			checkExpr(t, "trigger.when.variables", trigger.When.Variables)
			checkExprString(t, "trigger.when.expression", trigger.When.Expression)
			checkExpr(t, "trigger.when.output", trigger.When.Output)
			if trigger.Response != nil {
				checkExprString(t, "trigger.response.output.expression", trigger.Response.Output.Expression)
			}

			checkGoTemplate(t, "descriptionTemplate", wrcs.Spec.DescriptionTemplate)
			checkGoTemplate(t, "urlTemplate", wrcs.Spec.UrlTemplate)
			checkGoTemplate(t, "httpRequest.urlTemplate", wrcs.Spec.HTTPRequest.URLTemplate)
			checkGoTemplate(t, "httpRequest.methodTemplate", wrcs.Spec.HTTPRequest.MethodTemplate)
			checkGoTemplate(t, "httpRequest.bodyTemplate", wrcs.Spec.HTTPRequest.BodyTemplate)
			for header, tmpl := range wrcs.Spec.HTTPRequest.HeaderTemplates {
				checkGoTemplate(t, "httpRequest.headerTemplates["+header+"]", tmpl)
			}
		})
	}
}

// checkGoTemplate parses (but does not execute) a Go text/template string with the same Sprig
// func map the controller uses (internal/utils.RenderStringTemplate), catching syntax errors
// like unbalanced "{{ }}" or unknown function names without needing a full render data set.
func checkGoTemplate(t *testing.T, label, templateStr string) {
	t.Helper()
	if templateStr == "" {
		return
	}
	if _, err := template.New("").Funcs(sprig.GenericFuncMap()).Parse(templateStr); err != nil {
		t.Errorf("%s: failed to parse template: %v\ntemplate:\n%s", label, err, templateStr)
	}
}

func checkExpr(t *testing.T, label string, spec *promoterv1alpha1.OutputSpec) {
	t.Helper()
	if spec == nil {
		return
	}
	checkExprString(t, label, spec.Expression)
}

func checkExprString(t *testing.T, label, expression string) {
	t.Helper()
	if expression == "" {
		t.Fatalf("%s: expression is empty", label)
	}
	if _, err := expr.Compile(expression, expr.AllowUndefinedVariables()); err != nil {
		t.Errorf("%s: failed to compile: %v\nexpression:\n%s", label, err, expression)
	}
}
