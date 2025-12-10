/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultTimeout         = 30 * time.Second
	maxResponseBodySize    = 1024 * 1024 // 1MB
	responseBodyPreviewLen = 1024        // 1KB for status preview
)

// WebhookExecutor handles webhook execution with authentication.
type WebhookExecutor struct {
	client   client.Client
	recorder record.EventRecorder
}

// NewWebhookExecutor creates a new WebhookExecutor.
func NewWebhookExecutor(c client.Client, recorder record.EventRecorder) *WebhookExecutor {
	return &WebhookExecutor{
		client:   c,
		recorder: recorder,
	}
}

// Execute executes a webhook action and returns the response.
func (w *WebhookExecutor) Execute(ctx context.Context, hook *promoterv1alpha1.PromotionEventHook, ps *promoterv1alpha1.PromotionStrategy) (*utils.WebhookResponse, error) {
	webhook := hook.Spec.Action.Webhook
	if webhook == nil {
		return nil, errors.New("webhook action is nil")
	}

	secretResolver := utils.NewSecretResolver(w.client, hook.Namespace)

	// Create HTTP client with appropriate timeout and TLS config
	httpClient, err := w.createHTTPClient(ctx, webhook, secretResolver)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Build template context
	templateCtx := PromotionEventHookTemplateContext{
		PromotionStrategy: ps,
	}

	// Render URL
	url, err := utils.RenderStringTemplate(webhook.URL, templateCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to render URL template: %w", err)
	}

	// Resolve secret references in URL
	url, err = secretResolver.ResolveString(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve secrets in URL: %w", err)
	}

	// Render body
	var body string
	if webhook.Body != "" {
		body, err = utils.RenderStringTemplate(webhook.Body, templateCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to render body template: %w", err)
		}
		// Resolve secret references in body
		body, err = secretResolver.ResolveString(ctx, body)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve secrets in body: %w", err)
		}
	}

	// Create request
	method := webhook.Method
	if method == "" {
		method = http.MethodPost
	}

	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Render and set headers
	if webhook.Headers != nil {
		for key, value := range webhook.Headers {
			renderedValue, err := utils.RenderStringTemplate(value, templateCtx)
			if err != nil {
				return nil, fmt.Errorf("failed to render header %q template: %w", key, err)
			}
			resolvedValue, err := secretResolver.ResolveString(ctx, renderedValue)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve secrets in header %q: %w", key, err)
			}
			req.Header.Set(key, resolvedValue)
		}
	}

	// Apply authentication (except TLS which is handled in client creation)
	if webhook.Auth != nil {
		if err := w.applyAuth(ctx, req, httpClient, webhook.Auth, secretResolver); err != nil {
			return nil, fmt.Errorf("failed to apply authentication: %w", err)
		}
	}

	// Execute request
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Read response body (with size limit)
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Build response
	webhookResp := &utils.WebhookResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       string(bodyBytes),
	}

	// Try to parse JSON response
	var jsonData any
	if err := json.Unmarshal(bodyBytes, &jsonData); err == nil {
		webhookResp.Data = jsonData
	}

	// Check for success (2xx status codes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return webhookResp, fmt.Errorf("webhook returned non-success status code: %d", resp.StatusCode)
	}

	return webhookResp, nil
}

