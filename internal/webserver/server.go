package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"strings"
	"time"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
	viewv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/view/v1alpha1"
	webserverlogr "github.com/argoproj-labs/gitops-promoter/internal/webserver/logr"
	"github.com/argoproj-labs/gitops-promoter/ui/web"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	controllerruntime "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var logger = ctrl.Log.WithName("webServer")

// promotionStrategyDetailsKind is the Kind of the aggregated bundle resource and
// the SSE event name the dashboard UI subscribes to.
const promotionStrategyDetailsKind = "PromotionStrategyDetails"

// lastAppliedAnnotation is dropped from objects sent to the UI: it is large and unused there.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// defaultHeartbeatInterval is how often a /watch stream sends a ping. It is well under the
// 60s idle timeout that most ingresses and load balancers default to, so a stream with
// nothing to report is not cut off, and it bounds how long a dead connection goes unnoticed.
const defaultHeartbeatInterval = 15 * time.Second

// WebServer handles the web server functionality for the dashboard and API endpoints.
type WebServer struct {
	client.Client
	Scheme *runtime.Scheme
	// events fans bundle changes out to the connected /watch clients.
	events *broadcaster
	// heartbeatInterval is how often a /watch stream sends a ping.
	heartbeatInterval time.Duration
}

// NewWebServer creates a new WebServer instance with the given manager.
func NewWebServer(mgr controllerruntime.Manager) WebServer {
	return WebServer{
		events:            newBroadcaster(),
		heartbeatInterval: defaultHeartbeatInterval,
		Scheme:            mgr.GetScheme(),
		Client:            mgr.GetClient(),
	}
}

// Reconcile handles the reconciliation logic for the WebServer controller.
func (ws *WebServer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// bundleForClient returns the bundle as it is sent to the UI: with its kind and apiVersion
// set and without managed fields or the last-applied-configuration annotation. The bundle
// comes from the shared informer cache and must not be modified, so the changes are made
// on a shallow copy.
func bundleForClient(bundle *viewv1alpha1.PromotionStrategyDetails) *viewv1alpha1.PromotionStrategyDetails {
	out := *bundle
	out.SetGroupVersionKind(viewv1alpha1.GroupVersion.WithKind(promotionStrategyDetailsKind))
	out.ManagedFields = nil
	if _, ok := out.Annotations[lastAppliedAnnotation]; ok {
		out.Annotations = maps.Clone(out.Annotations)
		delete(out.Annotations, lastAppliedAnnotation)
	}
	return &out
}

// publishBundle broadcasts the current state of a bundle to the /watch clients.
func (ws *WebServer) publishBundle(bundle *viewv1alpha1.PromotionStrategyDetails) {
	data, err := json.Marshal(bundleForClient(bundle))
	if err != nil {
		logger.Error(err, "failed to marshal for SSE", "namespace", bundle.GetNamespace(), "name", bundle.GetName())
		return
	}
	ws.events.publish(Message{
		Name:      bundle.GetName(),
		Namespace: bundle.GetNamespace(),
		Kind:      promotionStrategyDetailsKind,
		Data:      string(data),
	})
}

// publishBundleDeleted broadcasts the deletion of a bundle to the /watch clients.
func (ws *WebServer) publishBundleDeleted(bundle *viewv1alpha1.PromotionStrategyDetails) {
	ref := objectRef{Kind: promotionStrategyDetailsKind, Namespace: bundle.GetNamespace(), Name: bundle.GetName()}
	data, err := json.Marshal(ref)
	if err != nil {
		logger.Error(err, "failed to marshal for SSE", "namespace", ref.Namespace, "name", ref.Name)
		return
	}
	ws.events.publish(Message{
		Name:      ref.Name,
		Namespace: ref.Namespace,
		Kind:      ref.Kind,
		Data:      string(data),
		Deleted:   true,
	})
}

// SetupWithManager sets up the WebServer controller with the given manager.
//
// Rather than watching the four raw promoter CRD kinds and stitching them together
// in the browser, the dashboard now watches the single server-computed
// PromotionStrategyDetails bundle served by the dashboard aggregation apiserver and
// forwards each bundle to clients over SSE.
func (ws *WebServer) SetupWithManager(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		Named("webServer").
		Watches(&viewv1alpha1.PromotionStrategyDetails{}, handler.Funcs{
			CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				if bundle, ok := e.Object.(*viewv1alpha1.PromotionStrategyDetails); ok {
					ws.publishBundle(bundle)
				}
			},
			UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				if bundle, ok := e.ObjectNew.(*viewv1alpha1.PromotionStrategyDetails); ok {
					ws.publishBundle(bundle)
				}
			},
			DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				if bundle, ok := e.Object.(*viewv1alpha1.PromotionStrategyDetails); ok {
					ws.publishBundleDeleted(bundle)
				}
			},
		}).
		Complete(ws)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}
	return nil
}

