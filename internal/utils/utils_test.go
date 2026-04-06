package utils_test

import (
	"context"
	"errors"
	"time"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	acv1alpha1 "github.com/argoproj-labs/gitops-promoter/applyconfiguration/api/v1alpha1"
	"github.com/argoproj-labs/gitops-promoter/internal/types/conditions"
	"github.com/argoproj-labs/gitops-promoter/internal/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("test rendering a template", func() {
	tests := map[string]struct {
		testdata []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase
		result   bool
	}{
		"all success": {
			testdata: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				{Key: "test1", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
				{Key: "test1", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
				{Key: "test1", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
			},
			result: true,
		},
		"one pending": {
			testdata: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				{Key: "test1", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
				{Key: "test1", Phase: string(promoterv1alpha1.CommitPhasePending)},
				{Key: "test1", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
			},
			result: false,
		},
		"one failure": {
			testdata: []promoterv1alpha1.ChangeRequestPolicyCommitStatusPhase{
				{Key: "test1", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
				{Key: "test1", Phase: string(promoterv1alpha1.CommitPhaseFailure)},
				{Key: "test1", Phase: string(promoterv1alpha1.CommitPhaseSuccess)},
			},
			result: false,
		},
	}

	for name, test := range tests {
		It(name, func() {
			result := utils.AreCommitStatusesPassing(test.testdata)
			Expect(result).To(Equal(test.result))
		})
	}
})

var _ = Describe("InheritNotReadyConditionFromObjects", func() {
	var (
		parent    *promoterv1alpha1.PromotionStrategy
		child1    *promoterv1alpha1.CommitStatus
		child2    *promoterv1alpha1.CommitStatus
		childObjs []*promoterv1alpha1.CommitStatus
	)

	BeforeEach(func() {
		parent = &promoterv1alpha1.PromotionStrategy{
			TypeMeta:   metav1.TypeMeta{Kind: "PromotionStrategy"},
			ObjectMeta: metav1.ObjectMeta{Name: "parent", Generation: 1},
		}
		child1 = &promoterv1alpha1.CommitStatus{
			TypeMeta:   metav1.TypeMeta{Kind: "CommitStatus"},
			ObjectMeta: metav1.ObjectMeta{Name: "child1", Generation: 1},
		}
		child2 = &promoterv1alpha1.CommitStatus{
			TypeMeta:   metav1.TypeMeta{Kind: "CommitStatus"},
			ObjectMeta: metav1.ObjectMeta{Name: "child2", Generation: 1},
		}
		childObjs = []*promoterv1alpha1.CommitStatus{child2, child1} // Intentionally out of order to test sorting
	})

	It("should not modify parent Ready condition if all children are Ready", func() {
		meta.SetStatusCondition(child1.GetConditions(), metav1.Condition{
			Type:               string(conditions.Ready),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
		})
		meta.SetStatusCondition(child2.GetConditions(), metav1.Condition{
			Type:               string(conditions.Ready),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
		})
		meta.SetStatusCondition(parent.GetConditions(), metav1.Condition{
			Type:   string(conditions.Ready),
			Status: metav1.ConditionFalse,
		})

		utils.InheritNotReadyConditionFromObjects(parent, conditions.CommitStatusesNotReady, childObjs...)

		Expect(meta.FindStatusCondition(*parent.GetConditions(), string(conditions.Ready)).Status).To(Equal(metav1.ConditionFalse))
	})

	It("should set parent Ready condition to False if any child is not Ready", func() {
		meta.SetStatusCondition(child1.GetConditions(), metav1.Condition{
			Type:               string(conditions.Ready),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
		})
		meta.SetStatusCondition(child2.GetConditions(), metav1.Condition{
			Type:               string(conditions.Ready),
			Status:             metav1.ConditionFalse,
			Reason:             "NotReady",
			Message:            "Child is not ready",
			ObservedGeneration: 1,
		})

		utils.InheritNotReadyConditionFromObjects(parent, conditions.CommitStatusesNotReady, childObjs...)

		readyCondition := meta.FindStatusCondition(*parent.GetConditions(), string(conditions.Ready))
		Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCondition.Message).To(Equal(`CommitStatus "child2" is not Ready because "NotReady": Child is not ready`))
	})

	It("should set parent Ready condition to False if a child Ready condition is missing", func() {
		meta.SetStatusCondition(child1.GetConditions(), metav1.Condition{
			Type:               string(conditions.Ready),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
		})
		// child2 has no Ready condition

		utils.InheritNotReadyConditionFromObjects(parent, conditions.CommitStatusesNotReady, childObjs...)

		readyCondition := meta.FindStatusCondition(*parent.GetConditions(), string(conditions.Ready))
		Expect(readyCondition.Status).To(Equal(metav1.ConditionUnknown))
		Expect(readyCondition.Message).To(Equal(`CommitStatus "child2" Ready condition is missing`))
	})

	It("should set parent Ready condition to False if a child Ready condition is outdated", func() {
		meta.SetStatusCondition(child1.GetConditions(), metav1.Condition{
			Type:               string(conditions.Ready),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
		})
		meta.SetStatusCondition(child2.GetConditions(), metav1.Condition{
			Type:               string(conditions.Ready),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 0, // Simulate outdated condition
		})

		utils.InheritNotReadyConditionFromObjects(parent, conditions.CommitStatusesNotReady, childObjs...)

		readyCondition := meta.FindStatusCondition(*parent.GetConditions(), string(conditions.Ready))
		Expect(readyCondition.Status).To(Equal(metav1.ConditionUnknown))
		Expect(readyCondition.Message).To(Equal(`CommitStatus "child2" Ready condition is not up to date`))
	})

	It("should always take the first not ready condition, ordered alphabetically by child name", func() {
		meta.SetStatusCondition(child1.GetConditions(), metav1.Condition{
			Type:               string(conditions.Ready),
			Status:             metav1.ConditionFalse,
			Reason:             "NotReady1",
			Message:            "Child1 is not ready",
			ObservedGeneration: 1,
		})
		meta.SetStatusCondition(child2.GetConditions(), metav1.Condition{
			Type:               string(conditions.Ready),
			Status:             metav1.ConditionFalse,
			Reason:             "NotReady2",
			Message:            "Child2 is not ready",
			ObservedGeneration: 1,
		})

		utils.InheritNotReadyConditionFromObjects(parent, conditions.CommitStatusesNotReady, childObjs...)

		readyCondition := meta.FindStatusCondition(*parent.GetConditions(), string(conditions.Ready))
		Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCondition.Message).To(Equal(`CommitStatus "child1" is not Ready because "NotReady1": Child1 is not ready`))
	})
})

var _ = Describe("HandleReconciliationResult panic recovery", func() {
	var (
		ctx      context.Context
		obj      *promoterv1alpha1.PromotionStrategy
		recorder events.EventRecorder
		scheme   *runtime.Scheme
	)

	BeforeEach(func() {
		ctx = context.Background()
		obj = &promoterv1alpha1.PromotionStrategy{
			TypeMeta: metav1.TypeMeta{
				Kind:       "PromotionStrategy",
				APIVersion: "promoter.argoproj.io/v1alpha1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-strategy",
				Namespace:  "default",
				Generation: 1,
			},
		}
		scheme = runtime.NewScheme()
		_ = promoterv1alpha1.AddToScheme(scheme)
		recorder = events.NewFakeRecorder(10)
	})

	It("should recover from panic and convert it to an error", func() {
		var err error
		// We use fakeclient here since it's virtually impossible to trigger a panic otherwise. Don't spread
		// this use to other tests if at all avoidable.
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).Build()

		func() {
			defer utils.HandleReconciliationResult(ctx, metav1.Now().Time, obj, fakeClient, recorder, nil, &err,
				"test-field-owner", buildPSApplyConfig(obj))
			panic("test panic message")
		}()

		// The panic should have been caught and converted to an error
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("panic in reconciliation"))
		Expect(err.Error()).To(ContainSubstring("test panic message"))
	})

	It("should handle normal errors without panicking", func() {
		var err error
		// We use fakeclient here since it's virtually impossible to trigger a panic otherwise. Don't spread
		// this use to other tests if at all avoidable.
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).Build()

		// Create the object in the fake client so HandleReconciliationResult can update it
		Expect(fakeClient.Create(ctx, obj)).To(Succeed())

		func() {
			defer utils.HandleReconciliationResult(ctx, metav1.Now().Time, obj, fakeClient, recorder, nil, &err,
				"test-field-owner", buildPSApplyConfig(obj))
			err = errors.New("test error message")
		}()

		// The error should be preserved
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("test error message"))
	})

	It("should handle successful reconciliation without error", func() {
		var err error
		// We use fakeclient here since it's virtually impossible to trigger a panic otherwise. Don't spread
		// this use to other tests if at all avoidable.
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).Build()

		// Create the object in the fake client so HandleReconciliationResult can update it
		Expect(fakeClient.Create(ctx, obj)).To(Succeed())

		func() {
			defer utils.HandleReconciliationResult(ctx, metav1.Now().Time, obj, fakeClient, recorder, nil, &err,
				"test-field-owner", buildPSApplyConfig(obj))
			// No error or panic
		}()

		// No error should be set
		Expect(err).ToNot(HaveOccurred())
	})

	It("should clear result when panic occurs with a non-nil result", func() {
		var err error
		result := reconcile.Result{Requeue: true, RequeueAfter: 5 * time.Second}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).Build()

		func() {
			defer utils.HandleReconciliationResult(ctx, metav1.Now().Time, obj, fakeClient, recorder, &result, &err,
				"test-field-owner", buildPSApplyConfig(obj))
			panic("test panic message")
		}()

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("panic in reconciliation"))
		// result must be zeroed so the caller doesn't return both a requeue and an error
		Expect(result).To(Equal(reconcile.Result{}))
	})

	It("should clear result when status patch fails", func() {
		var err error
		result := reconcile.Result{Requeue: true, RequeueAfter: 5 * time.Second}
		// Use interceptor to force all status patches to fail.
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(obj).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					return errors.New("simulated status patch failure")
				},
			}).
			Build()

		func() {
			defer utils.HandleReconciliationResult(ctx, metav1.Now().Time, obj, fakeClient, recorder, &result, &err,
				"test-field-owner", buildPSApplyConfig(obj))
			// No error or panic — HandleReconciliationResult will try (and fail) to patch status.
		}()

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to patch status"))
		// result must be zeroed so the caller doesn't return both a requeue and an error
		Expect(result).To(Equal(reconcile.Result{}))
	})

	It("should preserve result when reconciliation succeeds and status patch succeeds", func() {
		var err error
		result := reconcile.Result{RequeueAfter: 5 * time.Second}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).Build()
		Expect(fakeClient.Create(ctx, obj)).To(Succeed())

		func() {
			defer utils.HandleReconciliationResult(ctx, metav1.Now().Time, obj, fakeClient, recorder, &result, &err,
				"test-field-owner", buildPSApplyConfig(obj))
			// No error or panic
		}()

		Expect(err).ToNot(HaveOccurred())
		// result should be untouched — HandleReconciliationResult only clears it on error
		Expect(result).To(Equal(reconcile.Result{RequeueAfter: 5 * time.Second}))
	})
})

