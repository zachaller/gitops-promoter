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
	"fmt"
	"math"
	"time"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/settings"
	promoterConditions "github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	defaultRetryStrategy   = "exponential"
	defaultMaxAttempts     = 3
	defaultInitialDelay    = 5 * time.Second
	defaultMaxDelay        = 5 * time.Minute
	defaultRequeueDuration = 5 * time.Minute
)

// PromotionEventHookReconciler reconciles a PromotionEventHook object
type PromotionEventHookReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    record.EventRecorder
	SettingsMgr *settings.Manager
}

// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotioneventhooks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotioneventhooks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotioneventhooks/finalizers,verbs=update
// +kubebuilder:rbac:groups=promoter.argoproj.io,resources=promotionstrategies,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *PromotionEventHookReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling PromotionEventHook")
	startTime := time.Now()

	var peh promoterv1alpha1.PromotionEventHook
	defer utils.HandleReconciliationResult(ctx, startTime, &peh, r.Client, r.Recorder, &err)

	// 1. Fetch the PromotionEventHook instance
	err = r.Get(ctx, req.NamespacedName, &peh, &client.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Info("PromotionEventHook not found")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get PromotionEventHook")
		return ctrl.Result{}, fmt.Errorf("failed to get PromotionEventHook %q: %w", req.Name, err)
	}

	// Remove any existing Ready condition. We want to start fresh.
	meta.RemoveStatusCondition(peh.GetConditions(), string(promoterConditions.Ready))

	// 2. Fetch the referenced PromotionStrategy
	var ps promoterv1alpha1.PromotionStrategy
	psKey := client.ObjectKey{
		Namespace: peh.Namespace,
		Name:      peh.Spec.PromotionStrategyRef.Name,
	}
	err = r.Get(ctx, psKey, &ps)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Error(err, "referenced PromotionStrategy not found", "promotionStrategy", peh.Spec.PromotionStrategyRef.Name)
			meta.SetStatusCondition(peh.GetConditions(), metav1.Condition{
				Type:    string(promoterConditions.Ready),
				Status:  metav1.ConditionFalse,
				Reason:  "PromotionStrategyNotFound",
				Message: fmt.Sprintf("Referenced PromotionStrategy %q not found", peh.Spec.PromotionStrategyRef.Name),
			})
			if updateErr := r.Status().Update(ctx, &peh); updateErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update status: %w", updateErr)
			}
			return ctrl.Result{RequeueAfter: defaultRequeueDuration}, nil
		}
		logger.Error(err, "failed to get PromotionStrategy")
		return ctrl.Result{}, fmt.Errorf("failed to get PromotionStrategy %q: %w", peh.Spec.PromotionStrategyRef.Name, err)
	}

	// 3. Evaluate triggerExpr
	now := metav1.Now()
	peh.Status.LastEvaluationTime = &now

	triggerResult, err := r.evaluateTriggerExpr(ctx, &peh, &ps)
	if err != nil {
		logger.Error(err, "failed to evaluate triggerExpr")
		meta.SetStatusCondition(peh.GetConditions(), metav1.Condition{
			Type:    string(promoterConditions.Ready),
			Status:  metav1.ConditionFalse,
			Reason:  "TriggerExprError",
			Message: fmt.Sprintf("Failed to evaluate triggerExpr: %v", err),
		})
		if updateErr := r.Status().Update(ctx, &peh); updateErr != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", updateErr)
		}
		return ctrl.Result{RequeueAfter: defaultRequeueDuration}, nil
	}

	// Store trigger data (excluding trigger boolean)
	peh.Status.TriggerData = triggerResult.TriggerData

	// 4. Check if we should fire
	if !triggerResult.Trigger {
		logger.V(4).Info("triggerExpr returned trigger=false, skipping actions")
		meta.SetStatusCondition(peh.GetConditions(), metav1.Condition{
			Type:    string(promoterConditions.Ready),
			Status:  metav1.ConditionTrue,
			Reason:  "TriggerNotFired",
			Message: "Trigger condition not met, waiting for next evaluation",
		})
		if updateErr := r.Status().Update(ctx, &peh); updateErr != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", updateErr)
		}
		return ctrl.Result{RequeueAfter: defaultRequeueDuration}, nil
	}

	// 5. Execute actions sequentially
	logger.Info("Trigger fired, executing actions")
	peh.Status.LastTriggerTime = &now

	var webhookResp *utils.WebhookResponse
	var webhookErr error

	// Execute webhook if present
	//
	//nolint:nestif // Webhook execution and error handling have inherent complexity
	if peh.Spec.Action.Webhook != nil {
		webhookResp, webhookErr = r.executeWebhook(ctx, &peh, &ps)
		r.updateWebhookStatus(&peh, webhookResp, webhookErr)

		// Evaluate webhookResponseExpr if webhook succeeded and expr is present
		if webhookErr == nil && webhookResp != nil && peh.Spec.WebhookResponseExpr != "" {
			if err := r.evaluateWebhookResponseExpr(&peh, &ps, webhookResp); err != nil {
				logger.Error(err, "failed to evaluate webhookResponseExpr")
				// This is not a fatal error - continue with resource action
			}
		}

		if webhookErr != nil && !peh.Spec.Action.Webhook.ContinueOnFailure {
			logger.Error(webhookErr, "webhook failed and continueOnFailure is false")
			meta.SetStatusCondition(peh.GetConditions(), metav1.Condition{
				Type:    string(promoterConditions.Ready),
				Status:  metav1.ConditionFalse,
				Reason:  "WebhookFailed",
				Message: fmt.Sprintf("Webhook failed: %v", webhookErr),
			})
			if updateErr := r.Status().Update(ctx, &peh); updateErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update status: %w", updateErr)
			}
			return r.handleRetry(ctx, &peh), nil
		}
	} else if peh.Spec.WebhookResponseExpr != "" {
		// If no webhook but webhookResponseExpr is present, evaluate it without webhook response
		// This allows resource-only actions to use webhookResponseExpr for data transformation
		if err := r.evaluateWebhookResponseExpr(&peh, &ps, nil); err != nil {
			logger.Error(err, "failed to evaluate webhookResponseExpr")
			// This is not a fatal error - continue with resource action
		}
	}

	// Execute resource if present
	if peh.Spec.Action.Resource != nil {
		resourceResult, resourceErr := r.executeResource(ctx, &peh, &ps, webhookResp)
		r.updateResourceStatus(&peh, resourceResult, resourceErr)

		if resourceErr != nil {
			logger.Error(resourceErr, "resource action failed")
			meta.SetStatusCondition(peh.GetConditions(), metav1.Condition{
				Type:    string(promoterConditions.Ready),
				Status:  metav1.ConditionFalse,
				Reason:  "ResourceFailed",
				Message: fmt.Sprintf("Resource action failed: %v", resourceErr),
			})
			if updateErr := r.Status().Update(ctx, &peh); updateErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update status: %w", updateErr)
			}
			return r.handleRetry(ctx, &peh), nil
		}
	}

	// 6. All actions succeeded
	logger.Info("All actions completed successfully")

	// Reset attempt counters on success
	if peh.Status.WebhookStatus != nil {
		peh.Status.WebhookStatus.Attempts = 0
	}
	if peh.Status.ResourceStatus != nil {
		peh.Status.ResourceStatus.Attempts = 0
	}

	meta.SetStatusCondition(peh.GetConditions(), metav1.Condition{
		Type:    string(promoterConditions.Ready),
		Status:  metav1.ConditionTrue,
		Reason:  "ActionsSucceeded",
		Message: "All actions completed successfully",
	})

	if updateErr := r.Status().Update(ctx, &peh); updateErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", updateErr)
	}

	return ctrl.Result{RequeueAfter: defaultRequeueDuration}, nil
}

