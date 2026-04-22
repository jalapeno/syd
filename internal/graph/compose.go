package graph

// Compose creates a new Graph named id by merging the provided source graphs
// into a single unified view.
//
// # Stitch logic
//
// BGP session edges in peer graphs use LocalBGPID (a BGP router-ID such as
// "10.0.0.6") as SrcID. IGP nodes in underlay graphs are keyed by IS-IS
// system ID ("0000.0000.0006"). These two IDs refer to the same physical
// router but don't match. The stitch resolves the discrepancy using
// graph.Node.RouterID — every IGP-derived node stores its BGP router-ID there,
// providing the cross-namespace join key.
//
// # Duplicate peer deduplication
//
// Nodes that appear in both the IGP domain and as eBGP peers produce two
// vertices: one IGP node (IS-IS system ID key) and one NSExternalBGP peer
// node (peer:<RID>_<IP> key). When composing, we detect these duplicates by
// checking if the peer node's RouterID already maps to an IGP node. Duplicate
// peer vertices are skipped; BGPReachabilityEdges that originated from the
// duplicate peer vertex are rewritten to use the IGP node ID as SrcID so
// reachability information is preserved.
//
// # Algorithm
//
//  1. Build a RouterID → nodeID secondary index from all VTNode vertices
//     across all source graphs (IGP nodes only; NSExternalBGP nodes excluded).
//  2. Build a peerVertexID → igpNodeID dedup index for external BGP peer nodes
//     whose RouterID already appears in the RouterID index.
//  3. Pass 1 — copy all vertices from all sources in order, skipping
//     NSExternalBGP nodes that are covered by an IGP node (dedup index).
//  4. Pass 2 — copy all edges from all sources in order:
//     - ETBGPSession edges have their SrcID rewritten from LocalBGPID to the
//       matched IGP node ID; dropped if the local end cannot be resolved.
//     - ETBGPReachability edges have their SrcID rewritten from the peer
//       vertex ID to the IGP node ID when the peer was a dedup (so reachability
//       edges remain connected even after the peer vertex is dropped).
//     - All other edge types are copied verbatim; failures (missing vertex) are
//       silently skipped.
//
// # Staleness
//
// The composed graph is a point-in-time snapshot. Subsequent BMP updates to
// the source graphs are not reflected. Call Compose again (and PUT the result
// in the Store) to refresh.
func Compose(id string, sources ...*Graph) *Graph {
	out := New(id)

	// --- build RouterID → nodeID index (IGP nodes only) -------------------
	// RouterID is the BGP loopback IP stored on IGP-derived Node vertices.
	// LocalBGPID in peer session messages equals this value, enabling the
	// stitch from the peer graph's SrcID onto the IGP graph's node ID.
	// NSExternalBGP nodes are excluded so a peer node for 10.0.0.6 doesn't
	// shadow the IGP node for 0000.0000.0006 in the index.
	routerIDToNodeID := make(map[string]string)
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			if n, ok := v.(*Node); ok && n.RouterID != "" && n.Subtype != NSExternalBGP {
				// Only store IGP-derived nodes: their ID is an IS-IS system ID,
				// not an IP address. Skip if ID == RouterID (that would be a
				// BGP-only stub, not an IGP node).
				if n.ID != n.RouterID {
					routerIDToNodeID[n.RouterID] = n.ID
				}
			}
		}
		src.mu.RUnlock()
	}

	// --- build peerVertexID → igpNodeID dedup index -----------------------
	// Any NSExternalBGP peer node whose RouterID maps to a known IGP node is
	// a duplicate — the same physical router appears in both the IGP and BGP
	// peer graphs. We skip the peer vertex and rewrite edges that reference it.
	peerIDToIGPID := make(map[string]string)
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			if n, ok := v.(*Node); ok && n.Subtype == NSExternalBGP && n.RouterID != "" {
				if igpID, exists := routerIDToNodeID[n.RouterID]; exists {
					peerIDToIGPID[n.ID] = igpID
				}
			}
		}
		src.mu.RUnlock()
	}

	// --- pass 1: copy all vertices, skipping duplicate peer nodes ----------
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			if n, ok := v.(*Node); ok && n.Subtype == NSExternalBGP {
				if _, isDup := peerIDToIGPID[n.ID]; isDup {
					continue // IGP node already represents this router
				}
			}
			_ = out.AddVertex(v)
		}
		src.mu.RUnlock()
	}

	// --- pass 2: copy all edges, stitching BGP sessions and dedup peers ----
	for _, src := range sources {
		src.mu.RLock()
		for _, e := range src.edges {
			switch typed := e.(type) {
			case *BGPSessionEdge:
				// Rewrite SrcID from LocalBGPID to the canonical IGP node ID.
				igpID, ok := routerIDToNodeID[typed.SrcID]
				if !ok {
					// Local end is not a known IGP node — drop this edge.
					continue
				}
				rewritten := *typed
				rewritten.SrcID = igpID
				// Rekey the edge so it doesn't collide if the same session
				// appears in multiple sources.
				rewritten.ID = "bgpsess:" + igpID + ":" + typed.RemoteIP
				_ = out.AddEdge(&rewritten)
			case *BGPReachabilityEdge:
				// If the source peer vertex was deduplicated onto an IGP node,
				// rewrite SrcID so the reachability edge remains connected.
				if igpID, isDup := peerIDToIGPID[typed.SrcID]; isDup {
					rewritten := *typed
					rewritten.SrcID = igpID
					rewritten.ID = "bgpreach:" + igpID + ":" + typed.DstID
					_ = out.AddEdge(&rewritten)
				} else {
					_ = out.AddEdge(e)
				}
			default:
				_ = out.AddEdge(e)
			}
		}
		src.mu.RUnlock()
	}

	return out
}
