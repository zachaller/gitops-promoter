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
	"context"

	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/argoproj-labs/gitops-promoter/internal/aggregationapi/registry/promotionstrategyview"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
)

// PromotionStrategyViewStorage wraps the REST storage to properly handle namespace from request context.
type PromotionStrategyViewStorage struct {
	rest *promotionstrategyview.REST
}

var (
	_ rest.Getter               = &PromotionStrategyViewStorage{}
	_ rest.Lister               = &PromotionStrategyViewStorage{}
	_ rest.Scoper               = &PromotionStrategyViewStorage{}
	_ rest.SingularNameProvider = &PromotionStrategyViewStorage{}
	_ rest.TableConvertor       = &PromotionStrategyViewStorage{}
)

// NewPromotionStrategyViewStorage creates a new storage for PromotionStrategyView.
func NewPromotionStrategyViewStorage(c client.Client) *PromotionStrategyViewStorage {
	return &PromotionStrategyViewStorage{
		rest: promotionstrategyview.NewREST(c),
	}
}

// New returns a new instance of PromotionStrategyView.
func (s *PromotionStrategyViewStorage) New() runtime.Object {
	return s.rest.New()
}

// Destroy cleans up resources on shutdown.
func (s *PromotionStrategyViewStorage) Destroy() {
	s.rest.Destroy()
}

// NewList returns a new list of PromotionStrategyView.
func (s *PromotionStrategyViewStorage) NewList() runtime.Object {
	return s.rest.NewList()
}

// NamespaceScoped returns true because PromotionStrategyView is namespace-scoped.
func (s *PromotionStrategyViewStorage) NamespaceScoped() bool {
	return s.rest.NamespaceScoped()
}

// GetSingularName returns the singular name of the resource.
func (s *PromotionStrategyViewStorage) GetSingularName() string {
	return s.rest.GetSingularName()
}

// Get retrieves a PromotionStrategyView by name.
func (s *PromotionStrategyViewStorage) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	// Extract namespace from the request context and inject it into our context
	ctx = s.injectNamespace(ctx)
	return s.rest.Get(ctx, name, options)
}

// List retrieves all PromotionStrategyViews in a namespace.
func (s *PromotionStrategyViewStorage) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	// Extract namespace from the request context and inject it into our context
	ctx = s.injectNamespace(ctx)
	return s.rest.List(ctx, options)
}

// ConvertToTable converts the object to a table for kubectl output.
func (s *PromotionStrategyViewStorage) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return s.rest.ConvertToTable(ctx, object, tableOptions)
}

// injectNamespace extracts the namespace from the apiserver request context
// and injects it into our custom context key.
func (s *PromotionStrategyViewStorage) injectNamespace(ctx context.Context) context.Context {
	if requestInfo, ok := genericapirequest.RequestInfoFrom(ctx); ok {
		return promotionstrategyview.WithNamespace(ctx, requestInfo.Namespace)
	}
	return ctx
}

// GetResetFields returns the set of fields that get reset by the strategy
// and should not be modified by the user.
func (s *PromotionStrategyViewStorage) GetResetFields() map[string]interface{} {
	return nil
}

// Categories returns the list of categories this resource belongs to.
func (s *PromotionStrategyViewStorage) Categories() []string {
	return []string{"promoter"}
}

// ShortNames returns the list of short names for this resource.
func (s *PromotionStrategyViewStorage) ShortNames() []string {
	return []string{"psv"}
}

// Kind returns the kind of the resource.
func (s *PromotionStrategyViewStorage) Kind() string {
	return "PromotionStrategyView"
}
