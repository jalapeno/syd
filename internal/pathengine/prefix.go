package pathengine

import (
	"fmt"

	"github.com/jalapeno/syd/internal/graph"
)

// PrefixResolution holds the result of resolving a CIDR prefix to the transit
// node that serves as its path-computation entry point.
type PrefixResolution struct {
	PrefixVertexID string   // e.g. "pfx:10.0.0.0/8"
	NodeID         string   // node ID to use as SPF src/dst
	BGPNexthop     string   // BGP NEXT_HOP, populated when adjacent edge is a BGPReachabilityEdge
	LocalPref      uint32   // BGP LocalPref, when available
	ASPath         []uint32 // BGP AS_PATH, when available
}

// ResolvePrefix finds the transit node that serves as the path-computation
// entry point for the given CIDR prefix in graph g. Any edge type adjacent to
// the prefix vertex is followed — no assumption is made about routing protocol.
//
// For each adjacent node, resolveToTransitNode walks inbound edges until it
// finds a node with IGP adjacency edges (a node that participates in link-state
// routing). Boundary nodes such as external BGP peers are walked through their
// inbound BGPSession edge to the connected border router.
//
// BGP path attributes (LocalPref, ASPath, NextHop) are extracted when the
// adjacent edge is a BGPReachabilityEdge and used to prefer better-quality paths.
func ResolvePrefix(g *graph.Graph, cidr string) (*PrefixResolution, error) {
	pfxID, err := findPrefixVertex(g, cidr)
	if err != nil {
		return nil, err
	}

	var best *PrefixResolution
	visited := make(map[string]struct{})

	// Inbound edges: node → prefix (e.g. BGPReachabilityEdge: peer announces prefix).
	for _, e := range g.InEdges(pfxID) {
		srcID := e.GetSrcID()
		sv := g.GetVertex(srcID)
		if sv == nil || sv.GetType() != graph.VTNode {
			continue
		}
		nodeID, err := resolveToTransitNode(g, srcID, pfxID, visited)
		if err != nil {
			continue
		}
		candidate := &PrefixResolution{
			PrefixVertexID: pfxID,
			NodeID:         nodeID,
		}
		if reach, ok := e.(*graph.BGPReachabilityEdge); ok {
			candidate.BGPNexthop = reach.NextHop
			candidate.LocalPref = reach.LocalPref
			candidate.ASPath = reach.ASPath
		}
		if best == nil || isBetterPath(candidate, best) {
			best = candidate
		}
	}
	if best != nil {
		return best, nil
	}

	// Outbound edges: prefix → node (e.g. OwnershipEdge: prefix owned by node).
	for _, e := range g.OutEdges(pfxID) {
		dstID := e.GetDstID()
		dv := g.GetVertex(dstID)
		if dv == nil || dv.GetType() != graph.VTNode {
			continue
		}
		nodeID, err := resolveToTransitNode(g, dstID, pfxID, visited)
		if err != nil {
			continue
		}
		return &PrefixResolution{PrefixVertexID: pfxID, NodeID: nodeID}, nil
	}

	return nil, fmt.Errorf("prefix %q has no reachable transit node in the topology", cidr)
}

// findPrefixVertex returns the vertex ID for the given CIDR string. It first
// tries the canonical key "pfx:<cidr>", then falls back to a linear scan of
// all VTPrefix vertices comparing the Prefix.Prefix field. Returns an error if
// no matching vertex is found.
func findPrefixVertex(g *graph.Graph, cidr string) (string, error) {
	// Fast path: canonical key.
	if v := g.GetVertex("pfx:" + cidr); v != nil {
		return v.GetID(), nil
	}
	// Slow path: linear scan (handles non-canonical CIDR formatting).
	for _, v := range g.VerticesByType(graph.VTPrefix) {
		if p, ok := v.(*graph.Prefix); ok && p.Prefix == cidr {
			return v.GetID(), nil
		}
	}
	return "", fmt.Errorf("prefix %q not found in topology", cidr)
}

// resolveToTransitNode walks from nodeID toward the nearest transit node.
// A transit node is any node that has ETIGPAdjacency edges — it participates
// in link-state path computation. Boundary nodes (external BGP peers, nexthop
// stubs) that only have session or reachability edges are walked through their
// inbound edges to find the connected transit node.
//
// skipID prevents re-entering the prefix vertex that initiated the walk.
func resolveToTransitNode(g *graph.Graph, nodeID, skipID string, visited map[string]struct{}) (string, error) {
	if _, seen := visited[nodeID]; seen {
		return "", fmt.Errorf("cycle at %q", nodeID)
	}
	visited[nodeID] = struct{}{}

	if g.GetVertex(nodeID) == nil || g.GetVertex(nodeID).GetType() != graph.VTNode {
		return "", fmt.Errorf("vertex %q is not a node", nodeID)
	}

	// Any IGP adjacency edge marks this as a transit node.
	for _, e := range g.OutEdges(nodeID) {
		if e.GetType() == graph.ETIGPAdjacency {
			return nodeID, nil
		}
	}
	for _, e := range g.InEdges(nodeID) {
		if e.GetType() == graph.ETIGPAdjacency {
			return nodeID, nil
		}
	}

	// No IGP adjacencies — follow inbound edges from other nodes to find one.
	// This handles boundary nodes: peer:X has a BGPSessionEdge InEdge from the
	// border router, which itself has IGP adjacencies.
	for _, e := range g.InEdges(nodeID) {
		srcID := e.GetSrcID()
		if srcID == skipID || srcID == nodeID {
			continue
		}
		if sv := g.GetVertex(srcID); sv == nil || sv.GetType() != graph.VTNode {
			continue
		}
		if id, err := resolveToTransitNode(g, srcID, skipID, visited); err == nil {
			return id, nil
		}
	}

	return "", fmt.Errorf("no transit node reachable from %q", nodeID)
}

// isBetterPath returns true if candidate is a better BGP path than current.
// Selection criteria: higher LocalPref wins; on tie, shorter ASPath wins.
func isBetterPath(candidate, current *PrefixResolution) bool {
	if candidate.LocalPref != current.LocalPref {
		return candidate.LocalPref > current.LocalPref
	}
	return len(candidate.ASPath) < len(current.ASPath)
}