// evaluateTriggerExpr evaluates the triggerExpr and returns the result.
func (r *PromotionEventHookReconciler) evaluateTriggerExpr(_ context.Context, peh *promoterv1alpha1.PromotionEventHook, ps *promoterv1alpha1.PromotionStrategy) (utils.TriggerResult, error) {
	exprCtx := utils.TriggerContext{
		PromotionStrategy: ps,
		Status:            &peh.Status,
	}

	result, err := utils.EvaluateMap(peh.Spec.TriggerExpr, exprCtx)
	if err != nil {
		return utils.TriggerResult{}, fmt.Errorf("failed to evaluate expression: %w", err)
	}

	triggerResult, err := utils.ParseTriggerResult(result)
	if err != nil {
		return utils.TriggerResult{}, fmt.Errorf("failed to parse trigger result: %w", err)
	}
	return triggerResult, nil
}

// evaluateWebhookResponseExpr evaluates the webhookResponseExpr and stores the result in status.
func (r *PromotionEventHookReconciler) evaluateWebhookResponseExpr(peh *promoterv1alpha1.PromotionEventHook, ps *promoterv1alpha1.PromotionStrategy, webhookResp *utils.WebhookResponse) error {
	exprCtx := utils.WebhookResponseContext{
		PromotionStrategy: ps,
		WebhookResponse:   webhookResp,
	}

	result, err := utils.EvaluateAny(peh.Spec.WebhookResponseExpr, exprCtx)
	if err != nil {
		return fmt.Errorf("failed to evaluate webhookResponseExpr: %w", err)
	}

	// Convert result to map[string]string for storage in status
	resultMap, ok := result.(map[string]any)
	if !ok {
		return fmt.Errorf("webhookResponseExpr must return a map, got %T", result)
	}

	webhookResponseData, err := utils.MapToStringMap(resultMap)
	if err != nil {
		return fmt.Errorf("failed to convert webhookResponseExpr result to string map: %w", err)
	}

	peh.Status.WebhookResponseData = webhookResponseData
	return nil
}

