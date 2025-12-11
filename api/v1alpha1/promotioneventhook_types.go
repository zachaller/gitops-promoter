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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PromotionEventHookSpec defines the desired state of PromotionEventHook
type PromotionEventHookSpec struct {
	// PromotionStrategyRef is a reference to the PromotionStrategy to watch.
	// +required
	PromotionStrategyRef ObjectReference `json:"promotionStrategyRef"`

	// TriggerExpr is a Go expr expression that returns a map with `trigger` (bool) and custom data.
	// The expression has access to `promotionStrategy` and `status` (including triggerData, webhookResponseData).
	// Example: `{trigger: sha != "" && sha != status.triggerData["lastSha"], lastSha: sha}`
	// +required
	TriggerExpr string `json:"triggerExpr"`

	// Action defines the actions to execute when the trigger fires.
	// +required
	Action PromotionEventHookAction `json:"action"`

	// RetryPolicy defines how to handle retries on failure.
	// +optional
	RetryPolicy *RetryPolicy `json:"retryPolicy,omitempty"`
}

// PromotionEventHookAction defines the actions to execute.
// +kubebuilder:validation:XValidation:rule="has(self.webhook) || has(self.resource)",message="at least one of webhook or resource must be specified"
type PromotionEventHookAction struct {
	// Webhook defines an HTTP request to execute.
	// +optional
	Webhook *WebhookAction `json:"webhook,omitempty"`

	// Resource defines a Kubernetes resource to create or update.
	// +optional
	Resource *ResourceAction `json:"resource,omitempty"`
}

// WebhookAction defines an HTTP request configuration.
type WebhookAction struct {
	// URL is the URL to call. Supports Go template syntax with .PromotionStrategy context.
	// +required
	URL string `json:"url"`

	// Method is the HTTP method to use.
	// +kubebuilder:validation:Enum=GET;POST;PUT;PATCH;DELETE
	// +kubebuilder:default=POST
	// +optional
	Method string `json:"method,omitempty"`

	// Headers are HTTP headers to include. Values support Go template syntax and secret references.
	// Secret references use the format: ${secret:secret-name:key}
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// Body is the request body. Supports Go template syntax and secret references.
	// +optional
	Body string `json:"body,omitempty"`

	// Timeout is the request timeout.
	// +kubebuilder:default="30s"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// ContinueOnFailure determines whether to continue to resource action even if webhook fails.
	// +kubebuilder:default=false
	// +optional
	ContinueOnFailure bool `json:"continueOnFailure,omitempty"`

	// Auth defines authentication configuration for the webhook.
	// +optional
	Auth *WebhookAuth `json:"auth,omitempty"`

	// ResponseExpr is a Go expr expression that transforms the webhook response data.
	// The expression has access to `promotionStrategy` and `webhookResponse`.
	// The result is stored in status.webhookResponseData and made available to templates and future triggerExpr evaluations.
	// +optional
	ResponseExpr string `json:"responseExpr,omitempty"`
}

// WebhookAuth defines authentication configuration for webhooks.
// +kubebuilder:validation:XValidation:rule="[has(self.basic), has(self.bearer), has(self.oauth2), has(self.tls)].filter(x, x).size() <= 1",message="only one auth type can be specified"
type WebhookAuth struct {
	// Basic configures HTTP Basic Authentication.
	// +optional
	Basic *BasicAuth `json:"basic,omitempty"`

	// Bearer configures Bearer token authentication.
	// +optional
	Bearer *BearerAuth `json:"bearer,omitempty"`

	// OAuth2 configures OAuth2 client credentials flow.
	// +optional
	OAuth2 *OAuth2Auth `json:"oauth2,omitempty"`

	// TLS configures TLS client certificate authentication.
	// +optional
	TLS *TLSAuth `json:"tls,omitempty"`
}

// BasicAuth configures HTTP Basic Authentication.
type BasicAuth struct {
	// Username for basic auth. Can use secret ref: ${secret:name:key}
	// +optional
	Username string `json:"username,omitempty"`

	// Password for basic auth. Can use secret ref: ${secret:name:key}
	// +optional
	Password string `json:"password,omitempty"`

	// SecretRef references a secret containing username and password.
	// +optional
	SecretRef *BasicAuthSecretRef `json:"secretRef,omitempty"`
}

