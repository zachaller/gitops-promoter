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

package utils

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/expr-lang/expr"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// TriggerContext is the context available to triggerExpr expressions.
type TriggerContext struct {
	PromotionStrategy *promoterv1alpha1.PromotionStrategy        `expr:"promotionStrategy"`
	Status            *promoterv1alpha1.PromotionEventHookStatus `expr:"status"`
}

// WebhookResponseContext is the context available to webhookResponseExpr expressions.
type WebhookResponseContext struct {
	PromotionStrategy *promoterv1alpha1.PromotionStrategy `expr:"promotionStrategy"`
	WebhookResponse   *WebhookResponse                    `expr:"webhookResponse"`
}

// WebhookResponse holds webhook response data for expr context.
type WebhookResponse struct {
	Headers    map[string][]string `expr:"headers"`
	Data       any                 `expr:"data"` // Parsed JSON, or nil
	Body       string              `expr:"body"`
	StatusCode int                 `expr:"statusCode"`
}

// ResourceResult holds info about created/updated resource.
type ResourceResult struct {
	APIVersion string `expr:"apiVersion"`
	Kind       string `expr:"kind"`
	Namespace  string `expr:"namespace"`
	Name       string `expr:"name"`
}

// TriggerResult represents parsed result from triggerExpr.
type TriggerResult struct {
	TriggerData map[string]string
	Trigger     bool
}

// EvaluateMap evaluates an expr expression and returns the result as a map.
func EvaluateMap(exprStr string, ctx any) (map[string]any, error) {
	program, err := expr.Compile(exprStr, expr.Env(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to compile expression: %w", err)
	}

	output, err := expr.Run(program, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate expression: %w", err)
	}

	result, ok := output.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expression must return a map, got %T", output)
	}

	return result, nil
}

// EvaluateAny evaluates an expr expression and returns the result as any type.
func EvaluateAny(exprStr string, ctx any) (any, error) {
	program, err := expr.Compile(exprStr, expr.Env(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to compile expression: %w", err)
	}

	output, err := expr.Run(program, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate expression: %w", err)
	}

	return output, nil
}

// ParseTriggerResult parses the result of a triggerExpr evaluation.
func ParseTriggerResult(result map[string]any) (TriggerResult, error) {
	tr := TriggerResult{
		TriggerData: make(map[string]string),
	}

	// Extract trigger
	triggerVal, ok := result["trigger"]
	if !ok {
		return tr, errors.New("triggerExpr must return a map with 'trigger' key")
	}

	trigger, ok := triggerVal.(bool)
	if !ok {
		return tr, fmt.Errorf("'trigger' must be a boolean, got %T", triggerVal)
	}
	tr.Trigger = trigger

	// Extract all other fields as triggerData
	for key, value := range result {
		if key == "trigger" {
			continue
		}
		strVal := toStringValue(value)
		tr.TriggerData[key] = strVal
	}

	return tr, nil
}

// ValidateTemplateData validates that data is suitable for Go template rendering.
func ValidateTemplateData(data any) error {
	if data == nil {
		return nil
	}

	v := reflect.ValueOf(data)
	if v.Kind() == reflect.Map || v.Kind() == reflect.Struct || v.Kind() == reflect.Ptr ||
		v.Kind() == reflect.Slice || v.Kind() == reflect.Array ||
		v.Kind() == reflect.String || v.Kind() == reflect.Int || v.Kind() == reflect.Int8 ||
		v.Kind() == reflect.Int16 || v.Kind() == reflect.Int32 || v.Kind() == reflect.Int64 ||
		v.Kind() == reflect.Uint || v.Kind() == reflect.Uint8 || v.Kind() == reflect.Uint16 ||
		v.Kind() == reflect.Uint32 || v.Kind() == reflect.Uint64 ||
		v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64 || v.Kind() == reflect.Bool {
		return nil
	}
	return fmt.Errorf("unsupported template data type: %T", data)
}

// MapToStringMap converts a map[string]any to map[string]string.
func MapToStringMap(m map[string]any) (map[string]string, error) {
	result := make(map[string]string, len(m))
	for key, value := range m {
		strVal := toStringValue(value)
		result[key] = strVal
	}
	return result, nil
}

// toStringValue converts a value to its string representation.
func toStringValue(v any) string {
	if v == nil {
		return ""
	}

	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(val)
	case int8:
		return strconv.Itoa(int(val))
	case int16:
		return strconv.Itoa(int(val))
	case int32:
		return strconv.Itoa(int(val))
	case int64:
		return strconv.FormatInt(val, 10)
	case uint:
		return strconv.FormatUint(uint64(val), 10)
	case uint8:
		return strconv.FormatUint(uint64(val), 10)
	case uint16:
		return strconv.FormatUint(uint64(val), 10)
	case uint32:
		return strconv.FormatUint(uint64(val), 10)
	case uint64:
		return strconv.FormatUint(val, 10)
	case float32:
		return fmt.Sprintf("%g", val)
	case float64:
		return fmt.Sprintf("%g", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