// executeWebhook executes the webhook action.
func (r *PromotionEventHookReconciler) executeWebhook(ctx context.Context, peh *promoterv1alpha1.PromotionEventHook, ps *promoterv1alpha1.PromotionStrategy) (*utils.WebhookResponse, error) {
	executor := NewWebhookExecutor(r.Client, r.Recorder)
	return executor.Execute(ctx, peh, ps)
}

// executeResource executes the resource action.
func (r *PromotionEventHookReconciler) executeResource(ctx context.Context, peh *promoterv1alpha1.PromotionEventHook, ps *promoterv1alpha1.PromotionStrategy, webhookResp *utils.WebhookResponse) (*utils.ResourceResult, error) {
	executor := NewResourceExecutor(r.Client, r.Scheme, r.Recorder)
	return executor.Execute(ctx, peh, ps, webhookResp)
}

// updateWebhookStatus updates the webhook status in the PromotionEventHook.
func (r *PromotionEventHookReconciler) updateWebhookStatus(peh *promoterv1alpha1.PromotionEventHook, resp *utils.WebhookResponse, err error) {
	now := metav1.Now()

	if peh.Status.WebhookStatus == nil {
		peh.Status.WebhookStatus = &promoterv1alpha1.WebhookActionStatus{}
	}

	peh.Status.WebhookStatus.LastAttemptTime = &now
	peh.Status.WebhookStatus.Attempts++

	if err != nil {
		peh.Status.WebhookStatus.Success = false
		peh.Status.WebhookStatus.Error = err.Error()
		if resp != nil {
			peh.Status.WebhookStatus.ResponseCode = resp.StatusCode
		}
	} else {
		peh.Status.WebhookStatus.Success = true
		peh.Status.WebhookStatus.Error = ""
		if resp != nil {
			peh.Status.WebhookStatus.ResponseCode = resp.StatusCode
		}
	}
}

// updateResourceStatus updates the resource status in the PromotionEventHook.
func (r *PromotionEventHookReconciler) updateResourceStatus(peh *promoterv1alpha1.PromotionEventHook, result *utils.ResourceResult, err error) {
	now := metav1.Now()

	if peh.Status.ResourceStatus == nil {
		peh.Status.ResourceStatus = &promoterv1alpha1.ResourceActionStatus{}
	}

	peh.Status.ResourceStatus.LastAttemptTime = &now
	peh.Status.ResourceStatus.Attempts++

	if err != nil {
		peh.Status.ResourceStatus.Success = false
		peh.Status.ResourceStatus.Error = err.Error()
	} else {
		peh.Status.ResourceStatus.Success = true
		peh.Status.ResourceStatus.Error = ""
		if result != nil {
			peh.Status.ResourceStatus.ResourceRef = &promoterv1alpha1.ResourceReference{
				APIVersion: result.APIVersion,
				Kind:       result.Kind,
				Namespace:  result.Namespace,
				Name:       result.Name,
			}
		}
	}
}

