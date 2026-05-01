package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jalapeno/syd/internal/allocation"
	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/pathengine"
	"github.com/jalapeno/syd/internal/southbound"
	"github.com/jalapeno/syd/pkg/apitypes"
)

// handlePathRequest is the primary endpoint for workload schedulers and
// PyTorch jobs. It accepts a PathRequest, resolves endpoints in the topology,
// computes SRv6 paths (stub — path engine to be wired in next), allocates
// them in the state table, and returns the segment lists.
func (s *Server) handlePathRequest(w http.ResponseWriter, r *http.Request) {
	var req apitypes.PathRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.WorkloadID == "" {
		writeError(w, http.StatusBadRequest, "workload_id is required")
		return
	}
	if req.TopologyID == "" {
		writeError(w, http.StatusBadRequest, "topology_id is required")
		return
	}
	if req.DstPrefix != "" && req.SrcPrefix != "" {
		writeError(w, http.StatusBadRequest, "dst_prefix and src_prefix are mutually exclusive")
		return
	}
	isPrefixRequest := req.DstPrefix != "" || req.SrcPrefix != ""
	minEndpoints := 2
	if isPrefixRequest {
		minEndpoints = 1
	}
	if len(req.Endpoints) < minEndpoints {
		if isPrefixRequest {
			writeError(w, http.StatusBadRequest, "at least 1 endpoint is required for prefix path requests")
		} else {
			writeError(w, http.StatusBadRequest, "at least 2 endpoints are required")
		}
		return
	}

	g := s.store.Get(req.TopologyID)
	if g == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("topology %q not found", req.TopologyID))
		return
	}

	table := s.tables.Get(req.TopologyID)
	if table == nil {
		writeError(w, http.StatusInternalServerError, "allocation table missing for topology")
		return
	}

	// Resolve a named policy to an algo_id, overriding any explicit algo_id in
	// Constraints. This decouples job specifications from IGP internals: the
	// operator registers mappings (e.g. "carbon-optimized" → 130) once via
	// POST /topology/{id}/policies; job schedulers reference them by name.
	if req.Policy != "" {
		algoID, ok := s.policies.Resolve(req.TopologyID, req.Policy)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity,
				fmt.Sprintf("unknown policy %q for topology %q", req.Policy, req.TopologyID))
			return
		}
		if req.Constraints == nil {
			req.Constraints = &apitypes.PathRequestConstraints{}
		}
		req.Constraints.AlgoID = algoID
	}

	// Resolve service level presets into disjointness + sharing.
	disjointness, sharing := resolveServiceLevel(req)

	s.log.Info("path request received",
		"workload_id", req.WorkloadID,
		"topology_id", req.TopologyID,
		"endpoints", len(req.Endpoints),
		"disjointness", disjointness,
		"sharing", sharing,
	)

	// Route to prefix-aware or standard compute path.
	var paths []graph.Path
	var failedPairs []string
	var prefixID, bgpNexthop string

	if req.DstPrefix != "" || req.SrcPrefix != "" {
		cidr := req.DstPrefix
		direction := pathengine.PrefixDst
		if req.SrcPrefix != "" {
			cidr = req.SrcPrefix
			direction = pathengine.PrefixSrc
		}
		presult, err := pathengine.ComputePrefixPaths(g, table, req, cidr, direction, disjointness, sharing)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		paths = presult.Paths
		failedPairs = presult.FailedPairs
		prefixID = presult.PrefixVertexID
		bgpNexthop = presult.BGPNexthop
	} else {
		cache := s.spfCacheFor(req.TopologyID)
		result, err := pathengine.ComputeWithCache(g, table, req, disjointness, sharing, cache)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		paths = result.Paths
		failedPairs = result.FailedPairs
	}

	// Convert graph.Path results to API response types.
	apiPaths := make([]apitypes.PathResult, len(paths))
	for i, p := range paths {
		apiPaths[i] = apitypes.PathResult{
			SrcID:       p.SrcID,
			DstID:       p.DstID,
			SegmentList: p.SegmentList,
			PathID:      p.ID,
			VertexIDs:   p.VertexIDs,
			EdgeIDs:     p.EdgeIDs,
			BGPNexthop:  bgpNexthop,
			PrefixID:    prefixID,
			Metric: apitypes.PathResultMetric{
				IGPMetric:    p.Metric.IGPMetric,
				DelayUS:      p.Metric.DelayUS,
				BottleneckBW: p.Metric.BottleneckBW,
				HopCount:     p.Metric.HopCount,
			},
		}
	}

	snap := table.Snapshot()
	resp := apitypes.PathResponse{
		WorkloadID: req.WorkloadID,
		TopologyID: req.TopologyID,
		Paths:      apiPaths,
		AllocationState: apitypes.AllocationSummary{
			TotalFreeAfter: snap.Free,
		},
	}

	if len(failedPairs) > 0 {
		s.log.Warn("some pairs could not be routed",
			"workload_id", req.WorkloadID,
			"failed", len(failedPairs),
		)
	}

	// Asynchronously program the southbound driver so allocation latency is not
	// affected by gNMI round-trips. Failures are logged but do not change the
	// HTTP response — the workload remains allocated and the caller can retry
	// via the /flows endpoint.
	if len(paths) > 0 {
		// Convert value slice to pointer slice for EncodeFlows.
		pathPtrs := make([]*graph.Path, len(paths))
		for i := range paths {
			p := paths[i]
			pathPtrs[i] = &p
		}
		wlID := req.WorkloadID
		topoID := req.TopologyID
		go func() {
			flows := southbound.EncodeFlows(pathPtrs)
			preq := &southbound.ProgramRequest{
				WorkloadID: wlID,
				TopologyID: topoID,
				Flows:      flows,
			}
			if err := s.driver.ProgramWorkload(r.Context(), preq); err != nil {
				s.log.Warn("southbound: ProgramWorkload failed",
					"workload_id", wlID,
					"error", err,
				)
			}
		}()
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWorkloadStatus(w http.ResponseWriter, r *http.Request) {
	workloadID := r.PathValue("workload_id")

	// Search across all topology tables.
	for topologyID, table := range s.tables.All() {
		wl, ok := table.GetWorkload(workloadID)
		if !ok {
			continue
		}
		writeJSON(w, http.StatusOK, apitypes.WorkloadStatusResponse{
			WorkloadID:  workloadID,
			TopologyID:  topologyID,
			State:       string(wl.State),
			PathCount:   len(wl.PathIDs),
			CreatedAt:   wl.CreatedAt.Format(time.RFC3339),
			DrainReason: string(wl.DrainReason),
		})
		return
	}
	writeError(w, http.StatusNotFound, fmt.Sprintf("workload %q not found", workloadID))
}

func (s *Server) handleWorkloadComplete(w http.ResponseWriter, r *http.Request) {
	workloadID := r.PathValue("workload_id")

	var req apitypes.WorkloadCompleteRequest
	// Body is optional; ignore decode errors gracefully.
	_ = decodeJSON(r, &req)

	for _, table := range s.tables.All() {
		if _, ok := table.GetWorkload(workloadID); !ok {
			continue
		}
		if err := table.DrainWorkload(workloadID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if req.Immediate {
			if err := table.ReleaseWorkload(workloadID); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			if err := table.StartDrainTimer(workloadID); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		s.log.Info("workload complete", "workload_id", workloadID, "immediate", req.Immediate)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeError(w, http.StatusNotFound, fmt.Sprintf("workload %q not found", workloadID))
}

func (s *Server) handlePathState(w http.ResponseWriter, r *http.Request) {
	all := s.tables.All()
	snapshots := make([]interface{}, 0, len(all))
	for _, table := range all {
		snapshots = append(snapshots, table.Snapshot())
	}
	writeJSON(w, http.StatusOK, apitypes.AllocationTableResponse{Topologies: snapshots})
}

// handleHeartbeat extends the lease on an active workload, resetting its
// expiry timer to the original lease duration from now. Returns 204 on
// success, 404 if the workload is not found, and 409 if the workload is not
// active or was created without a lease.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	workloadID := r.PathValue("workload_id")
	for _, table := range s.tables.All() {
		if _, ok := table.GetWorkload(workloadID); !ok {
			continue
		}
		if err := table.ExtendLease(workloadID); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.log.Info("workload lease extended", "workload_id", workloadID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeError(w, http.StatusNotFound, fmt.Sprintf("workload %q not found", workloadID))
}

// handleWorkloadEvents streams server-sent events for a workload's lifecycle.
//
// The client receives one event immediately on connect (current state), then
// an event for each subsequent state transition, and the stream closes when
// the workload reaches the COMPLETE state or the client disconnects.
//
// Event format (Content-Type: text/event-stream):
//
//	event: workload_state
//	data: {"workload_id":"...","state":"active","drain_reason":"","path_count":N}
//
// drain_reason values: "topology_change" | "topology_replaced" |
// "lease_expired" | "workload_complete"
func (s *Server) handleWorkloadEvents(w http.ResponseWriter, r *http.Request) {
	workloadID := r.PathValue("workload_id")

	// Locate the table that owns this workload.
	var table *allocation.Table
	var topoID string
	for tid, t := range s.tables.All() {
		if _, ok := t.GetWorkload(workloadID); ok {
			table = t
			topoID = tid
			break
		}
	}
	if table == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("workload %q not found", workloadID))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported by this server")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if present

	sendEvent := func() bool {
		wl, ok := table.GetWorkload(workloadID)
		if !ok {
			return false
		}
		evt := apitypes.WorkloadEvent{
			WorkloadID:  workloadID,
			TopologyID:  topoID,
			State:       string(wl.State),
			PathCount:   len(wl.PathIDs),
			DrainReason: string(wl.DrainReason),
		}
		b, _ := json.Marshal(evt)
		fmt.Fprintf(w, "event: workload_state\ndata: %s\n\n", b)
		flusher.Flush()
		return wl.State != allocation.WorkloadComplete
	}

	// Send the initial snapshot and bail early if already complete.
	if !sendEvent() {
		return
	}

	for {
		ch, ok := table.Subscribe(workloadID)
		if !ok {
			return
		}
		select {
		case <-ch:
			if !sendEvent() {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// handleWorkloadFlows returns the SRv6 segment lists for all flows in a
// workload, ready for host-side programming (pull model / EncapModeHost).
//
// GET /paths/{workload_id}/flows
//
// Each entry in the response includes:
//   - segment_list: the packed uSID containers
//   - encap_flavor: "H.Encaps.Red" (single container) or "H.Encaps" (multi)
//   - outer_da: the outer IPv6 destination to use when sending traffic
//   - srh_raw: base64-encoded Type-4 SRH bytes, present only when >1 container
//
// Paths are returned as-allocated; if the workload has no paths (e.g. it was
// seeded with an empty path for testing) the flows array will be empty.
func (s *Server) handleWorkloadFlows(w http.ResponseWriter, r *http.Request) {
	workloadID := r.PathValue("workload_id")

	var foundTable *allocation.Table
	var topoID string
	for tid, t := range s.tables.All() {
		if _, ok := t.GetWorkload(workloadID); ok {
			foundTable = t
			topoID = tid
			break
		}
	}
	if foundTable == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("workload %q not found", workloadID))
		return
	}

	paths := foundTable.WorkloadPaths(workloadID)
	encoded := southbound.EncodeFlows(paths)

	entries := make([]apitypes.FlowEntry, 0, len(encoded))
	for _, f := range encoded {
		e := apitypes.FlowEntry{
			SrcNodeID:   f.SrcNodeID,
			DstNodeID:   f.DstNodeID,
			PathID:      f.PathID,
			SegmentList: f.SegmentList,
			EncapFlavor: f.EncapFlavor,
			OuterDA:     f.OuterDA,
		}
		if len(f.SRHRaw) > 0 {
			e.SRHRaw = base64.StdEncoding.EncodeToString(f.SRHRaw)
		}
		entries = append(entries, e)
	}

	writeJSON(w, http.StatusOK, apitypes.FlowsResponse{
		WorkloadID:    workloadID,
		TopologyID:    topoID,
		Flows:         entries,
		LeafPairFlows: buildLeafPairFlows(entries),
	})
}

// buildLeafPairFlows groups FlowEntries by their (SrcNodeID, DstNodeID) pair,
// producing a compact O(leaf²) representation. All flows that share the same
// attachment-node pair use the same segment list; the entry records how many
// individual GPU-pair flows it covers.
func buildLeafPairFlows(flows []apitypes.FlowEntry) []apitypes.LeafPairFlowEntry {
	if len(flows) == 0 {
		return nil
	}

	type pairKey struct{ src, dst string }
	seen := make(map[pairKey]int) // key → index in result
	var result []apitypes.LeafPairFlowEntry

	for _, f := range flows {
		k := pairKey{f.SrcNodeID, f.DstNodeID}
		if idx, ok := seen[k]; ok {
			result[idx].FlowCount++
			continue
		}
		seen[k] = len(result)
		result = append(result, apitypes.LeafPairFlowEntry{
			SrcNodeID:   f.SrcNodeID,
			DstNodeID:   f.DstNodeID,
			SegmentList: f.SegmentList,
			EncapFlavor: f.EncapFlavor,
			OuterDA:     f.OuterDA,
			SRHRaw:      f.SRHRaw,
			FlowCount:   1,
		})
	}
	return result
}

// resolveServiceLevel maps a PathRequest's ServiceLevel preset (or explicit
// Disjointness/Sharing fields) to concrete values used by the path engine and
// allocation table.
func resolveServiceLevel(req apitypes.PathRequest) (string, string) {
	switch req.ServiceLevel {
	case "lossless-disjoint":
		return "node", "exclusive"
	case "best-effort":
		return "none", "shared"
	case "shared-fabric":
		return "link", "shared"
	}
	// Fall back to explicit fields with defaults.
	d := req.Disjointness
	if d == "" {
		d = "none"
	}
	s := req.Sharing
	if s == "" {
		s = "exclusive"
	}
	return d, s
}
