// Package pathengine computes constrained SRv6 paths through a topology graph.
//
// Entry point: Compute(). It resolves endpoints, enumerates pairs, runs greedy
// constrained Dijkstra with disjointness exclusion, and constructs SRv6
// segment lists from uA and uN SIDs. When every SID carries a compatible
// SIDStructure, the segment list is compressed into uSID containers.
//
// Algorithm summary:
//  1. Resolve each EndpointSpec to its attached Node vertex (resolve.go).
//  2. Enumerate pairs according to PairingMode (allpairs.go).
//  3. For each pair, run Dijkstra over LinkEdge/Physical edges (dijkstra.go),
//     applying bandwidth, latency, and admin-group constraints (cost.go).
//  4. After each successful path, add its nodes/edges/SRLGs to an ExcludedSet
//     so subsequent pairs respect the requested disjointness level.
//  5. Build the SRv6 segment list: one uA SID per egress interface + the
//     destination node's uN SID as the final anchor (seglist.go).
//     If SIDStructure data is present, SIDs are packed into uSID containers.
//
// Pairing modes:
//
//	all_directed  — N*(N-1) independent directed flows; default
//	bidir_paired  — N*(N-1)/2 physical pairs, each yielding a forward and a
//	                reverse path on the same physical links; recommended for
//	                AI all-reduce workloads
//
// Disjointness levels:
//
//	none   — no isolation; all pairs computed independently
//	link   — no shared link edges across the workload's paths
//	node   — no shared transit nodes (implies link as well)
//	srlg   — no shared SRLG groups
package pathengine

import (
	"fmt"
	"strings"
	"time"

	"github.com/jalapeno/syd/internal/allocation"
	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/pkg/apitypes"
)

// ComputeResult is the output of Compute.
type ComputeResult struct {
	Paths      []graph.Path
	FailedPairs []string // human-readable descriptions of pairs that could not be routed
}

// Compute is the main entry point. It resolves endpoints, computes all-pairs
// paths, and registers them with the allocation table.
func Compute(
	g *graph.Graph,
	table *allocation.Table,
	req apitypes.PathRequest,
	disjointness string,
	sharing string,
) (*ComputeResult, error) {
	// --- 1. Build PathConstraints ----------------------------------------
	constraints := buildConstraints(req, disjointness)

	// --- 2. Resolve endpoints to Node vertices ---------------------------
	resolved, errs := ResolveEndpoints(g, req.Endpoints)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("endpoint resolution failed: %s", strings.Join(msgs, "; "))
	}
	if len(resolved) < 2 {
		return nil, fmt.Errorf("at least 2 endpoints must resolve successfully")
	}

	// --- 3. Resolve pairing mode -----------------------------------------
	mode := PairingMode(req.PairingMode)
	if mode != PairingBiDirPaired {
		mode = PairingAllDirected // safe default
	}

	// --- 4. Enumerate pairs and compute paths ----------------------------
	pairs := EnumeratePairs(resolved, mode)
	prefix := fmt.Sprintf("%s-%d", req.WorkloadID, time.Now().UnixNano())
	pairResults := ComputeAllPairs(g, pairs, constraints, req.WorkloadID, prefix, mode)

	// --- 5. Separate successes from failures -----------------------------
	result := &ComputeResult{}
	var successPaths []graph.Path

	for _, pr := range pairResults {
		if pr.Err != nil {
			result.FailedPairs = append(result.FailedPairs, pr.Err.Error())
			continue
		}
		successPaths = append(successPaths, pr.Pair)
	}

	// --- 6. Allocate successful paths in the state table -----------------
	sharingPolicy := graph.SharingExclusive
	if sharing == string(graph.SharingAllowed) {
		sharingPolicy = graph.SharingAllowed
	}

	wl := &allocation.WorkloadAllocation{
		WorkloadID:   req.WorkloadID,
		TopologyID:   req.TopologyID,
		Sharing:      sharingPolicy,
		Disjointness: constraints.Disjointness,
	}
	if req.LeaseDuration > 0 {
		d := time.Duration(req.LeaseDuration) * time.Second
		wl.LeaseDuration = d
		wl.LeaseExpires = time.Now().Add(d)
	}

	pathIDs := make([]string, len(successPaths))
	for i, p := range successPaths {
		cp := p // copy for pointer registration
		table.RegisterPath(&cp)
		pathIDs[i] = cp.ID
	}

	if len(pathIDs) > 0 {
		if err := table.AllocatePaths(wl, pathIDs); err != nil {
			return nil, fmt.Errorf("allocation failed: %w", err)
		}
	}

	result.Paths = successPaths
	return result, nil
}

// buildConstraints converts a PathRequest into a graph.PathConstraints.
func buildConstraints(req apitypes.PathRequest, disjointness string) graph.PathConstraints {
	c := graph.PathConstraints{
		Disjointness: graph.DisjointnessLevel(disjointness),
		Sharing:      graph.SharingExclusive,
	}
	if req.Sharing == string(graph.SharingAllowed) {
		c.Sharing = graph.SharingAllowed
	}
	if req.Constraints != nil {
		c.MinBandwidthBPS = req.Constraints.MinBandwidthBPS
		c.MaxLatencyUS = req.Constraints.MaxLatencyUS
		c.Color = req.Constraints.Color
		c.AlgoID = req.Constraints.AlgoID
		c.ExcludeGroup = req.Constraints.ExcludeGroup
	}
	c.TenantID = req.TenantID
	c.SegmentListMode = req.SegmentListMode
	return c
}
