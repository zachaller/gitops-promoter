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
	"context"
	"fmt"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// secretRefPattern matches ${secret:secret-name:key} references
var secretRefPattern = regexp.MustCompile(`\$\{secret:([^:}]+):([^}]+)\}`)

// SecretResolver handles resolution of secret references.
type SecretResolver struct {
	Client    client.Client
	Namespace string
}

// NewSecretResolver creates a new SecretResolver.
func NewSecretResolver(c client.Client, namespace string) *SecretResolver {
	return &SecretResolver{
		Client:    c,
		Namespace: namespace,
	}
}

// ResolveString resolves ${secret:name:key} references in a string.
func (r *SecretResolver) ResolveString(ctx context.Context, input string) (string, error) {
	matches := secretRefPattern.FindAllStringSubmatchIndex(input, -1)
	if len(matches) == 0 {
		return input, nil
	}

	result := input
	// Process matches in reverse order to preserve indices
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		fullMatch := input[match[0]:match[1]]
		secretName := input[match[2]:match[3]]
		key := input[match[4]:match[5]]

		value, err := r.GetSecretValue(ctx, secretName, key)
		if err != nil {
			return "", fmt.Errorf("failed to resolve secret reference %q: %w", fullMatch, err)
		}

		result = result[:match[0]] + value + result[match[1]:]
	}

	return result, nil
}

// ResolveMap resolves secret references in all map values.
func (r *SecretResolver) ResolveMap(ctx context.Context, input map[string]string) (map[string]string, error) {
	if input == nil {
		return nil, nil
	}

	result := make(map[string]string, len(input))
	for key, value := range input {
		resolved, err := r.ResolveString(ctx, value)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve secret in key %q: %w", key, err)
		}
		result[key] = resolved
	}
	return result, nil
}

// GetSecretValue retrieves a specific key from a secret.
func (r *SecretResolver) GetSecretValue(ctx context.Context, secretName, key string) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: secretName}, secret); err != nil {
		return "", fmt.Errorf("failed to get secret %q: %w", secretName, err)
	}

	data, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %q", key, secretName)
	}

	return string(data), nil
}

// HasSecretRefs checks if a string contains any secret references.
func HasSecretRefs(input string) bool {
	return secretRefPattern.MatchString(input)
}

// ExtractSecretRefs extracts all secret references from a string.
// Returns a slice of [secretName, key] pairs.
func ExtractSecretRefs(input string) [][2]string {
	matches := secretRefPattern.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return nil
	}

	refs := make([][2]string, len(matches))
	for i, match := range matches {
		refs[i] = [2]string{match[1], match[2]}
	}
	return refs
}
