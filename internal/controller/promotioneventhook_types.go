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

// PromotionEventHookTemplateContext is the context passed to Go templates when
// rendering webhook URLs/headers/bodies and Kubernetes resource templates in
// the PromotionEventHook controller.
type PromotionEventHookTemplateContext struct {
	// PromotionStrategy is the PromotionStrategy being watched.
	PromotionStrategy any `json:"PromotionStrategy"`
	// WebhookResponseData is the result of webhookResponseExpr evaluation (if present),
	// stored in status.webhookResponseData and available for templating.
	WebhookResponseData any `json:"WebhookResponseData"`
}