var _ = Describe("HandleReconciliationResult fallback status update", func() {
	var (
		ctx      context.Context
		obj      *promoterv1alpha1.ArgoCDCommitStatus
		recorder events.EventRecorder
		scheme   *runtime.Scheme
	)

	BeforeEach(func() {
		ctx = context.Background()
		obj = &promoterv1alpha1.ArgoCDCommitStatus{
			TypeMeta: metav1.TypeMeta{
				Kind:       "ArgoCDCommitStatus",
				APIVersion: "promoter.argoproj.io/v1alpha1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-commit-status",
				Namespace:  "default",
				Generation: 1,
			},
		}
		scheme = runtime.NewScheme()
		_ = promoterv1alpha1.AddToScheme(scheme)
		recorder = events.NewFakeRecorder(10)
	})

	It("should use fallback when full status patch fails", func() {
		var err error
		patchCallCount := 0

		// Create a fake client with an interceptor that fails the first status patch
		// but allows the second (fallback) patch to succeed
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(obj).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					patchCallCount++
					if patchCallCount == 1 {
						// First patch (full status) fails with a validation error
						return apierrors.NewInvalid(
							schema.GroupKind{Group: "promoter.argoproj.io", Kind: "ArgoCDCommitStatus"},
							obj.GetName(),
							nil,
						)
					}
					// Second patch (fallback with only condition) succeeds
					return client.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
				},
			}).
			Build()

		// Create the object in the fake client
		Expect(fakeClient.Create(ctx, obj)).To(Succeed())

		// Simulate a successful reconciliation followed by a status patch failure
		result := reconcile.Result{}
		func() {
			defer utils.HandleReconciliationResult(ctx, metav1.Now().Time, obj, fakeClient, recorder, &result, &err,
				"test-field-owner", buildACSApplyConfig(obj))
			// No reconciliation error - reconciliation succeeded
		}()

		// The error should indicate that the full status patch failed but fallback succeeded
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to patch full status"))
		Expect(err.Error()).To(ContainSubstring("patching only the Ready condition succeeded"))

		// Verify that we attempted two patches: full status, then fallback
		Expect(patchCallCount).To(Equal(2))

		// Verify the Ready condition was set by fetching a fresh copy
		updated := &promoterv1alpha1.ArgoCDCommitStatus{}
		Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(obj), updated)).To(Succeed())
		readyCondition := meta.FindStatusCondition(*updated.GetConditions(), string(conditions.Ready))
		Expect(readyCondition).ToNot(BeNil())
		Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
	})

	It("should report error when both full patch and fallback fail", func() {
		var err error

		// Create a fake client that fails all status patches
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(obj).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					return apierrors.NewInvalid(
						schema.GroupKind{Group: "promoter.argoproj.io", Kind: "ArgoCDCommitStatus"},
						obj.GetName(),
						nil,
					)
				},
			}).
			Build()

		// Create the object in the fake client
		Expect(fakeClient.Create(ctx, obj)).To(Succeed())

		// Simulate a successful reconciliation
		result := reconcile.Result{}
		func() {
			defer utils.HandleReconciliationResult(ctx, metav1.Now().Time, obj, fakeClient, recorder, &result, &err,
				"test-field-owner", buildACSApplyConfig(obj))
			// No reconciliation error
		}()

		// The error should indicate that both patches failed
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("patching only the Ready condition also failed"))
	})

	It("should include original reconciliation error in fallback condition", func() {
		var err error
		patchCallCount := 0

		// Create a fake client that fails the first patch but allows the fallback
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(obj).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					patchCallCount++
					if patchCallCount == 1 {
						return errors.New("simulated status patch failure")
					}
					// Fallback succeeds
					return client.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
				},
			}).
			Build()

		// Create the object in the fake client
		Expect(fakeClient.Create(ctx, obj)).To(Succeed())

		// Simulate a reconciliation that returns an error
		reconcileErr := errors.New("reconciliation failed for test")
		result := reconcile.Result{}
		func() {
			defer utils.HandleReconciliationResult(ctx, metav1.Now().Time, obj, fakeClient, recorder, &result, &err,
				"test-field-owner", buildACSApplyConfig(obj))
			err = reconcileErr
		}()

		// The error should mention both the reconciliation error and that patching only the condition succeeded
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("reconciliation failed for test"))
		Expect(err.Error()).To(ContainSubstring("patching only the Ready condition succeeded"))

		// Verify the condition includes the original reconciliation error
		updated := &promoterv1alpha1.ArgoCDCommitStatus{}
		Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(obj), updated)).To(Succeed())
		readyCondition := meta.FindStatusCondition(*updated.GetConditions(), string(conditions.Ready))
		Expect(readyCondition).ToNot(BeNil())
		Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCondition.Message).To(ContainSubstring("Reconciliation failed"))
		Expect(readyCondition.Message).To(ContainSubstring("reconciliation failed for test"))
	})
})

func buildPSApplyConfig(ps *promoterv1alpha1.PromotionStrategy) func() any {
	return func() any {
		return acv1alpha1.PromotionStrategy(ps.Name, ps.Namespace).
			WithStatus(acv1alpha1.PromotionStrategyStatus().
				WithConditions(utils.ConditionsToApplyConfig(ps.Status.Conditions)...))
	}
}

func buildACSApplyConfig(acs *promoterv1alpha1.ArgoCDCommitStatus) func() any {
	return func() any {
		return acv1alpha1.ArgoCDCommitStatus(acs.Name, acs.Namespace).
			WithStatus(acv1alpha1.ArgoCDCommitStatusStatus().
				WithConditions(utils.ConditionsToApplyConfig(acs.Status.Conditions)...))
	}
}
