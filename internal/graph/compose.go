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
// # Algorithm
//
//  1. Build a RouterID → nodeID secondary index from all VTNode vertices
//     across all source graphs (only nodes with RouterID != "" contribute).
//  2. Pass 1 — copy all vertices from all sources in order.
//  3. Pass 2 — copy all edges from all sources in order; ETBGPSession edges
//     have their SrcID rewritten from LocalBGPID to the matched IGP node ID
//     and are dropped if the local end cannot be resolved.
//     All other edge types are copied verbatim; failures (missing vertex) are
//     silently skipped.
//
// # Staleness
//
// The composed graph is a point-in-time snapshot. Subsequent BMP updates to
// the source graphs are not reflected. Call Compose again (and PUT the result
// in the Store) to refresh.
func Compose(id string, sources ...*Graph) *Graph {
	out := New(id)

	// --- build RouterID → nodeID index -----------------------------------
	// RouterID is the BGP loopback IP stored on IGP-derived Node vertices.
	// LocalBGPID in peer session messages equals this value, enabling the
	// stitch from the peer graph's SrcID onto the IGP graph's node ID.
	routerIDToNodeID := make(map[string]string)
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			if n, ok := v.(*Node); ok && n.RouterID != "" {
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

	// --- pass 1: copy all vertices ---------------------------------------
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			_ = out.AddVertex(v)
		}
		src.mu.RUnlock()
	}

	// --- pass 2: copy all edges, stitching BGP sessions -----------------
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
			default:
				_ = out.AddEdge(e)
			}
		}
		src.mu.RUnlock()
	}

	return out
}