// StartDashboard starts the dashboard web server on the specified address.
func (ws *WebServer) StartDashboard(ctx context.Context, addr string) error {
	// Embed dashboard UI from DashboardFS
	distFS, err := fs.Sub(web.DashboardFS, "static")
	if err != nil {
		return fmt.Errorf("failed to create sub FS: %w", err)
	}

	assetsFS, err := fs.Sub(distFS, "assets")
	if err != nil {
		return fmt.Errorf("failed to create assets sub FS: %w", err)
	}

	router := gin.New()
	router.Use(webserverlogr.Ginlogr(logger, time.RFC3339, true))
	router.Use(webserverlogr.RecoveryWithLogr(logger, time.RFC3339, true, true))

	ws.registerAPIRoutes(router)

	// Handle favicon (serve from embed so /favicon.png is not caught by SPA catch-all)
	router.GET("/favicon.ico", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/favicon.png", func(c *gin.Context) {
		f, err := distFS.Open("favicon.png")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		defer func() {
			if cerr := f.Close(); cerr != nil {
				logger.Error(cerr, "failed to close favicon.png")
			}
		}()
		content, err := io.ReadAll(f)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "image/png", content)
	})

	// Serve static files from embed
	router.StaticFS("/assets", http.FS(assetsFS))
	router.GET("/", func(c *gin.Context) {
		f, err := distFS.Open("index.html")
		if err != nil {
			c.String(500, "Failed to open index.html: %v", err)
			return
		}
		defer func() {
			cerr := f.Close()
			if cerr != nil {
				logger.Error(cerr, "failed to close file")
			}
		}()
		content, err := io.ReadAll(f)
		if err != nil {
			c.String(500, "Failed to read index.html: %v", err)
			return
		}
		c.Data(200, "text/html; charset=utf-8", content)
	})

	// SPA route handler for nested routes
	router.GET("/:section/*path", func(c *gin.Context) {
		section := c.Param("section")

		// Skip if it's an API or static asset
		if section == "watch" || section == "list" || section == "healthz" ||
			section == "assets" {
			c.Status(http.StatusNotFound)
			return
		}
		// Serve index.html for SPA routes
		f, err := distFS.Open("index.html")
		if err != nil {
			c.String(500, "Failed to open index.html: %v", err)
			return
		}
		defer func() {
			cerr := f.Close()
			if cerr != nil {
				logger.Error(cerr, "failed to close file")
			}
		}()
		content, err := io.ReadAll(f)
		if err != nil {
			c.String(500, "Failed to read index.html: %v", err)
			return
		}
		c.Data(200, "text/html; charset=utf-8", content)
	})

	// Serve index.html for all other routes from embed
	router.NoRoute(func(c *gin.Context) {
		c.FileFromFS("index.html", http.FS(distFS))
	})

	server := http.Server{
		Addr:    addr,
		Handler: router,
	}

	go func() {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			logger.Info("web server closed")
		} else if err != nil {
			logger.Error(err, "error listening for server")
		}
	}()

	logger.Info("web server started")

	<-ctx.Done()
	logger.Info("web server stopped")

	if err := server.Shutdown(ctx); err != nil {
		logger.Error(err, "web server shutdown failed", "error", err)
	}
	logger.Info("web server exited properly")

	return nil
}

func (ws *WebServer) httpList(c *gin.Context) {
	if c.Query("kind") == "" {
		c.JSON(http.StatusBadRequest, "kind is required")
		return
	}
	kind := strings.ToLower(c.Query("kind"))
	listOptions := &client.ListOptions{}
	if c.Query("namespace") != "" {
		listOptions = &client.ListOptions{Namespace: c.Query("namespace")}
	}

	switch kind {
	case "promotionstrategydetails":
		bundleList := &viewv1alpha1.PromotionStrategyDetailsList{}
		err := ws.List(c, bundleList, listOptions)
		if err != nil {
			c.JSON(http.StatusInternalServerError, err.Error())
			return
		}
		items := bundleList.Items
		if items == nil {
			items = []viewv1alpha1.PromotionStrategyDetails{}
		}
		c.JSON(http.StatusOK, items)

	case "namespace":
		if c.Query("namespace") != "" {
			c.JSON(http.StatusBadRequest, "namespace is not valid for listing namespaces")
			return
		}

		m := make(map[string]bool)
		var namespaces []string

		psl := &promoterv1alpha1.PromotionStrategyList{}
		err := ws.List(c, psl, &client.ListOptions{})
		if err != nil {
			c.JSON(http.StatusInternalServerError, err.Error())
			return
		}
		for _, ps := range psl.Items {
			ns := ps.Namespace
			if !m[ns] {
				m[ns] = true
				namespaces = append(namespaces, ns)
			}
		}
		c.JSON(http.StatusOK, namespaces)

	default:
		c.JSON(http.StatusBadRequest, "invalid kind")
	}
}

