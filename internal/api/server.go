// Package api implements the syd HTTP API server.
//
// Endpoints:
//
//	POST   /topology               — upload/replace a topology (push-via-JSON)
//	POST   /topology/compose       — merge source topologies into a composite graph
//	GET    /topology               — list topology IDs
//	GET    /topology/{id}          — describe a topology (stats)
//	GET    /topology/{id}/nodes    — list node IDs in a topology
//	DELETE /topology/{id}          — remove a topology
//	POST   /topology/{id}/policies — set/merge name→algo_id policy mappings
//	GET    /topology/{id}/policies — list current policy mappings
//	DELETE /topology/{id}/policies — clear all policy mappings
//
//	POST   /paths/request                  — request SRv6 paths for a workload
//	GET    /paths/{workload_id}            — get workload allocation status
//	GET    /paths/{workload_id}/flows      — SRv6 segment lists for pull model
//	GET    /paths/{workload_id}/events     — SSE stream of workload state changes
//	POST   /paths/{workload_id}/complete   — release paths when workload is done
//	POST   /paths/{workload_id}/heartbeat  — extend workload lease
//	GET    /paths/state                    — allocation table snapshot (all topologies)
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jalapeno/syd/internal/allocation"
	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/pathengine"
	"github.com/jalapeno/syd/internal/southbound"
	"github.com/jalapeno/syd/pkg/apitypes"
)

// Server holds the shared state accessed by all HTTP handlers.
type Server struct {
	store    *graph.Store
	tables   *allocation.TableSet
	driver   southbound.SouthboundDriver
	log      *slog.Logger
	policies *policyStore

	// spfCaches holds one SPFCache per topology ID, populated after each
	// compose or topology push. Keyed by topology ID.
	spfCachesMu sync.RWMutex
	spfCaches   map[string]*pathengine.SPFCache
}

// New creates a new API server backed by a no-op southbound driver. The store
// and tables are shared with the rest of the controller and must not be nil.
func New(store *graph.Store, tables *allocation.TableSet, log *slog.Logger) *Server {
	return NewWithDriver(store, tables, nil, log)
}

// NewWithDriver creates a new API server with an explicit southbound driver.
// Pass nil for driver to use a no-op (pull-only) driver.
func NewWithDriver(store *graph.Store, tables *allocation.TableSet, driver southbound.SouthboundDriver, log *slog.Logger) *Server {
	s := &Server{store: store, tables: tables, driver: driver, log: log, policies: newPolicyStore(), spfCaches: make(map[string]*pathengine.SPFCache)}
	if s.driver == nil {
		s.driver = noopDriver{}
	}
	// Register the release callback so the driver can delete forwarding state
	// when a workload's paths are freed (drain timer, lease expiry, etc.).
	tables.SetOnRelease(func(topoID, workloadID string, paths []*graph.Path) {
		flows := southbound.EncodeFlows(paths)
		if len(flows) == 0 {
			return
		}
		go func() {
			ctx := context.Background()
			if gd, ok := s.driver.(interface {
				DeleteFlows(context.Context, string, []southbound.EncodedFlow) error
			}); ok {
				if err := gd.DeleteFlows(ctx, topoID, flows); err != nil {
					s.log.Warn("southbound: DeleteFlows failed", "workload_id", workloadID, "error", err)
				}
			} else {
				if err := s.driver.DeleteWorkload(ctx, workloadID); err != nil {
					s.log.Warn("southbound: DeleteWorkload failed", "workload_id", workloadID, "error", err)
				}
			}
		}()
	})
	return s
}

// ComposeRecipe describes a single auto-compose job: a target topology ID and
// the ordered list of source topology IDs to merge into it.
type ComposeRecipe struct {
	TargetID  string
	SourceIDs []string
}

// StartAutoCompose launches a background goroutine that watches the source
// graphs named in each recipe and re-composes the target whenever any source
// version changes. It also composes immediately on first call (once all sources
// are present). The goroutine exits when ctx is cancelled.
func (s *Server) StartAutoCompose(ctx context.Context, recipes []ComposeRecipe) {
	for _, r := range recipes {
		go s.autoComposeLoop(ctx, r)
	}
}

