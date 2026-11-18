package webserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	viewv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/view/v1alpha1"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	testNamespace = "team-a"
	eventTimeout  = 5 * time.Second
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// sseEvent is one server-sent event as the browser would see it.
type sseEvent struct {
	name string
	data string
}

// testBundle returns a bundle whose repository reference name doubles as a revision marker,
// so a test can tell which version of the object an event carries.
func testBundle(namespace, name, revision string) *viewv1alpha1.PromotionStrategyDetails {
	bundle := &viewv1alpha1.PromotionStrategyDetails{}
	bundle.Namespace = namespace
	bundle.Name = name
	bundle.PromotionStrategy.Spec.RepositoryReference.Name = revision
	return bundle
}

func revisionOf(t *testing.T, data string) (name, revision string) {
	t.Helper()
	bundle := &viewv1alpha1.PromotionStrategyDetails{}
	require.NoError(t, json.Unmarshal([]byte(data), bundle))
	return bundle.Name, bundle.PromotionStrategy.Spec.RepositoryReference.Name
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, viewv1alpha1.AddToScheme(scheme))
	return scheme
}

// newTestServer serves the dashboard API routes exactly as StartDashboard registers them
// (gzip included), backed by the given client.
func newTestServer(t *testing.T, c client.Client) (*WebServer, string) {
	t.Helper()
	ws := &WebServer{
		Client:            c,
		events:            newBroadcaster(),
		heartbeatInterval: defaultHeartbeatInterval,
	}
	router := gin.New()
	ws.registerAPIRoutes(router)
	srv := httptest.NewServer(router)
	// Close waits for in-flight requests, so this also proves that every /watch handler
	// returns once its client is gone (the streams are closed by an earlier cleanup).
	t.Cleanup(srv.Close)
	return ws, srv.URL
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(objs...).Build()
}

// openStream connects to /watch and returns the events it receives. The connection is
// closed when stop is called or the test ends.
func openStream(t *testing.T, baseURL, query string) (events <-chan sseEvent, header http.Header, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/watch?"+query, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // The stream stays open; stop closes it.
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	out := make(chan sseEvent, 4096)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
		var ev sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if ev.name != "" {
					out <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "event:"):
				ev.name = strings.TrimPrefix(line, "event:")
			case strings.HasPrefix(line, "data:"):
				ev.data += strings.TrimPrefix(line, "data:")
			default:
				// Not a field these tests use.
			}
		}
	}()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			_ = resp.Body.Close()
			<-done
		})
	}
	t.Cleanup(stop)
	return out, resp.Header, stop
}

// nextEvent returns the next event that is not a heartbeat.
func nextEvent(t *testing.T, events <-chan sseEvent) sseEvent {
	t.Helper()
	timeout := time.After(eventTimeout)
	for {
		select {
		case ev, ok := <-events:
			require.True(t, ok, "the stream ended unexpectedly")
			if ev.name != eventPing {
				return ev
			}
		case <-timeout:
			require.FailNow(t, "timed out waiting for an event")
		}
	}
}

func snapshotNames(t *testing.T, ev sseEvent) map[string]string {
	t.Helper()
	require.Equal(t, eventSnapshot, ev.name)
	var bundles []viewv1alpha1.PromotionStrategyDetails
	require.NoError(t, json.Unmarshal([]byte(ev.data), &bundles))
	out := make(map[string]string, len(bundles))
	for i := range bundles {
		assert.Equal(t, promotionStrategyDetailsKind, bundles[i].Kind)
		out[bundles[i].Name] = bundles[i].PromotionStrategy.Spec.RepositoryReference.Name
	}
	return out
}

// testWatchQuery selects the bundles of testNamespace, as the dashboard does for the
// namespace it is showing.
const testWatchQuery = "kind=" + promotionStrategyDetailsKind + "&namespace=" + testNamespace

func TestWatchStartsWithASnapshotOnAnIdleStream(t *testing.T) {
	t.Parallel()
	_, url := newTestServer(t, newFakeClient(t,
		testBundle(testNamespace, "app-a", "rev-1"),
		testBundle(testNamespace, "app-b", "rev-1"),
		testBundle("team-b", "app-c", "rev-1"),
	))

	// Nothing is ever published in this test. The response headers and the current state
	// must arrive anyway: until the headers do, a browser's EventSource is not open, and a
	// proxy waiting on them times the request out.
	events, header, _ := openStream(t, url, testWatchQuery)

	assert.Equal(t, "text/event-stream", header.Get("Content-Type"))
	assert.Equal(t, "no", header.Get("X-Accel-Buffering"))
	assert.Equal(t,
		map[string]string{"app-a": "rev-1", "app-b": "rev-1"},
		snapshotNames(t, nextEvent(t, events)),
		"the snapshot holds exactly the bundles the filter selects")
}

