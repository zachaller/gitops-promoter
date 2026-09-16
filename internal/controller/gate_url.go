package controller

import (
	"context"
	"fmt"
	"net/url"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
)

// urlSchemeHTTP and urlSchemeHTTPS are the only schemes allowed on a child CommitStatus details link.
// An SCM renders spec.url as a clickable link, so anything else (javascript:, file:, a bare path) is
// either useless or unsafe.
const (
	urlSchemeHTTP  = "http"
	urlSchemeHTTPS = "https"
)

// renderGateCommitStatusURL renders a gate's spec.url template against data and validates the result.
// It returns "" when the gate configures no template, which callers treat as "leave spec.url unset".
//
// Shared by every gate that supports url.template so they cannot drift on what counts as a valid link.
func renderGateCommitStatusURL(
	ctx context.Context,
	urlConfig promoterv1alpha1.URLConfig,
	data any,
	environment string,
	commitStatusName string,
	namespace string,
) (string, error) {
	if urlConfig.Template == "" {
		return "", nil
	}

	renderedURL, err := utils.RenderStringTemplate(urlConfig.Template, data, urlConfig.Options...)
	if err != nil {
		return "", fmt.Errorf("failed to render URL template: %w", err)
	}

	parsedURL, err := url.Parse(renderedURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL: %w", err)
	}
	if parsedURL.Scheme != urlSchemeHTTP && parsedURL.Scheme != urlSchemeHTTPS {
		return "", fmt.Errorf("URL scheme is not http or https: %s", parsedURL.Scheme)
	}

	logf.FromContext(ctx).V(4).Info("Rendered URL template",
		"url", renderedURL,
		"environment", environment,
		"commitStatus", commitStatusName,
		"namespace", namespace)

	return renderedURL, nil
}