func (s *Server) autoComposeLoop(ctx context.Context, recipe ComposeRecipe) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastVersionSum int64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Gather source graphs; skip if any are not yet available.
		sources := make([]*graph.Graph, 0, len(recipe.SourceIDs))
		missing := false
		var versionSum int64
		for _, id := range recipe.SourceIDs {
			g := s.store.Get(id)
			if g == nil {
				missing = true
				break
			}
			sources = append(sources, g)
			// Use the graph's own write sequence rather than the store-level
			// version. store.Put is only called once per BMP-driven graph (at
			// creation); subsequent BMP mutations modify the graph in place
			// without calling store.Put, so store.Version never advances past 1
			// and the poller would skip every recompose after the first tick.
			versionSum += g.WriteSeq()
		}
		if missing {
			continue
		}

		// Only recompose when something changed.
		if versionSum == lastVersionSum {
			continue
		}
		lastVersionSum = versionSum

		composed := graph.Compose(recipe.TargetID, sources...)
		s.store.Put(composed)
		s.tables.Put(composed.ID(), allocation.NewTable(composed.ID()))

		s.log.Info("auto-compose: topology recomposed",
			"topology_id", recipe.TargetID,
			"sources", recipe.SourceIDs,
			"stats", composed.Stats(),
		)

		// Warm up the SPF cache for the newly composed graph in the background
		// so that the next path request hits cached leaf-pair results instead
		// of running Dijkstra for every GPU-pair from scratch.
		go s.warmupCache(recipe.TargetID, composed)
	}
}

// warmupCache creates (or replaces) the SPFCache for topoID and runs Warmup
// against the given graph. Called in a goroutine after each auto-compose.
func (s *Server) warmupCache(topoID string, g *graph.Graph) {
	cache := pathengine.NewSPFCache()
	cache.Warmup(g, 0) // algoID=0 covers the default (unconstrained) case

	s.spfCachesMu.Lock()
	s.spfCaches[topoID] = cache
	s.spfCachesMu.Unlock()

	s.log.Info("spf cache warmed up",
		"topology_id", topoID,
		"entries", cache.Len(),
	)
}

// spfCacheFor returns the SPFCache for the given topology, or nil if none exists.
func (s *Server) spfCacheFor(topoID string) *pathengine.SPFCache {
	s.spfCachesMu.RLock()
	defer s.spfCachesMu.RUnlock()
	return s.spfCaches[topoID]
}

// noopDriver is used when no explicit southbound driver is configured.
type noopDriver struct{}

func (noopDriver) ProgramWorkload(_ context.Context, _ *southbound.ProgramRequest) error {
	return nil
}
func (noopDriver) DeleteWorkload(_ context.Context, _ string) error { return nil }

// Handler returns an http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /topology", s.handleTopologyPush)
	mux.HandleFunc("POST /topology/compose", s.handleTopologyCompose)
	mux.HandleFunc("GET /topology", s.handleTopologyList)
	mux.HandleFunc("GET /topology/{id}", s.handleTopologyGet)
	mux.HandleFunc("GET /topology/{id}/nodes", s.handleTopologyNodes)
	mux.HandleFunc("DELETE /topology/{id}", s.handleTopologyDelete)
	mux.HandleFunc("POST /topology/{id}/policies", s.handlePoliciesSet)
	mux.HandleFunc("GET /topology/{id}/policies", s.handlePoliciesGet)
	mux.HandleFunc("DELETE /topology/{id}/policies", s.handlePoliciesDelete)
	mux.HandleFunc("GET /topology/{id}/graph", s.handleTopologyGraph)

	mux.HandleFunc("POST /paths/request", s.handlePathRequest)
	mux.HandleFunc("GET /paths/state", s.handlePathState)
	mux.HandleFunc("GET /paths/{workload_id}", s.handleWorkloadStatus)
	mux.HandleFunc("GET /paths/{workload_id}/events", s.handleWorkloadEvents)
	mux.HandleFunc("GET /paths/{workload_id}/flows", s.handleWorkloadFlows)
	mux.HandleFunc("POST /paths/{workload_id}/complete", s.handleWorkloadComplete)
	mux.HandleFunc("POST /paths/{workload_id}/heartbeat", s.handleHeartbeat)

	return mux
}

// --- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string, detail ...string) {
	writeJSON(w, status, apitypes.ErrorResponse{Error: msg, Detail: detail})
}

func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}
