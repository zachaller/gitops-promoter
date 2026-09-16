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

package apiserver

import (
	"bytes"

	corev1 "k8s.io/api/core/v1"

	promotercache "github.com/argoproj-labs/gitops-promoter/internal/cache"
)

// secretCredentialDataEqual reports whether promoter credential keys in two Secrets match.
func secretCredentialDataEqual(a, b *corev1.Secret) bool {
	if a == nil || b == nil {
		return a == b
	}
	allowed := promotercache.PromoterSecretDataKeySet()
	ak := filteredSecretData(a.Data, allowed)
	bk := filteredSecretData(b.Data, allowed)
	if len(ak) != len(bk) {
		return false
	}
	for k, av := range ak {
		bv, ok := bk[k]
		if !ok || !bytes.Equal(av, bv) {
			return false
		}
	}
	return true
}

func filteredSecretData(data map[string][]byte, allowed map[string]struct{}) map[string][]byte {
	if len(data) == 0 {
		return nil
	}
	out := make(map[string][]byte)
	for k, v := range data {
		if _, keep := allowed[k]; keep {
			out[k] = v
		}
	}
	return out
}
