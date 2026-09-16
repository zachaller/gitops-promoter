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
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	viewv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/view/v1alpha1"
)

// HistoryREST is read-only rest.Storage for PromotionStrategyHistory.
type HistoryREST struct {
	provider       *HistoryProvider
	tableConvertor rest.TableConvertor
}

var (
	_ rest.Storage              = &HistoryREST{}
	_ rest.Getter               = &HistoryREST{}
	_ rest.Lister               = &HistoryREST{}
	_ rest.Watcher              = &HistoryREST{}
	_ rest.Scoper               = &HistoryREST{}
	_ rest.SingularNameProvider = &HistoryREST{}
	_ rest.TableConvertor       = &HistoryREST{}
)

// NewHistoryREST creates REST storage for PromotionStrategyHistory.
func NewHistoryREST(provider *HistoryProvider) *HistoryREST {
	return &HistoryREST{
		provider:       provider,
		tableConvertor: rest.NewDefaultTableConvertor(viewv1alpha1.Resource("promotionstrategyhistories")),
	}
}

func (r *HistoryREST) New() runtime.Object {
	return &viewv1alpha1.PromotionStrategyHistory{}
}

func (r *HistoryREST) Destroy() {}

func (r *HistoryREST) NewList() runtime.Object {
	return &viewv1alpha1.PromotionStrategyHistoryList{}
}

func (r *HistoryREST) NamespaceScoped() bool {
	return true
}

func (r *HistoryREST) GetSingularName() string {
	return "promotionstrategyhistory"
}

func (r *HistoryREST) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	namespace := genericapirequest.NamespaceValue(ctx)
	return r.provider.Get(ctx, namespace, name)
}

func (r *HistoryREST) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	namespace := genericapirequest.NamespaceValue(ctx)
	name, err := historyNameFromFieldSelector(options)
	if err != nil {
		return nil, err
	}
	return r.provider.List(ctx, namespace, name, labelSelectorFromOptions(options))
}

func (r *HistoryREST) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	namespace := genericapirequest.NamespaceValue(ctx)
	sendInitialEvents := options != nil && options.SendInitialEvents != nil && *options.SendInitialEvents
	allowWatchBookmarks := options != nil && options.AllowWatchBookmarks
	sendInitial := sendInitialEvents || options == nil || options.ResourceVersion == "" || options.ResourceVersion == "0"
	name, err := historyNameFromFieldSelector(options)
	if err != nil {
		return nil, err
	}
	return r.provider.Watch(ctx, namespace, name, labelSelectorFromOptions(options), sendInitial, sendInitialEvents && allowWatchBookmarks)
}

func historyNameFromFieldSelector(options *metainternalversion.ListOptions) (string, error) {
	name, err := nameFromFieldSelector(options)
	if err != nil {
		return "", err
	}
	return name, nil
}

func (r *HistoryREST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return r.tableConvertor.ConvertToTable(ctx, object, tableOptions)
}