// createHTTPClient creates an HTTP client with appropriate configuration.
func (w *WebhookExecutor) createHTTPClient(ctx context.Context, webhook *promoterv1alpha1.WebhookAction, secretResolver *utils.SecretResolver) (*http.Client, error) {
	timeout := defaultTimeout
	if webhook.Timeout != nil {
		timeout = webhook.Timeout.Duration
	}

	transport := &http.Transport{}

	// Configure TLS if TLS auth is specified
	if webhook.Auth != nil && webhook.Auth.TLS != nil {
		tlsConfig, err := w.buildTLSConfig(ctx, webhook.Auth.TLS, secretResolver)
		if err != nil {
			return nil, fmt.Errorf("failed to build TLS config: %w", err)
		}
		transport.TLSClientConfig = tlsConfig
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, nil
}

// applyAuth applies authentication to the request.
func (w *WebhookExecutor) applyAuth(ctx context.Context, req *http.Request, _ *http.Client, auth *promoterv1alpha1.WebhookAuth, secretResolver *utils.SecretResolver) error {
	if auth.Basic != nil {
		return w.applyBasicAuth(ctx, req, auth.Basic, secretResolver)
	}
	if auth.Bearer != nil {
		return w.applyBearerAuth(ctx, req, auth.Bearer, secretResolver)
	}
	if auth.OAuth2 != nil {
		return w.applyOAuth2Auth(ctx, req, auth.OAuth2, secretResolver)
	}
	// TLS auth is handled in createHTTPClient
	return nil
}

// applyBasicAuth applies HTTP Basic Authentication.
//
//nolint:nestif // Auth secret handling has inherent nesting complexity
func (w *WebhookExecutor) applyBasicAuth(ctx context.Context, req *http.Request, auth *promoterv1alpha1.BasicAuth, secretResolver *utils.SecretResolver) error {
	var username, password string
	var err error

	if auth.SecretRef != nil {
		usernameKey := auth.SecretRef.UsernameKey
		if usernameKey == "" {
			usernameKey = "username"
		}
		passwordKey := auth.SecretRef.PasswordKey
		if passwordKey == "" {
			passwordKey = "password"
		}

		username, err = secretResolver.GetSecretValue(ctx, auth.SecretRef.Name, usernameKey)
		if err != nil {
			return fmt.Errorf("failed to get username from secret: %w", err)
		}
		password, err = secretResolver.GetSecretValue(ctx, auth.SecretRef.Name, passwordKey)
		if err != nil {
			return fmt.Errorf("failed to get password from secret: %w", err)
		}
	} else {
		username, err = secretResolver.ResolveString(ctx, auth.Username)
		if err != nil {
			return fmt.Errorf("failed to resolve username: %w", err)
		}
		password, err = secretResolver.ResolveString(ctx, auth.Password)
		if err != nil {
			return fmt.Errorf("failed to resolve password: %w", err)
		}
	}

	credentials := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	req.Header.Set("Authorization", "Basic "+credentials)
	return nil
}

// applyBearerAuth applies Bearer token authentication.
func (w *WebhookExecutor) applyBearerAuth(ctx context.Context, req *http.Request, auth *promoterv1alpha1.BearerAuth, secretResolver *utils.SecretResolver) error {
	var token string
	var err error

	if auth.SecretRef != nil {
		key := auth.SecretRef.Key
		if key == "" {
			key = "token"
		}
		token, err = secretResolver.GetSecretValue(ctx, auth.SecretRef.Name, key)
		if err != nil {
			return fmt.Errorf("failed to get token from secret: %w", err)
		}
	} else {
		token, err = secretResolver.ResolveString(ctx, auth.Token)
		if err != nil {
			return fmt.Errorf("failed to resolve token: %w", err)
		}
	}

	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// applyOAuth2Auth applies OAuth2 client credentials authentication.
//
//nolint:nestif // Auth secret handling has inherent nesting complexity
func (w *WebhookExecutor) applyOAuth2Auth(ctx context.Context, req *http.Request, auth *promoterv1alpha1.OAuth2Auth, secretResolver *utils.SecretResolver) error {
	var clientID, clientSecret string
	var err error

	if auth.SecretRef != nil {
		clientIDKey := auth.SecretRef.ClientIDKey
		if clientIDKey == "" {
			clientIDKey = "clientID"
		}
		clientSecretKey := auth.SecretRef.ClientSecretKey
		if clientSecretKey == "" {
			clientSecretKey = "clientSecret"
		}

		clientID, err = secretResolver.GetSecretValue(ctx, auth.SecretRef.Name, clientIDKey)
		if err != nil {
			return fmt.Errorf("failed to get clientID from secret: %w", err)
		}
		clientSecret, err = secretResolver.GetSecretValue(ctx, auth.SecretRef.Name, clientSecretKey)
		if err != nil {
			return fmt.Errorf("failed to get clientSecret from secret: %w", err)
		}
	} else {
		clientID, err = secretResolver.ResolveString(ctx, auth.ClientID)
		if err != nil {
			return fmt.Errorf("failed to resolve clientID: %w", err)
		}
		clientSecret, err = secretResolver.ResolveString(ctx, auth.ClientSecret)
		if err != nil {
			return fmt.Errorf("failed to resolve clientSecret: %w", err)
		}
	}

	// Create OAuth2 config
	config := clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     auth.TokenURL,
		Scopes:       auth.Scopes,
	}

	// Get token
	token, err := config.Token(ctx)
	if err != nil {
		return fmt.Errorf("failed to get OAuth2 token: %w", err)
	}

	// Set Authorization header
	token.SetAuthHeader(req)
	return nil
}

// buildTLSConfig builds TLS configuration from secrets.
func (w *WebhookExecutor) buildTLSConfig(ctx context.Context, auth *promoterv1alpha1.TLSAuth, secretResolver *utils.SecretResolver) (*tls.Config, error) {
	certKey := auth.SecretRef.CertKey
	if certKey == "" {
		certKey = "tls.crt"
	}
	keyKey := auth.SecretRef.KeyKey
	if keyKey == "" {
		keyKey = "tls.key"
	}
	caKey := auth.SecretRef.CAKey
	if caKey == "" {
		caKey = "ca.crt"
	}

	// Get certificate and key
	certPEM, err := secretResolver.GetSecretValue(ctx, auth.SecretRef.Name, certKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate: %w", err)
	}
	keyPEM, err := secretResolver.GetSecretValue(ctx, auth.SecretRef.Name, keyKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get private key: %w", err)
	}

	// Parse certificate
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// Optionally add CA certificate
	caPEM, err := secretResolver.GetSecretValue(ctx, auth.SecretRef.Name, caKey)
	if err == nil && caPEM != "" {
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("failed to parse CA certificate")
		}
		tlsConfig.RootCAs = caCertPool
	}

	return tlsConfig, nil
}

// TruncateResponseBody truncates response body for status storage.
func TruncateResponseBody(body string) string {
	if len(body) <= responseBodyPreviewLen {
		return body
	}
	return body[:responseBodyPreviewLen] + "..."
}

// Ensure oauth2.Token is used to avoid import error
var _ = oauth2.Token{}
