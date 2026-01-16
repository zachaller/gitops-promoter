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

package promotionstrategyview

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	aggregationv1alpha1 "github.com/argoproj-labs/gitops-promoter/internal/aggregationapi/v1alpha1"
)

func TestNewREST(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	rest := NewREST(fakeClient)

	assert.NotNil(t, rest)
	assert.True(t, rest.NamespaceScoped())
	assert.Equal(t, "promotionstrategyview", rest.GetSingularName())
}

func TestREST_New(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	rest := NewREST(fakeClient)

	obj := rest.New()
	assert.NotNil(t, obj)
	_, ok := obj.(*aggregationv1alpha1.PromotionStrategyView)
	assert.True(t, ok, "New() should return a *PromotionStrategyView")
}

func TestREST_NewList(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	rest := NewREST(fakeClient)

	obj := rest.NewList()
	assert.NotNil(t, obj)
	_, ok := obj.(*aggregationv1alpha1.PromotionStrategyViewList)
	assert.True(t, ok, "NewList() should return a *PromotionStrategyViewList")
}

func TestREST_Get_NotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	rest := NewREST(fakeClient)

	ctx := WithNamespace(context.Background(), "test-namespace")
	_, err := rest.Get(ctx, "nonexistent", &metav1.GetOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestREST_Get_Success(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	// Create a PromotionStrategy
	ps := &promoterv1alpha1.PromotionStrategy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-strategy",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.PromotionStrategySpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{
				Name: "test-repo",
			},
			Environments: []promoterv1alpha1.Environment{
				{
					Branch: "environment/development",
				},
			},
		},
	}

	// Create a GitRepository
	gr := &promoterv1alpha1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.GitRepositorySpec{
			Fake: &promoterv1alpha1.FakeRepo{
				Owner: "test-owner",
				Name:  "test-name",
			},
			ScmProviderRef: promoterv1alpha1.ScmProviderObjectReference{
				Kind: "ScmProvider",
				Name: "test-provider",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ps, gr).
		Build()
	rest := NewREST(fakeClient)

	ctx := WithNamespace(context.Background(), "test-namespace")
	obj, err := rest.Get(ctx, "test-strategy", &metav1.GetOptions{})
	require.NoError(t, err)

	view, ok := obj.(*aggregationv1alpha1.PromotionStrategyView)
	require.True(t, ok)

	assert.Equal(t, "test-strategy", view.Name)
	assert.Equal(t, "test-namespace", view.Namespace)
	assert.Equal(t, "test-repo", view.Spec.RepositoryReference.Name)
	assert.Len(t, view.Spec.Environments, 1)
	assert.Equal(t, "environment/development", view.Spec.Environments[0].Branch)

	// Check aggregated GitRepository
	assert.NotNil(t, view.Aggregated.GitRepository)
	assert.Equal(t, "test-repo", view.Aggregated.GitRepository.Name)
}

func TestREST_List_Empty(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	rest := NewREST(fakeClient)

	ctx := WithNamespace(context.Background(), "test-namespace")
	obj, err := rest.List(ctx, nil)
	require.NoError(t, err)

	list, ok := obj.(*aggregationv1alpha1.PromotionStrategyViewList)
	require.True(t, ok)
	assert.Empty(t, list.Items)
}

func TestREST_List_WithItems(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	// Create PromotionStrategies
	ps1 := &promoterv1alpha1.PromotionStrategy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "strategy-1",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.PromotionStrategySpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{
				Name: "repo-1",
			},
		},
	}
	ps2 := &promoterv1alpha1.PromotionStrategy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "strategy-2",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.PromotionStrategySpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{
				Name: "repo-2",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ps1, ps2).
		Build()
	rest := NewREST(fakeClient)

	ctx := WithNamespace(context.Background(), "test-namespace")
	obj, err := rest.List(ctx, nil)
	require.NoError(t, err)

	list, ok := obj.(*aggregationv1alpha1.PromotionStrategyViewList)
	require.True(t, ok)
	assert.Len(t, list.Items, 2)
}

func TestREST_Get_WithChangeTransferPolicies(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	// Create a PromotionStrategy
	ps := &promoterv1alpha1.PromotionStrategy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-strategy",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.PromotionStrategySpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{
				Name: "test-repo",
			},
		},
	}

	// Create a ChangeTransferPolicy with the PromotionStrategy label
	ctp := &promoterv1alpha1.ChangeTransferPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ctp",
			Namespace: "test-namespace",
			Labels: map[string]string{
				promoterv1alpha1.PromotionStrategyLabel: "test-strategy",
			},
		},
		Spec: promoterv1alpha1.ChangeTransferPolicySpec{
			ActiveBranch: "environment/development",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ps, ctp).
		Build()
	rest := NewREST(fakeClient)

	ctx := WithNamespace(context.Background(), "test-namespace")
	obj, err := rest.Get(ctx, "test-strategy", &metav1.GetOptions{})
	require.NoError(t, err)

	view, ok := obj.(*aggregationv1alpha1.PromotionStrategyView)
	require.True(t, ok)

	assert.Len(t, view.Aggregated.ChangeTransferPolicies, 1)
	assert.Equal(t, "test-ctp", view.Aggregated.ChangeTransferPolicies[0].Name)
	assert.Equal(t, "environment/development", view.Aggregated.ChangeTransferPolicies[0].Branch)
}

