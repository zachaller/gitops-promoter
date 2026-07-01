package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
)

// changeRecord is the auto-approved, auto-closable change record the mock service hands back
// to the change-management-open/-approval/-close WebRequestCommitStatus trio (see wrcs.go).
// Field names are lowerCamelCase in JSON to match what those templates/expressions read
// (Response.Body.change_records[].status, .change_request.start_time/.end_time, etc.).
type changeRecord struct {
	Start    time.Time `json:"-"`
	End      time.Time `json:"-"`
	ID       string    `json:"id"`
	AssetID  string    `json:"asset_id"`
	CommitID string    `json:"commit_id"`
	Status   string    `json:"status"`
	closed   bool
}

// changeRequestWindow mirrors the change_request.{start_time,end_time} shape the trigger/success
// expressions read from search and open responses.
type changeRequestWindow struct {
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

// changeMgmtStore is an in-memory, auto-approving change-management-service. "Auto-approving"
// means every opened record is immediately APPROVED with a window covering now, so the
// approval gate succeeds on its very next poll (matching the "auto-success" WRCS trio these
// endpoints are built for) - unless --approve-delay defers that.
type changeMgmtStore struct {
	records      map[string]*changeRecord
	approveDelay time.Duration
	mu           sync.Mutex
}

func newChangeMgmtStore(approveDelay time.Duration) *changeMgmtStore {
	return &changeMgmtStore{records: make(map[string]*changeRecord), approveDelay: approveDelay}
}

// open returns the existing record for (assetID, commitID) if one is already active, or creates
// a new one. Idempotent on purpose: WebRequestCommitStatus is at-least-once delivery, so the open
// trigger may fire more than once for the same commit.
func (s *changeMgmtStore) open(assetID, commitID string) *changeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, r := range s.records {
		if !r.closed && r.AssetID == assetID && r.CommitID == commitID {
			return r
		}
	}

	now := time.Now().UTC()
	status := "APPROVED"
	if s.approveDelay > 0 {
		status = "PENDING"
	}
	r := &changeRecord{
		ID:       uuid.NewString(),
		AssetID:  assetID,
		CommitID: commitID,
		Status:   status,
		Start:    now,
		End:      now.Add(8 * time.Hour),
	}
	s.records[r.ID] = r

	if s.approveDelay > 0 {
		delay := s.approveDelay
		go func() {
			time.Sleep(delay)
			s.mu.Lock()
			defer s.mu.Unlock()
			if rec, ok := s.records[r.ID]; ok && !rec.closed {
				rec.Status = "APPROVED"
			}
		}()
	}

	return r
}

// search returns every non-closed record matching (assetID, commitID).
func (s *changeMgmtStore) search(assetID, commitID string) []*changeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*changeRecord
	for _, r := range s.records {
		if !r.closed && r.AssetID == assetID && r.CommitID == commitID {
			out = append(out, r)
		}
	}
	return out
}

// close marks id as closed (excluded from future search results) and reports whether it existed.
func (s *changeMgmtStore) close(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.records[id]
	if !ok {
		return false
	}
	r.closed = true
	return true
}

// newChangeMgmtMockHandler wires the three endpoints the change-management WebRequestCommitStatus
// trio calls (see wrcs.go's httpRequest.urlTemplate values) under pathPrefix.
func newChangeMgmtMockHandler(store *changeMgmtStore, pathPrefix string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+pathPrefix+"/change/open", handleChangeOpen(store))
	mux.HandleFunc("GET "+pathPrefix+"/changes/search", handleChangeSearch(store))
	mux.HandleFunc("POST "+pathPrefix+"/change/close/{id}", handleChangeClose(store))
	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		color.Cyan("  [change-mgmt-mock] %s %s -> %d\n", r.Method, r.URL.RequestURI(), rec.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func handleChangeOpen(store *changeMgmtStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AssetID  string `json:"asset_id"`
			CommitID string `json:"commit_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
			return
		}

		rec := store.open(body.AssetID, body.CommitID)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"id":      rec.ID,
			"message": "change record created",
			"change_request": changeRequestWindow{
				StartTime: rec.Start.Format(time.RFC3339),
				EndTime:   rec.End.Format(time.RFC3339),
			},
		})
	}
}

func handleChangeSearch(store *changeMgmtStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		assetID := r.URL.Query().Get("asset_id")
		commitID := r.URL.Query().Get("commit_id")

		records := store.search(assetID, commitID)
		out := make([]map[string]any, 0, len(records))
		for _, rec := range records {
			out = append(out, map[string]any{
				"id":     rec.ID,
				"status": rec.Status,
				"change_request": changeRequestWindow{
					StartTime: rec.Start.Format(time.RFC3339),
					EndTime:   rec.End.Format(time.RFC3339),
				},
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"change_records": out})
	}
}

func handleChangeClose(store *changeMgmtStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !store.close(id) {
			http.Error(w, "change record not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "change_execution_status": "SUCCEEDED"})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set(headerContentType, headerJSON)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Headers/status are already written; nothing left to do but log. A write error here
		// almost always means the client (the controller's HTTP client) disconnected mid-response.
		color.Red("  [change-mgmt-mock] failed to write response body: %v\n", err)
	}
}

// newChangeMgmtMockCommand implements `promoter loadtest change-mgmt-mock`: a throwaway,
// in-memory, auto-approving stand-in for the external change-management service the
// change-management-open/-approval/-close WebRequestCommitStatus trio calls (see
// --change-mgmt-base-url in `setup`). Not meant to be realistic - just enough surface area for
// those three gates to flow from pending to success without a hand-rolled curl script.
func newChangeMgmtMockCommand() *cobra.Command {
	var addr, pathPrefix string
	var approveDelay time.Duration

	cmd := &cobra.Command{
		Use:   "change-mgmt-mock",
		Short: "Run a throwaway mock of the change-management service the WRCS trio calls",
		Long: "Serves an in-memory, auto-approving stand-in for the external change-management " +
			"service that the change-management-open/-approval/-close WebRequestCommitStatus " +
			"gates call (see --change-mgmt-base-url in `setup`). Every opened change record is " +
			"immediately APPROVED (unless --approve-delay is set) with a window covering now, " +
			"and can be closed via the close endpoint - enough for those three gates to flow " +
			"from pending to success without a hand-rolled service.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := newChangeMgmtStore(approveDelay)
			handler := newChangeMgmtMockHandler(store, strings.TrimSuffix(pathPrefix, "/"))

			server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
			ctx := ctrl.SetupSignalHandler()

			go func() {
				<-ctx.Done()
				// Deliberately not derived from ctx (which is already Done): this is a fresh
				// bounded timeout for draining in-flight requests during shutdown.
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = server.Shutdown(shutdownCtx) //nolint:contextcheck // shutdown timeout must outlive the now-cancelled ctx
			}()

			color.Green("change-mgmt-mock listening on %s (path prefix %s)\n", addr, pathPrefix)
			color.Yellow("Point --change-mgmt-base-url at http://<this-host>%s%s\n", addr, pathPrefix)
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("change-mgmt-mock server error: %w", err)
			}
			color.Green("change-mgmt-mock stopped\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", ":8987", "Address to listen on")
	cmd.Flags().StringVar(&pathPrefix, "path-prefix", "/v1/change-management-service",
		"Path prefix for the change-management endpoints; must match --change-mgmt-base-url's path")
	cmd.Flags().DurationVar(&approveDelay, "approve-delay", 0,
		"Delay before a newly opened change record becomes APPROVED; 0 = immediately (auto-success)")
	return cmd
}