func TestWatchDeliversEveryObjectOfABurst(t *testing.T) {
	t.Parallel()
	ws, url := newTestServer(t, newFakeClient(t))
	events, _, _ := openStream(t, url, testWatchQuery)
	require.Empty(t, snapshotNames(t, nextEvent(t, events)))

	// Many PromotionStrategies changing at the same moment is the normal case (one commit
	// often moves several of them). Large payloads keep the handler busy writing while the
	// rest of the burst is published.
	const count = 50
	want := map[string]bool{}
	for i := range count {
		bundle := testBundle(testNamespace, fmt.Sprintf("app-%02d", i), "rev-1")
		bundle.Annotations = map[string]string{"padding": strings.Repeat("x", 100_000)}
		want[bundle.Name] = true
		ws.publishBundle(bundle)
	}

	got := map[string]bool{}
	for len(got) < count {
		ev := nextEvent(t, events)
		require.Equal(t, promotionStrategyDetailsKind, ev.name)
		name, _ := revisionOf(t, ev.data)
		got[name] = true
	}
	assert.Equal(t, want, got)
}

func TestWatchEndsOnTheLatestStateOfEveryObject(t *testing.T) {
	t.Parallel()
	ws, url := newTestServer(t, newFakeClient(t))
	events, _, _ := openStream(t, url, testWatchQuery)
	require.Empty(t, snapshotNames(t, nextEvent(t, events)))

	// Publish far faster than the stream can be written. Intermediate revisions may be
	// skipped, but the last thing sent for each object must be its final revision.
	const objects, revisions = 5, 200
	final := fmt.Sprintf("rev-%d", revisions)
	for rev := 1; rev <= revisions; rev++ {
		for i := range objects {
			ws.publishBundle(testBundle(testNamespace, fmt.Sprintf("app-%d", i), fmt.Sprintf("rev-%d", rev)))
		}
	}

	latest := map[string]string{}
	atFinal := func() bool {
		if len(latest) != objects {
			return false
		}
		for _, revision := range latest {
			if revision != final {
				return false
			}
		}
		return true
	}
	for !atFinal() {
		name, revision := revisionOf(t, nextEvent(t, events).data)
		latest[name] = revision
	}
}

func TestWatchReportsDeletions(t *testing.T) {
	t.Parallel()
	ws, url := newTestServer(t, newFakeClient(t))
	events, _, _ := openStream(t, url, testWatchQuery)
	require.Empty(t, snapshotNames(t, nextEvent(t, events)))

	ws.publishBundleDeleted(testBundle(testNamespace, "app-a", "rev-1"))

	ev := nextEvent(t, events)
	assert.Equal(t, eventDeleted, ev.name)
	var ref objectRef
	require.NoError(t, json.Unmarshal([]byte(ev.data), &ref), "the payload must be valid JSON")
	assert.Equal(t, objectRef{Kind: promotionStrategyDetailsKind, Namespace: testNamespace, Name: "app-a"}, ref)
}

func TestWatchReconnectStartsFromTheCurrentState(t *testing.T) {
	t.Parallel()
	c := newFakeClient(t,
		testBundle(testNamespace, "app-a", "rev-1"),
		testBundle(testNamespace, "app-b", "rev-1"),
	)
	_, url := newTestServer(t, c)

	events, _, stop := openStream(t, url, testWatchQuery)
	require.Len(t, snapshotNames(t, nextEvent(t, events)), 2)
	stop()

	// While the client is disconnected, app-a changes and app-b is deleted.
	updated := &viewv1alpha1.PromotionStrategyDetails{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: testNamespace, Name: "app-a"}, updated))
	updated.PromotionStrategy.Spec.RepositoryReference.Name = "rev-2"
	require.NoError(t, c.Update(t.Context(), updated))
	require.NoError(t, c.Delete(t.Context(), testBundle(testNamespace, "app-b", "rev-1")))

	events, _, _ = openStream(t, url, testWatchQuery)
	assert.Equal(t,
		map[string]string{"app-a": "rev-2"},
		snapshotNames(t, nextEvent(t, events)),
		"a reconnecting client must be brought up to date, not left with what it had")
}