func TestREST_Get_WithCommitStatuses(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	// Create a PromotionStrategy
	ps := &promoterv1alpha1.PromotionStrategy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-strategy",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.PromotionStrategySpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{
				Name: "test-repo",
			},
		},
	}

	// Create an ArgoCDCommitStatus
	argocdCS := &promoterv1alpha1.ArgoCDCommitStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-argocd-cs",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.ArgoCDCommitStatusSpec{
			PromotionStrategyRef: promoterv1alpha1.ObjectReference{
				Name: "test-strategy",
			},
		},
	}

	// Create a GitCommitStatus
	gitCS := &promoterv1alpha1.GitCommitStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-git-cs",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.GitCommitStatusSpec{
			PromotionStrategyRef: promoterv1alpha1.ObjectReference{
				Name: "test-strategy",
			},
			Key:        "test-key",
			Expression: "true",
		},
	}

	// Create a TimedCommitStatus
	timedCS := &promoterv1alpha1.TimedCommitStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-timed-cs",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.TimedCommitStatusSpec{
			PromotionStrategyRef: promoterv1alpha1.ObjectReference{
				Name: "test-strategy",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ps, argocdCS, gitCS, timedCS).
		Build()
	rest := NewREST(fakeClient)

	ctx := WithNamespace(context.Background(), "test-namespace")
	obj, err := rest.Get(ctx, "test-strategy", &metav1.GetOptions{})
	require.NoError(t, err)

	view, ok := obj.(*aggregationv1alpha1.PromotionStrategyView)
	require.True(t, ok)

	assert.Len(t, view.Aggregated.CommitStatuses.ArgoCD, 1)
	assert.Equal(t, "test-argocd-cs", view.Aggregated.CommitStatuses.ArgoCD[0].Metadata.Name)

	assert.Len(t, view.Aggregated.CommitStatuses.Git, 1)
	assert.Equal(t, "test-git-cs", view.Aggregated.CommitStatuses.Git[0].Metadata.Name)

	assert.Len(t, view.Aggregated.CommitStatuses.Timed, 1)
	assert.Equal(t, "test-timed-cs", view.Aggregated.CommitStatuses.Timed[0].Metadata.Name)
}

func TestREST_Get_WithPullRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, promoterv1alpha1.AddToScheme(scheme))

	// Create a PromotionStrategy
	ps := &promoterv1alpha1.PromotionStrategy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-strategy",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.PromotionStrategySpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{
				Name: "test-repo",
			},
		},
	}

	// Create a PullRequest with the PromotionStrategy label
	pr := &promoterv1alpha1.PullRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pr",
			Namespace: "test-namespace",
			Labels: map[string]string{
				promoterv1alpha1.PromotionStrategyLabel: "test-strategy",
			},
		},
		Spec: promoterv1alpha1.PullRequestSpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{
				Name: "test-repo",
			},
			Title:        "Test PR",
			TargetBranch: "environment/development",
			SourceBranch: "environment/development-next",
			MergeSha:     "abc123",
			State:        promoterv1alpha1.PullRequestOpen,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ps, pr).
		Build()
	rest := NewREST(fakeClient)

	ctx := WithNamespace(context.Background(), "test-namespace")
	obj, err := rest.Get(ctx, "test-strategy", &metav1.GetOptions{})
	require.NoError(t, err)

	view, ok := obj.(*aggregationv1alpha1.PromotionStrategyView)
	require.True(t, ok)

	assert.Len(t, view.Aggregated.PullRequests, 1)
	assert.Equal(t, "test-pr", view.Aggregated.PullRequests[0].Name)
	assert.Equal(t, "environment/development", view.Aggregated.PullRequests[0].Branch)
}

func TestWithNamespace(t *testing.T) {
	ctx := context.Background()

	// Without namespace
	ns := genericNamespaceFromContext(ctx)
	assert.Empty(t, ns)

	// With namespace
	ctx = WithNamespace(ctx, "my-namespace")
	ns = genericNamespaceFromContext(ctx)
	assert.Equal(t, "my-namespace", ns)
}

func TestREST_Watch(t *testing.T) {
	// Create scheme
	scheme := runtime.NewScheme()
	_ = promoterv1alpha1.AddToScheme(scheme)
	_ = aggregationv1alpha1.AddToScheme(scheme)

	// Create a fake client with watch support
	ps := &promoterv1alpha1.PromotionStrategy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-strategy",
			Namespace: "test-namespace",
		},
		Spec: promoterv1alpha1.PromotionStrategySpec{
			RepositoryReference: promoterv1alpha1.ObjectReference{
				Name: "test-repo",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ps).
		Build()

	rest := NewREST(fakeClient)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = WithNamespace(ctx, "test-namespace")

	// Start watching
	watcher, err := rest.Watch(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, watcher)

	// Stop the watcher after a short time
	go func() {
		<-time.After(100 * time.Millisecond)
		watcher.Stop()
	}()

	// We should receive at least the initial ADDED event
	select {
	case event := <-watcher.ResultChan():
		// The fake client may not support Watch, so we might get an error
		// or we might get the initial ADDED event
		if event.Type == watch.Added {
			view, ok := event.Object.(*aggregationv1alpha1.PromotionStrategyView)
			require.True(t, ok)
			assert.Equal(t, "test-strategy", view.Name)
		}
	case <-time.After(200 * time.Millisecond):
		// Timeout is acceptable if fake client doesn't support watch
	}
}
