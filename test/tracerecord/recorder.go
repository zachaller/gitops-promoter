// Package tracerecord records the changes the API server stores for the promotion resources in a
// namespace, so hack/formal/tracecheck can check the run against the Quint model in hack/formal.
package tracerecord

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// Event is one change: the object as stored after it, or as it was when it was deleted.
// Type is a watch event type (ADDED, MODIFIED, DELETED), or GAP when the watch lost history and
// had to start again from the current state.
type Event struct {
	Type   string          `json:"type"`
	Kind   string          `json:"kind"`
	Object json.RawMessage `json:"object,omitempty"`
}

// Recorder watches PromotionStrategies, ChangeTransferPolicies, PullRequests and CommitStatuses.
type Recorder struct {
	cancel context.CancelFunc
	events []Event
	errs   []error
	wg     sync.WaitGroup
	mu     sync.Mutex
}

var watchedLists = map[string]func() client.ObjectList{
	"PromotionStrategy":    func() client.ObjectList { return &promoterv1alpha1.PromotionStrategyList{} },
	"ChangeTransferPolicy": func() client.ObjectList { return &promoterv1alpha1.ChangeTransferPolicyList{} },
	"PullRequest":          func() client.ObjectList { return &promoterv1alpha1.PullRequestList{} },
	"CommitStatus":         func() client.ObjectList { return &promoterv1alpha1.CommitStatusList{} },
}

// Start begins watching namespace. Watches start from the current state, so start the recorder
// before creating the resources whose history should be checked.
func Start(cfg *rest.Config, namespace string) (*Recorder, error) {
	scheme := runtime.NewScheme()
	if err := promoterv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to build scheme: %w", err)
	}
	c, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create watch client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := &Recorder{cancel: cancel}
	for kind, newList := range watchedLists {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.watch(ctx, c, namespace, kind, newList)
		}()
	}
	return r, nil
}

// watch follows one kind, resuming from the last resourceVersion it saw when the server ends the
// watch, and starting over (with a GAP event) when that version is too old.
func (r *Recorder) watch(
	ctx context.Context, c client.WithWatch, namespace, kind string, newList func() client.ObjectList,
) {
	resourceVersion := ""
	for ctx.Err() == nil {
		w, err := c.Watch(ctx, newList(), &client.ListOptions{
			Namespace: namespace,
			Raw:       &metav1.ListOptions{ResourceVersion: resourceVersion, AllowWatchBookmarks: true},
		})
		if err != nil {
			if ctx.Err() == nil {
				r.fail(fmt.Errorf("failed to watch %s: %w", kind, err))
			}
			return
		}
		resourceVersion = r.drain(ctx, w, kind, resourceVersion)
	}
}

func (r *Recorder) drain(ctx context.Context, w watch.Interface, kind, resourceVersion string) string {
	defer w.Stop()
	for ev := range w.ResultChan() {
		switch ev.Type {
		case watch.Error:
			if ctx.Err() != nil {
				// Stop cancelled the watch.
				return resourceVersion
			}
			if status, ok := ev.Object.(*metav1.Status); ok && status.Code == 410 {
				r.add(Event{Type: "GAP", Kind: kind})
				return ""
			}
			r.fail(fmt.Errorf("watch of %s failed: %w", kind, apierrors.FromObject(ev.Object)))
			return resourceVersion
		case watch.Bookmark:
			if obj, ok := ev.Object.(client.Object); ok {
				resourceVersion = obj.GetResourceVersion()
			}
		case watch.Added, watch.Modified, watch.Deleted:
			obj, ok := ev.Object.(client.Object)
			if !ok {
				continue
			}
			resourceVersion = obj.GetResourceVersion()
			data, err := json.Marshal(obj)
			if err != nil {
				r.fail(fmt.Errorf("failed to encode %s: %w", kind, err))
				continue
			}
			r.add(Event{Type: string(ev.Type), Kind: kind, Object: data})
		default:
		}
	}
	return resourceVersion
}

func (r *Recorder) add(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *Recorder) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

// Stop ends the watches and reports any watch that failed.
func (r *Recorder) Stop() error {
	r.cancel()
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(r.errs...)
}

// WriteFile writes the events recorded so far as JSON lines, in the order they arrived. The order
// across kinds is restored from resourceVersions by hack/formal/tracecheck.
func (r *Recorder) WriteFile(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", path, err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, ev := range r.events {
		if err := enc.Encode(ev); err != nil {
			_ = f.Close()
			return fmt.Errorf("failed to write %s: %w", path, err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close %s: %w", path, err)
	}
	return nil
}
