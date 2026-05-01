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
// # Duplicate vertex deduplication
//
// Two kinds of stub vertices can duplicate an IGP node in a composite graph:
//
//  1. NSExternalBGP peer nodes — created when an eBGP session is seen from a
//     router that is also in the IGP domain. Their RouterID matches an IGP
//     node's RouterID (e.g. peer:10.0.0.6 duplicates xrd06).
//
//  2. Nexthop stub nodes — created by the unicast prefix handler's fallback
//     path when no peer spec is available. Their ID IS the nexthop IP address
//     (e.g. "10.0.0.6"), which equals the RouterID of the corresponding IGP
//     node.
//
// Both cases are detected by checking the routerIDToNodeID index: if a
// vertex's own ID appears as a RouterID key, or if its RouterID maps to an
// IGP node, the vertex is a duplicate. Duplicate vertices are skipped in
// pass 1; BGPReachabilityEdges that reference them have their SrcID rewritten
// to the IGP node ID so reachability is preserved.
//
// # BGP best-path selection (one edge per prefix)
//
// The BMP stream delivers prefix advertisements from every BGP speaker that
// has the prefix in its RIB, including re-advertisers at every tier. Without
// filtering, a prefix originated by DC46 arrives with edges from DC42/43,
// DC40/41, xrd01/02, etc. — creating a dense star rather than a clean origin
// attachment.
//
// The pre-pass selects the single best BGPReachabilityEdge per prefix using
// a simplified BGP decision process:
//
//  1. Shortest AS_PATH (fewer hops = closer to origin).
//  2. Highest LocalPref (tiebreak for iBGP-reflected paths).
//  3. Lowest MED (final tiebreak).
//
// OwnershipEdges that connect a prefix to a nexthop stub (pfxown:pfx:…:nh:…)
// are also suppressed for any prefix that has a BGPReachabilityEdge winner —
// the stub nexthop model is only needed as a fallback when no eBGP peer is
// known. After suppression, any nh: vertices left without edges are removed.
//
// The full RIB is preserved in the source prefix graphs; filtering is applied
// only to the composed view.
//
// # Algorithm
//
//  1. Build RouterID → nodeID index (IGP nodes only).
//  2. Build dupVertexID → igpNodeID dedup map.
//  3. Pre-pass — scan all source edges to:
//     a. Identify nh: stubs that have at least one edge (nhWithEdges).
//     b. Select the best BGPReachabilityEdge per prefix (bestReach), applying
//        SrcID rewrites for deduped peer nodes.
//  4. Pass 1 — copy vertices, skipping duplicates and bare stubs.
//  5. Pass 2 — copy edges:
//     - ETBGPSession: IS-IS or peer-vertex stitching; drop if unresolvable.
//     - ETBGPReachability: skipped here (handled by pre-pass + pass 3).
//     - ETOwnership (pfx→nh): suppressed when prefix has a bestReach winner.
//     - All other types: copy verbatim.
//  6. Pass 3 — insert one winning BGPReachabilityEdge per prefix.
//  7. Pass 4 — remove nh: vertices that ended up with no edges in out.
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
	// NSExternalBGP nodes are excluded so a peer vertex for 10.0.0.6 doesn't
	// shadow the IGP node for 0000.0000.0006 in the index.
	routerIDToNodeID := make(map[string]string)
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			if n, ok := v.(*Node); ok && n.RouterID != "" && n.Subtype != NSExternalBGP {
				// Skip stubs where ID == RouterID (IP-addressed, not an IGP node).
				if n.ID != n.RouterID {
					routerIDToNodeID[n.RouterID] = n.ID
				}
			}
		}
		src.mu.RUnlock()
	}

	// --- build dupVertexID → igpNodeID dedup map --------------------------
	// Covers two cases:
	//   a) NSExternalBGP peer node whose RouterID maps to a known IGP node.
	//   b) Nexthop stub node whose plain-IP ID equals a known RouterID.
	dupVertexToIGPID := make(map[string]string)
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			n, ok := v.(*Node)
			if !ok {
				continue
			}
			// Case (a): NSExternalBGP peer whose RouterID is a known IGP RouterID.
			if n.Subtype == NSExternalBGP && n.RouterID != "" {
				if igpID, exists := routerIDToNodeID[n.RouterID]; exists {
					dupVertexToIGPID[n.ID] = igpID
				}
				continue
			}
			// Case (b): stub node whose own ID is a known RouterID (plain IP stub).
			if igpID, exists := routerIDToNodeID[n.ID]; exists {
				dupVertexToIGPID[n.ID] = igpID
			}
		}
		src.mu.RUnlock()
	}

	// --- pre-pass: nhWithEdges + bestReach ---------------------------------
	// Combine two scans into one loop for efficiency.
	//
	// nhWithEdges: nh: stubs that have at least one edge in a source graph.
	// Stubs with NO source edges are orphans from the startup race and are
	// dropped in pass 1.
	//
	// bestReach: best BGPReachabilityEdge per prefix, selected by the BGP
	// decision process (shortest AS_PATH → highest LocalPref → lowest MED).
	// SrcID rewrites for deduped peer nodes are applied here so that the
	// OwnershipEdge suppression check below can use the same pfxID key.
	nhWithEdges := make(map[string]struct{})
	bestReach := make(map[string]*BGPReachabilityEdge) // pfxID → best candidate
	for _, src := range sources {
		src.mu.RLock()
		for _, e := range src.edges {
			// Track nh: destinations (for orphan filtering).
			if dst := e.GetDstID(); len(dst) > 3 && dst[:3] == "nh:" {
				nhWithEdges[dst] = struct{}{}
			}
			// Accumulate best BGPReachabilityEdge per prefix.
			typed, ok := e.(*BGPReachabilityEdge)
			if !ok {
				continue
			}
			candidate := typed
			if igpID, isDup := dupVertexToIGPID[typed.SrcID]; isDup {
				rewritten := *typed
				rewritten.SrcID = igpID
				rewritten.ID = "bgpreach:" + igpID + ":" + typed.DstID
				candidate = &rewritten
			}
			if existing, ok := bestReach[candidate.DstID]; !ok || betterBGPPath(candidate, existing) {
				bestReach[candidate.DstID] = candidate
			}
		}
		src.mu.RUnlock()
	}

	// --- pass 1: copy all vertices, skipping duplicates and bare stubs -----
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			if _, isDup := dupVertexToIGPID[v.GetID()]; isDup {
				continue
			}
			// Drop plain stub nodes — Node vertices with no RouterID and no
			// Subtype were auto-created by UpsertBGPSession (LocalBGPID stubs
			// for the local router) or EnsureNode. They add noise without
			// contributing connectivity in the composed graph.
			//
			// Exception A: nh: nexthop stubs with at least one live source edge
			// ARE kept — they are the target of the ResolvePrefix ownership
			// fallback for prefixes with no known eBGP peer.
			//
			// Exception B: NSExternalBGP peer nodes always have a Subtype set,
			// so they are never matched by this filter.
			if n, ok := v.(*Node); ok && n.RouterID == "" && string(n.Subtype) == "" {
				id := n.ID
				if len(id) >= 3 && id[:3] == "nh:" {
					if _, hasEdge := nhWithEdges[id]; !hasEdge {
						continue // orphaned nh: stub — drop
					}
				} else {
					continue // plain IP stub — always drop
				}
			}
			_ = out.AddVertex(v)
		}
		src.mu.RUnlock()
	}

	// --- pass 2: copy edges, stitching sessions and suppressing overridden nh: edges ---
	for _, src := range sources {
		src.mu.RLock()
		for _, e := range src.edges {
			switch typed := e.(type) {
			case *BGPSessionEdge:
				// Rewrite SrcID to the canonical vertex ID for the local end.
				//
				// Strategy 1 — IS-IS stitching: rewrite LocalBGPID to the
				// IS-IS system-ID-based node ID via the routerIDToNodeID index.
				//
				// Strategy 2 — peer-vertex stitching: if peer:<LocalBGPID>
				// exists in the composed graph, rewrite SrcID to that vertex.
				// Handles DC-only BGP routers (tier-2/1/0) that run BMP but
				// have no IGP adjacency.
				//
				// Edges that match neither strategy are dropped.
				srcID := typed.SrcID
				if igpID, ok := routerIDToNodeID[srcID]; ok {
					rewritten := *typed
					rewritten.SrcID = igpID
					rewritten.ID = "bgpsess:" + igpID + ":" + typed.RemoteIP
					_ = out.AddEdge(&rewritten)
					continue
				}
				peerID := "peer:" + srcID
				if out.GetVertex(peerID) != nil {
					rewritten := *typed
					rewritten.SrcID = peerID
					rewritten.ID = "bgpsess:" + peerID + ":" + typed.RemoteIP
					_ = out.AddEdge(&rewritten)
					continue
				}
				// Unresolvable — drop.
			case *BGPReachabilityEdge:
				// Best-path winner selected in pre-pass; inserted in pass 3.
				// Skip all candidates here to avoid duplicate insertion.
				_ = typed
			case *OwnershipEdge:
				// Suppress prefix→nexthop stub ownership edges for any prefix
				// that has a BGPReachabilityEdge winner. When the origin peer
				// is known, the nh: stub fallback model is unnecessary and
				// creates extra connections in the composed view.
				//
				// Interface→node ownership edges (SrcID = "iface:…") are
				// always kept — they model structural containment, not prefix
				// reachability.
				if len(typed.SrcID) >= 4 && typed.SrcID[:4] == "pfx:" {
					if _, hasBest := bestReach[typed.SrcID]; hasBest {
						continue
					}
				}
				_ = out.AddEdge(e)
			default:
				_ = out.AddEdge(e)
			}
		}
		src.mu.RUnlock()
	}

	// --- pass 3: insert the single best BGPReachabilityEdge per prefix ------
	for _, e := range bestReach {
		_ = out.AddEdge(e)
	}

	// --- pass 4: remove nh: vertices that have no edges in the composed graph ---
	// An nh: vertex may have been included in pass 1 (it had source edges) but
	// all its pfx→nh OwnershipEdges were suppressed in pass 2 because the
	// prefixes got BGPReachabilityEdge winners. Without edges, the vertex is an
	// invisible orphan — remove it to keep the graph clean.
	nhEdgeDsts := make(map[string]struct{})
	for _, e := range out.AllEdges() {
		if dst := e.GetDstID(); len(dst) > 3 && dst[:3] == "nh:" {
			nhEdgeDsts[dst] = struct{}{}
		}
	}
	for _, v := range out.AllVertices() {
		if id := v.GetID(); len(id) > 3 && id[:3] == "nh:" {
			if _, hasEdge := nhEdgeDsts[id]; !hasEdge {
				out.RemoveVertex(id)
			}
		}
	}

	return out
}

// betterBGPPath returns true if candidate should replace existing as the
// selected best-path edge for a prefix vertex in the composed graph.
// Implements a simplified BGP decision process:
//
//  1. Shorter AS_PATH — fewer hops means the advertising peer is closer to
//     the prefix origin. This is the primary differentiator when multiple
//     routers re-advertise the same prefix across BGP tiers.
//
//  2. Higher LocalPref — meaningful for iBGP-reflected paths where the
//     originating router's policy has already set a preference.
//
//  3. Lower MED — final tiebreak.
func betterBGPPath(candidate, existing *BGPReachabilityEdge) bool {
	cl, el := len(candidate.ASPath), len(existing.ASPath)
	if cl != el {
		return cl < el
	}
	if candidate.LocalPref != existing.LocalPref {
		return candidate.LocalPref > existing.LocalPref
	}
	return candidate.MED < existing.MED
}