// BasicAuthSecretRef references a secret for basic auth credentials.
type BasicAuthSecretRef struct {
	// Name is the name of the secret.
	// +required
	Name string `json:"name"`

	// UsernameKey is the key in the secret containing the username.
	// +kubebuilder:default="username"
	// +optional
	UsernameKey string `json:"usernameKey,omitempty"`

	// PasswordKey is the key in the secret containing the password.
	// +kubebuilder:default="password"
	// +optional
	PasswordKey string `json:"passwordKey,omitempty"`
}

// BearerAuth configures Bearer token authentication.
type BearerAuth struct {
	// Token is the bearer token value. Can use secret ref: ${secret:name:key}
	// +optional
	Token string `json:"token,omitempty"`

	// SecretRef references a secret containing the token.
	// +optional
	SecretRef *BearerAuthSecretRef `json:"secretRef,omitempty"`
}

// BearerAuthSecretRef references a secret for bearer token.
type BearerAuthSecretRef struct {
	// Name is the name of the secret.
	// +required
	Name string `json:"name"`

	// Key is the key in the secret containing the token.
	// +kubebuilder:default="token"
	// +optional
	Key string `json:"key,omitempty"`
}

// OAuth2Auth configures OAuth2 client credentials flow.
type OAuth2Auth struct {
	// ClientID is the OAuth2 client ID. Can use secret ref: ${secret:name:key}
	// +optional
	ClientID string `json:"clientID,omitempty"`

	// ClientSecret is the OAuth2 client secret. Can use secret ref: ${secret:name:key}
	// +optional
	ClientSecret string `json:"clientSecret,omitempty"`

	// TokenURL is the OAuth2 token endpoint.
	// +required
	TokenURL string `json:"tokenURL"`

	// Scopes are optional OAuth2 scopes to request.
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// SecretRef references a secret containing client credentials.
	// +optional
	SecretRef *OAuth2SecretRef `json:"secretRef,omitempty"`
}

// OAuth2SecretRef references a secret for OAuth2 credentials.
type OAuth2SecretRef struct {
	// Name is the name of the secret.
	// +required
	Name string `json:"name"`

	// ClientIDKey is the key in the secret containing the client ID.
	// +kubebuilder:default="clientID"
	// +optional
	ClientIDKey string `json:"clientIDKey,omitempty"`

	// ClientSecretKey is the key in the secret containing the client secret.
	// +kubebuilder:default="clientSecret"
	// +optional
	ClientSecretKey string `json:"clientSecretKey,omitempty"`
}

// TLSAuth configures TLS client certificate authentication.
type TLSAuth struct {
	// SecretRef references a secret containing TLS certificates.
	// +required
	SecretRef TLSSecretRef `json:"secretRef"`
}

// TLSSecretRef references a secret for TLS certificates.
type TLSSecretRef struct {
	// Name is the name of the secret.
	// +required
	Name string `json:"name"`

	// CertKey is the key in the secret containing the client certificate.
	// +kubebuilder:default="tls.crt"
	// +optional
	CertKey string `json:"certKey,omitempty"`

	// KeyKey is the key in the secret containing the private key.
	// +kubebuilder:default="tls.key"
	// +optional
	KeyKey string `json:"keyKey,omitempty"`

	// CAKey is the key in the secret containing the CA certificate.
	// +kubebuilder:default="ca.crt"
	// +optional
	CAKey string `json:"caKey,omitempty"`
}

// ResourceAction defines a Kubernetes resource to create or update.
type ResourceAction struct {
	// Template is a Go template that renders to a Kubernetes resource YAML.
	// The template context includes .WebhookResponseData (from webhook's responseExpr if available) and .PromotionStrategy.
	// +required
	Template string `json:"template"`

	// SetOwnerReference determines whether to set the PromotionEventHook as owner of the created resource.
	// When true, the resource will be deleted when the PromotionEventHook is deleted.
	// +kubebuilder:default=false
	// +optional
	SetOwnerReference bool `json:"setOwnerReference,omitempty"`
}