// handleRetry calculates the retry delay and returns the appropriate Result.
func (r *PromotionEventHookReconciler) handleRetry(_ context.Context, peh *promoterv1alpha1.PromotionEventHook) ctrl.Result {
	// Get retry policy settings
	strategy := defaultRetryStrategy
	maxAttempts := defaultMaxAttempts
	initialDelay := defaultInitialDelay
	maxDelay := defaultMaxDelay

	if peh.Spec.RetryPolicy != nil {
		if peh.Spec.RetryPolicy.Strategy != "" {
			strategy = peh.Spec.RetryPolicy.Strategy
		}
		if peh.Spec.RetryPolicy.MaxAttempts > 0 {
			maxAttempts = peh.Spec.RetryPolicy.MaxAttempts
		}
		if peh.Spec.RetryPolicy.InitialDelay != nil {
			initialDelay = peh.Spec.RetryPolicy.InitialDelay.Duration
		}
		if peh.Spec.RetryPolicy.MaxDelay != nil {
			maxDelay = peh.Spec.RetryPolicy.MaxDelay.Duration
		}
	}

	// Calculate current attempts
	attempts := 0
	if peh.Status.WebhookStatus != nil && !peh.Status.WebhookStatus.Success {
		attempts = peh.Status.WebhookStatus.Attempts
	}
	if peh.Status.ResourceStatus != nil && !peh.Status.ResourceStatus.Success {
		if peh.Status.ResourceStatus.Attempts > attempts {
			attempts = peh.Status.ResourceStatus.Attempts
		}
	}

	// Check if max attempts reached
	if attempts >= maxAttempts {
		r.Recorder.Eventf(peh, "Warning", "MaxRetriesReached", "Max retry attempts (%d) reached", maxAttempts)
		// Don't requeue - permanent failure
		return ctrl.Result{}
	}

	// Calculate delay based on strategy
	var delay time.Duration
	if strategy == "none" || strategy == "" {
		// No retry
		return ctrl.Result{}
	}

	if strategy == "fixed" {
		delay = initialDelay
	} else {
		// exponential backoff: initialDelay * 2^(attempts-1)
		delay = time.Duration(float64(initialDelay) * math.Pow(2, float64(attempts-1)))
		if delay > maxDelay {
			delay = maxDelay
		}
	}

	return ctrl.Result{RequeueAfter: delay}
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromotionEventHookReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	// Use Direct methods to read configuration from the API server without cache during setup.
	// The cache is not started during SetupWithManager, so we must use the non-cached API reader.
	rateLimiter, err := settings.GetRateLimiterDirect[promoterv1alpha1.PromotionEventHookConfiguration, ctrl.Request](ctx, r.SettingsMgr)
	if err != nil {
		// If configuration doesn't exist yet, use default rate limiter
		rateLimiter = nil
	}

	maxConcurrentReconciles, err := settings.GetMaxConcurrentReconcilesDirect[promoterv1alpha1.PromotionEventHookConfiguration](ctx, r.SettingsMgr)
	if err != nil {
		// If configuration doesn't exist yet, use default
		maxConcurrentReconciles = 1
	}

	opts := controller.Options{
		MaxConcurrentReconciles: maxConcurrentReconciles,
	}
	if rateLimiter != nil {
		opts.RateLimiter = rateLimiter
	}

	err = ctrl.NewControllerManagedBy(mgr).
		For(&promoterv1alpha1.PromotionEventHook{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&promoterv1alpha1.PromotionStrategy{}, r.enqueuePromotionEventHookForPromotionStrategy()).
		WithOptions(opts).
		Complete(r)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}
	return nil
}

// enqueuePromotionEventHookForPromotionStrategy returns a handler that enqueues all PromotionEventHook resources
// that reference a PromotionStrategy when that PromotionStrategy changes
func (r *PromotionEventHookReconciler) enqueuePromotionEventHookForPromotionStrategy() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
		ps, ok := obj.(*promoterv1alpha1.PromotionStrategy)
		if !ok {
			return nil
		}

		// List all PromotionEventHook resources in the same namespace
		var pehList promoterv1alpha1.PromotionEventHookList
		if err := r.List(ctx, &pehList, client.InNamespace(ps.Namespace)); err != nil {
			log.FromContext(ctx).Error(err, "failed to list PromotionEventHook resources")
			return nil
		}

		// Enqueue all PromotionEventHook resources that reference this PromotionStrategy
		var requests []ctrl.Request
		for _, peh := range pehList.Items {
			if peh.Spec.PromotionStrategyRef.Name == ps.Name {
				requests = append(requests, ctrl.Request{
					NamespacedName: client.ObjectKeyFromObject(&peh),
				})
			}
		}

		return requests
	})
}