// registerAPIRoutes registers the compression middleware and the dashboard's API endpoints.
func (ws *WebServer) registerAPIRoutes(router *gin.Engine) {
	// gzip is registered before the routes so it also covers the /watch SSE stream.
	// The bundles streamed over /watch are large, repetitive JSON, so compression is a
	// clear bandwidth win. gin-contrib/gzip's writer flushes the underlying gzip.Writer
	// on Flush(), so the per-event c.Writer.Flush() in httpWatch still delivers events in
	// real time. No minLength is set: a non-zero minLength buffers sub-threshold frames in
	// a pre-compression buffer that Flush() does not drain, which would stall small SSE
	// events until a later larger one arrives.
	router.Use(gzip.Gzip(gzip.DefaultCompression))

	router.GET("/watch", ws.httpWatch)
	router.GET("/list", ws.httpList)

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, "ok")
	})
}

// httpWatch streams bundle changes to the browser as server-sent events.
//
// Every connection starts with a snapshot event holding the complete current state for the
// requested filter, followed by one event per change (named after the Kind for an update,
// "deleted" for a deletion) and a periodic ping. The browser's EventSource reconnects on its
// own when a connection drops, and because the new connection starts with a fresh snapshot,
// whatever changed while it was away is picked up rather than lost.
func (ws *WebServer) httpWatch(c *gin.Context) {
	filter, err := newWatchFilter(c.Query("kind"), c.Query("namespace"), c.Query("name"))
	if err != nil {
		c.JSON(http.StatusBadRequest, err.Error())
		return
	}
	ctx := c.Request.Context()

	// Subscribe before taking the snapshot. A change that lands in between is then queued
	// and sent after the snapshot, instead of falling into a gap between the two.
	sub := ws.events.subscribe(filter)
	defer ws.events.unsubscribe(sub)

	snapshot, err := ws.snapshot(ctx, filter, c.Query("namespace"))
	if err != nil {
		logger.Error(err, "failed to build watch snapshot")
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	header := c.Writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	// Tell nginx-based proxies not to buffer the stream, which would delay events.
	header.Set("X-Accel-Buffering", "no")

	// Writing the snapshot also sends the response headers right away. Until they arrive
	// the browser's EventSource is still connecting, and a proxy waiting on them for an
	// idle stream answers with a gateway timeout, which EventSource does not retry.
	if err := writeSSE(c.Writer, eventSnapshot, snapshot); err != nil {
		return
	}
	c.Writer.Flush()

	heartbeat := time.NewTicker(ws.heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			// The client went away (or the server is shutting down).
			return
		case <-sub.notify:
			for _, msg := range sub.drain() {
				if err := writeSSE(c.Writer, msg.event(), msg.Data); err != nil {
					return
				}
			}
		case <-heartbeat.C:
			if err := writeSSE(c.Writer, eventPing, "{}"); err != nil {
				return
			}
		}
		c.Writer.Flush()
	}
}

// snapshot returns the JSON array of every bundle the filter selects, as it is now.
func (ws *WebServer) snapshot(ctx context.Context, filter watchFilter, namespace string) (string, error) {
	listOptions := &client.ListOptions{}
	// A literal namespace narrows the List. A glob (or no namespace) lists everything and
	// leaves the selection to the filter below.
	if namespace != "" && !strings.ContainsAny(namespace, `*?[\`) {
		listOptions.Namespace = namespace
	}
	bundleList := &viewv1alpha1.PromotionStrategyDetailsList{}
	if err := ws.List(ctx, bundleList, listOptions); err != nil {
		return "", fmt.Errorf("failed to list PromotionStrategyDetails: %w", err)
	}

	items := make([]*viewv1alpha1.PromotionStrategyDetails, 0, len(bundleList.Items))
	for i := range bundleList.Items {
		bundle := &bundleList.Items[i]
		if filter.matches(promotionStrategyDetailsKind, bundle.Namespace, bundle.Name) {
			items = append(items, bundleForClient(bundle))
		}
	}
	data, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("failed to marshal watch snapshot: %w", err)
	}
	return string(data), nil
}