// RetryPolicy defines how to handle retries on failure.
type RetryPolicy struct {
	// Strategy defines the retry strategy.
	// +kubebuilder:validation:Enum=none;fixed;exponential
	// +kubebuilder:default=exponential
	// +optional
	Strategy string `json:"strategy,omitempty"`

	// MaxAttempts is the maximum number of retry attempts.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxAttempts int `json:"maxAttempts,omitempty"`

	// InitialDelay is the initial delay before the first retry.
	// +kubebuilder:default="5s"
	// +optional
	InitialDelay *metav1.Duration `json:"initialDelay,omitempty"`

	// MaxDelay is the maximum delay between retries (for exponential strategy).
	// +kubebuilder:default="5m"
	// +optional
	MaxDelay *metav1.Duration `json:"maxDelay,omitempty"`
}

// PromotionEventHookStatus defines the observed state of PromotionEventHook
type PromotionEventHookStatus struct {
	// Conditions represent the latest available observations of the object's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastTriggerTime is when the trigger last fired and actions were executed.
	// +optional
	LastTriggerTime *metav1.Time `json:"lastTriggerTime,omitempty"`

	// LastEvaluationTime is when triggerExpr was last evaluated.
	// +optional
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`

	// TriggerData contains custom data from triggerExpr (excluding trigger boolean).
	// +optional
	TriggerData map[string]string `json:"triggerData,omitempty"`

	// WebhookResponseData contains the result of webhook's responseExpr evaluation.
	// This data is available to future triggerExpr evaluations and to resource templates.
	// +optional
	WebhookResponseData map[string]string `json:"webhookResponseData,omitempty"`

	// WebhookStatus contains the status of the last webhook action.
	// +optional
	WebhookStatus *WebhookActionStatus `json:"webhookStatus,omitempty"`

	// ResourceStatus contains the status of the last resource action.
	// +optional
	ResourceStatus *ResourceActionStatus `json:"resourceStatus,omitempty"`
}

// WebhookActionStatus contains the status of a webhook action.
type WebhookActionStatus struct {
	// Success indicates whether the last webhook attempt succeeded.
	Success bool `json:"success"`

	// Attempts is the number of attempts made.
	Attempts int `json:"attempts"`

	// LastAttemptTime is when the last attempt was made.
	// +optional
	LastAttemptTime *metav1.Time `json:"lastAttemptTime,omitempty"`

	// Error contains the error message if the last attempt failed.
	// +optional
	Error string `json:"error,omitempty"`

	// ResponseCode is the HTTP response code from the last attempt.
	// +optional
	ResponseCode int `json:"responseCode,omitempty"`
}

// ResourceActionStatus contains the status of a resource action.
type ResourceActionStatus struct {
	// Success indicates whether the last resource action succeeded.
	Success bool `json:"success"`

	// Attempts is the number of attempts made.
	Attempts int `json:"attempts"`

	// LastAttemptTime is when the last attempt was made.
	// +optional
	LastAttemptTime *metav1.Time `json:"lastAttemptTime,omitempty"`

	// Error contains the error message if the last attempt failed.
	// +optional
	Error string `json:"error,omitempty"`

	// ResourceRef contains a reference to the created/updated resource.
	// +optional
	ResourceRef *ResourceReference `json:"resourceRef,omitempty"`
}

// ResourceReference contains a reference to a Kubernetes resource.
type ResourceReference struct {
	// APIVersion is the API version of the resource.
	APIVersion string `json:"apiVersion"`

	// Kind is the kind of the resource.
	Kind string `json:"kind"`

	// Namespace is the namespace of the resource.
	Namespace string `json:"namespace"`

	// Name is the name of the resource.
	Name string `json:"name"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PromotionEventHook is the Schema for the promotioneventhooks API.
// It watches PromotionStrategy resources and fires webhooks and/or creates K8s resources
// based on expr expression evaluation.
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.spec.promotionStrategyRef.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Last Trigger",type=date,JSONPath=`.status.lastTriggerTime`
type PromotionEventHook struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of PromotionEventHook
	// +required
	Spec PromotionEventHookSpec `json:"spec"`

	// status defines the observed state of PromotionEventHook
	// +optional
	Status PromotionEventHookStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PromotionEventHookList contains a list of PromotionEventHook
type PromotionEventHookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionEventHook `json:"items"`
}

// GetConditions returns the conditions of the PromotionEventHook.
func (peh *PromotionEventHook) GetConditions() *[]metav1.Condition {
	return &peh.Status.Conditions
}

func init() {
	SchemeBuilder.Register(&PromotionEventHook{}, &PromotionEventHookList{})
}