func TestWatchSendsChangesMadeDuringTheSnapshotAfterIt(t *testing.T) {
	t.Parallel()
	var ws *WebServer
	c := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(testBundle(testNamespace, "app-a", "rev-1")).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				err := c.List(ctx, list, opts...)
				// A change lands after the snapshot was read but before it is sent.
				ws.publishBundle(testBundle(testNamespace, "app-a", "rev-2"))
				return err //nolint:wrapcheck // Pass the fake client's result through unchanged.
			},
		}).
		Build()
	ws, url := newTestServer(t, c)

	events, _, _ := openStream(t, url, testWatchQuery)

	assert.Equal(t, map[string]string{"app-a": "rev-1"}, snapshotNames(t, nextEvent(t, events)))
	_, revision := revisionOf(t, nextEvent(t, events).data)
	assert.Equal(t, "rev-2", revision, "the change must follow the snapshot, not be lost between the two")
}

func TestWatchSendsHeartbeats(t *testing.T) {
	t.Parallel()
	ws, url := newTestServer(t, newFakeClient(t))
	ws.heartbeatInterval = 20 * time.Millisecond

	events, _, _ := openStream(t, url, testWatchQuery)

	timeout := time.After(eventTimeout)
	for {
		select {
		case ev, ok := <-events:
			require.True(t, ok, "the stream ended unexpectedly")
			if ev.name == eventPing {
				return
			}
		case <-timeout:
			require.FailNow(t, "no heartbeat on an idle stream")
		}
	}
}

func TestWatchUnsubscribesWhenTheClientDisconnects(t *testing.T) {
	t.Parallel()
	ws, url := newTestServer(t, newFakeClient(t))

	events, _, stop := openStream(t, url, testWatchQuery)
	nextEvent(t, events)
	require.Equal(t, 1, ws.events.subscriberCount())

	// No event is published after the disconnect: the handler has to notice on its own.
	stop()
	require.Eventually(t, func() bool { return ws.events.subscriberCount() == 0 }, eventTimeout, 10*time.Millisecond)
}

func TestWatchOnlySendsWhatTheFilterSelects(t *testing.T) {
	t.Parallel()
	ws, url := newTestServer(t, newFakeClient(t))
	events, _, _ := openStream(t, url, testWatchQuery+"&name=app-a")
	require.Empty(t, snapshotNames(t, nextEvent(t, events)))

	ws.publishBundle(testBundle("team-b", "app-a", "other-namespace"))
	ws.publishBundle(testBundle(testNamespace, "app-b", "other-name"))
	ws.publishBundle(testBundle(testNamespace, "app-a", "selected"))

	_, revision := revisionOf(t, nextEvent(t, events).data)
	assert.Equal(t, "selected", revision)
}

func TestWatchRejectsAMalformedFilter(t *testing.T) {
	t.Parallel()
	_, url := newTestServer(t, newFakeClient(t))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/watch?namespace=%5B", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestBundleForClientLeavesTheCachedObjectUntouched(t *testing.T) {
	t.Parallel()
	cached := testBundle(testNamespace, "app-a", "rev-1")
	cached.Annotations = map[string]string{lastAppliedAnnotation: "{}", "keep": "me"}
	cached.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "controller"}}

	out := bundleForClient(cached)

	assert.Equal(t, promotionStrategyDetailsKind, out.Kind)
	assert.Equal(t, map[string]string{"keep": "me"}, out.Annotations)
	assert.Empty(t, out.ManagedFields)

	// The input is shared with the informer cache and every other reader of it.
	assert.Empty(t, cached.Kind)
	assert.Equal(t, map[string]string{lastAppliedAnnotation: "{}", "keep": "me"}, cached.Annotations)
	assert.Len(t, cached.ManagedFields, 1)
}

func TestSubscriberKeepsOnlyTheLatestMessagePerObject(t *testing.T) {
	t.Parallel()
	filter, err := newWatchFilter("", "", "")
	require.NoError(t, err)
	sub := newSubscriber(filter)

	sub.enqueue(Message{Kind: "Kind", Namespace: "ns", Name: "a", Data: "a1"})
	sub.enqueue(Message{Kind: "Kind", Namespace: "ns", Name: "b", Data: "b1"})
	sub.enqueue(Message{Kind: "Kind", Namespace: "ns", Name: "a", Data: "a2"})
	sub.enqueue(Message{Kind: "Kind", Namespace: "ns", Name: "a", Deleted: true})

	got := sub.drain()
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Name, "an object keeps its place in the queue when it changes again")
	assert.True(t, got[0].Deleted, "the latest message for an object wins")
	assert.Equal(t, "b1", got[1].Data)
	assert.Empty(t, sub.drain())
}
