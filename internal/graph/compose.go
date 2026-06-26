package graph

// Compose creates a new Graph named id by merging the provided source graphs
// into a single unified view.
//
// # Stitch logic
//
// BGP session edges in peer graphs use LocalBGPID (a BGP router-ID such as
// "10.0.0.6") as SrcID. IGP nodes in underlay graphs are keyed by IS-IS
// system ID ("0000.0000.0006"). These two IDs refer to the same physical
// router but don't match. The stitch resolves the discrepancy using two
// fields on graph.Node:
//
//   - RouterID (TLV 1028 / TLV 1029): the primary join key. For IPv4 BMP
//     sessions gobmp stores TLV 1028 (IPv4 BGP router-ID) here, which matches
//     LocalBGPID directly.
//   - BGPRouterID: the secondary join key, always the 4-byte IPv4 BGP OPEN
//     router-ID (TLV 1028). For IPv6 BMP sessions gobmp stores TLV 1029 (IPv6)
//     in RouterID, so LocalBGPID never matches it. BGPRouterID is derived at
//     ingestion time from PeerStateChange.LocalBGPID keyed by BMP RouterHash
//     and provides the cross-namespace join key for those sessions.
//
// Both RouterID and BGPRouterID are indexed in routerIDToNodeID; the BGPSession
// stitch and dupVertex detection code uses the same index for both.
//
// # Duplicate vertex deduplication
//
// Two kinds of stub vertices can duplicate an IGP node in a composite graph:
//
//  1. NSExternalBGP peer nodes — created when an eBGP session is seen from a
//     router that is also in the IGP domain. Their RouterID matches an IGP
//     node's RouterID (e.g. peer:10.0.0.1 duplicates xrd01).
//
//  2. Nexthop stub nodes — created by the unicast prefix handler's fallback
//     path when no peer spec is available. Their ID IS the nexthop IP address
//     (e.g. "10.0.0.6"), which equals the RouterID of the corresponding IGP
//     node.
//
// Both cases are detected by checking the routerIDToNodeID index. Duplicate
// vertices are skipped in pass 1; BGPReachabilityEdges that reference them
// have their SrcID rewritten to the IGP node ID.
//
// # BGP best-path selection (one edge per prefix per quality tier)
//
// The BMP stream delivers prefix advertisements from every BGP speaker that
// has the prefix in its RIB. Without filtering, a prefix originated by DC46
// arrives with edges from DC42/43, DC40/41, xrd01/02, etc.
//
// The pre-pass groups BGPReachabilityEdge candidates per prefix and selects
// the minimum-quality tier:
//
//  1. Shortest AS_PATH — eliminates re-advertisers at higher tiers while
//     retaining all peers at the same tier (preserves multi-homed prefixes).
//  2. Highest LocalPref — tiebreak.
//  3. Lowest MED — final tiebreak.
//
// All edges that survive all tiebreaks are inserted, so two ASBRs advertising
// an internet prefix at equal quality both appear.
//
// # Dedup-rewrite handling and nh: stitching
//
// Some BGPReachabilityEdges are "dedup-rewritten": their SrcID was originally
// a peer vertex that duplicates an IGP node (e.g. peer:10.0.0.1 is an eBGP
// peer of the DC fabric but also xrd01 in the IS-IS domain). After rewrite the
// edge points to the IGP node ID.
//
// These rewritten edges represent ASBR re-advertisement to the DC fabric, not
// true prefix origination. When ALL best-path winners for a prefix are
// dedup-rewritten, the real origin is more likely to be the iBGP source visible
// through the unicast prefix handler's nexthop fallback path:
//
//   pfx:0.0.0.0/0 → nh:10.0.0.9  (from xrd01's iBGP session with xrd09)
//
// If nh:X can be stitched to an IGP node (X is in routerIDToNodeID), the
// OwnershipEdge is rewritten to point directly to that node and the
// dedup-rewritten BGPReachabilityEdges are dropped. The stitched edge thus
// correctly identifies xrd09 as the default-route originator rather than
// xrd01/02 (the ASBR re-advertisers to the DC fabric).
//
// When no stitchable OwnershipEdge is available, the dedup-rewritten edges
// are kept as a fallback.
//
// OwnershipEdges for prefixes that have genuine (non-dedup) BGPReachabilityEdge
// winners are always suppressed.
//
// # Algorithm
//
//  1. Build RouterID → nodeID index (IGP nodes only).
//  2. Build dupVertexID → igpNodeID dedup map.
//  3. Pre-pass — scan all source edges to:
//     a. Identify nh: stubs with at least one edge (nhWithEdges).
//     b. Select the best BGPReachabilityEdge group per prefix (bestReach),
//        tracking whether all winners are dedup-rewritten (allDedup).
//  4. Pass 1 — copy vertices, skipping duplicates and bare stubs.
//     The Protocol field guards IGP nodes: IS-IS nodes always have
//     Protocol set, so they are never filtered as plain-IP stubs.
//  5. Pass 2 — copy edges:
//     - ETBGPSession: IS-IS or peer-vertex stitching; drop if unresolvable.
//     - ETBGPReachability: skipped (handled by pre-pass + pass 3).
//     - ETOwnership (pfx→nh): suppressed if prefix has non-dedup winner;
//       otherwise stitch nh:X → IGP node when X is in routerIDToNodeID.
//     - All other types: copy verbatim.
//  6. Pass 3 — insert winning BGPReachabilityEdges:
//     allDedup groups are skipped when a stitched OwnershipEdge was emitted;
//     otherwise inserted as fallback.
//  7. Pass 4 — remove nh: vertices left with no edges.
//
// # Staleness
//
// The composed graph is a point-in-time snapshot. Subsequent BMP updates to
// the source graphs are not reflected. Call Compose again (and PUT the result
// in the Store) to refresh.
func Compose(id string, sources ...*Graph) *Graph {
	out := New(id)

	// --- build RouterID → nodeID index (IGP nodes only) -------------------
	routerIDToNodeID := make(map[string]string)
	// routerHashToNodeID provides a fallback stitch key for BGP sessions where
	// LocalBGPID (always IPv4 format) doesn't match Node.RouterID (which may be
	// IPv6 when the BMP session is IPv6, or empty when TLV 1028 is absent).
	// Node.BMPRouterHash == BGPSessionEdge.BMPRouterHash for the same physical
	// router because both originate from the same BMP session.
	routerHashToNodeID := make(map[string]string)
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			n, ok := v.(*Node)
			if !ok || n.Subtype == NSExternalBGP {
				continue
			}
			if n.RouterID != "" && n.ID != n.RouterID {
				routerIDToNodeID[n.RouterID] = n.ID
			}
			// BGPRouterID carries TLV 1028 (IPv4 BGP OPEN router-ID) derived
			// from PeerStateChange.LocalBGPID. For IPv6 BMP sessions gobmp
			// stores TLV 1029 (IPv6) in RouterID, so LocalBGPID never matches
			// routerIDToNodeID. Indexing BGPRouterID separately lets the stitch
			// below succeed for both IPv4 and IPv6 BMP sessions.
			if n.BGPRouterID != "" {
				routerIDToNodeID[n.BGPRouterID] = n.ID
			}
			if n.BMPRouterHash != "" {
				routerHashToNodeID[n.BMPRouterHash] = n.ID
			}
		}
		src.mu.RUnlock()
	}

	// --- build dupVertexID → igpNodeID dedup map --------------------------
	dupVertexToIGPID := make(map[string]string)
	for _, src := range sources {
		src.mu.RLock()
		for _, v := range src.vertices {
			n, ok := v.(*Node)
			if !ok {
				continue
			}
			if n.Subtype == NSExternalBGP && n.RouterID != "" {
				if igpID, exists := routerIDToNodeID[n.RouterID]; exists {
					dupVertexToIGPID[n.ID] = igpID
				}
				continue
			}
			if igpID, exists := routerIDToNodeID[n.ID]; exists {
				dupVertexToIGPID[n.ID] = igpID
			}
		}
		src.mu.RUnlock()
	}

	// --- pre-pass: nhWithEdges + bestReach + pfxWithEdges -------------------
	nhWithEdges := make(map[string]struct{})
	pfxWithEdges := make(map[string]struct{}) // pfxIDs that have at least one edge
	bestReach := make(map[string]*reachGroup) // pfxID → group
	for _, src := range sources {
		src.mu.RLock()
		for _, e := range src.edges {
			if dst := e.GetDstID(); len(dst) > 3 && dst[:3] == "nh:" {
				nhWithEdges[dst] = struct{}{}
			}
			// Track prefix vertices that have at least one edge (BGPReachability
			// or OwnershipEdge). Bare prefix vertices left by withdrawn routes
			// are excluded from the composed graph to prevent resolution errors.
			switch src := e.GetSrcID(); {
			case len(e.GetDstID()) > 4 && e.GetDstID()[:4] == "pfx:":
				pfxWithEdges[e.GetDstID()] = struct{}{}
			case len(src) > 4 && src[:4] == "pfx:":
				pfxWithEdges[src] = struct{}{}
			}
			typed, ok := e.(*BGPReachabilityEdge)
			if !ok {
				continue
			}
			isDup := false
			candidate := typed
			if igpID, ok := dupVertexToIGPID[typed.SrcID]; ok {
				rewritten := *typed
				rewritten.SrcID = igpID
				rewritten.ID = "bgpreach:" + igpID + ":" + typed.DstID
				candidate = &rewritten
				isDup = true
			}
			cq := bgpQuality(candidate)
			group, exists := bestReach[candidate.DstID]
			if !exists {
				bestReach[candidate.DstID] = &reachGroup{
					quality:  cq,
					edges:    map[string]*BGPReachabilityEdge{candidate.ID: candidate},
					allDedup: isDup,
				}
				continue
			}
			cmp := cq.compare(group.quality)
			switch {
			case cmp < 0:
				// Strictly better — replace entire group.
				group.quality = cq
				group.edges = map[string]*BGPReachabilityEdge{candidate.ID: candidate}
				group.allDedup = isDup
			case cmp == 0:
				// Tied — add to group (dedup by edge ID).
				group.edges[candidate.ID] = candidate
				if !isDup {
					group.allDedup = false
				}
			}
			// Worse — discard.
		}
		src.mu.RUnlock()
	}

	// --- nh: stitch pre-check: which prefixes have at least one stitchable nh: OwnershipEdge ---
	// When a prefix has multiple pfx→nh: OwnershipEdges (e.g. one to nh:10.0.0.9 which
	// stitches fine, and others to interface IPs like nh:10.2.1.8 which don't match any
	// RouterID), the stitchable edge is emitted rewritten and the un-stitchable ones must
	// be suppressed. Without this, pass 2 iterates edges in arbitrary order and emits the
	// un-stitchable edges as-is, leaving orphaned nh: vertices alongside the stitched node.
	stitchablePfx := make(map[string]struct{})
	for _, src := range sources {
		src.mu.RLock()
		for _, e := range src.edges {
			ow, ok := e.(*OwnershipEdge)
			if !ok || len(ow.SrcID) < 4 || ow.SrcID[:4] != "pfx:" {
				continue
			}
			if len(ow.DstID) <= 3 || ow.DstID[:3] != "nh:" {
				continue
			}
			// Only relevant when this prefix would reach the nh: stitch path in
			// pass 2 (i.e. no genuine non-dedup BGPReachabilityEdge winner).
			if group, hasBest := bestReach[ow.SrcID]; hasBest && !group.allDedup {
				continue
			}
			nhIP := ow.DstID[3:]
			if _, ok := routerIDToNodeID[nhIP]; ok {
				stitchablePfx[ow.SrcID] = struct{}{}
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
			// Drop orphaned prefix vertices — prefix with no edges means the
			// route was withdrawn but the vertex was not cleaned up in the
			// source graph. Including it causes resolution errors.
			if _, isPrefix := v.(*Prefix); isPrefix {
				if _, hasEdge := pfxWithEdges[v.GetID()]; !hasEdge {
					continue
				}
			}
			// Drop bare stub nodes: no RouterID, no Subtype, AND no Protocol.
			// The Protocol guard exempts IGP nodes — translateLSNode always
			// sets Protocol from the BGP-LS advertisement ("IS-IS_L1", etc.),
			// so Level-1-only IS-IS nodes are never incorrectly filtered.
			if n, ok := v.(*Node); ok && n.RouterID == "" && string(n.Subtype) == "" && n.Protocol == "" {
				id := n.ID
				if len(id) >= 3 && id[:3] == "nh:" {
					if _, hasEdge := nhWithEdges[id]; !hasEdge {
						continue // orphaned nh: stub — drop
					}
				} else {
					continue // plain-IP stub — always drop
				}
			}
			_ = out.AddVertex(v)
		}
		src.mu.RUnlock()
	}

	// --- pass 2: copy edges, stitching sessions and handling nh: ownership ---
	// stitchedPfxes records prefixes whose OwnershipEdge was stitched to an
	// IGP node (used in pass 3 to suppress redundant dedup-rewritten edges).
	stitchedPfxes := make(map[string]struct{})

	for _, src := range sources {
		src.mu.RLock()
		for _, e := range src.edges {
			switch typed := e.(type) {
			case *BGPSessionEdge:
				srcID := typed.SrcID
				if igpID, ok := routerIDToNodeID[srcID]; ok {
					rewritten := *typed
					rewritten.SrcID = igpID
					rewritten.ID = "bgpsess:" + igpID + ":" + typed.RemoteIP
					_ = out.AddEdge(&rewritten)
					continue
				}
				// Fallback: match by BMP router hash. Handles the case where
				// LocalBGPID (always 4-byte IPv4 format) doesn't match
				// Node.RouterID (which may be IPv6 when the BMP session runs
				// over IPv6, or empty when TLV 1028 is absent).
				if typed.BMPRouterHash != "" {
					if igpID, ok := routerHashToNodeID[typed.BMPRouterHash]; ok {
						rewritten := *typed
						rewritten.SrcID = igpID
						rewritten.ID = "bgpsess:" + igpID + ":" + typed.RemoteIP
						_ = out.AddEdge(&rewritten)
						continue
					}
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
				// Best-path winners selected in pre-pass; inserted in pass 3.
				_ = typed
			case *OwnershipEdge:
				// Only handle prefix→nexthop ownership edges here.
				if len(typed.SrcID) < 4 || typed.SrcID[:4] != "pfx:" {
					_ = out.AddEdge(e)
					continue
				}
				group, hasBest := bestReach[typed.SrcID]
				if hasBest && !group.allDedup {
					// Genuine (non-dedup) BGPReachabilityEdge winner exists —
					// suppress this fallback ownership edge.
					continue
				}
				// No genuine winner. Try to stitch nh:X → IGP node so the
				// prefix connects to its true iBGP origin (e.g. the router
				// doing default-originate) rather than an ASBR re-advertiser.
				if len(typed.DstID) > 3 && typed.DstID[:3] == "nh:" {
					nhIP := typed.DstID[3:]
					if igpID, ok := routerIDToNodeID[nhIP]; ok {
						rewritten := *typed
						rewritten.DstID = igpID
						rewritten.ID = "pfxown:" + typed.SrcID + ":" + igpID
						stitchedPfxes[typed.SrcID] = struct{}{}
						_ = out.AddEdge(&rewritten)
						continue
					}
					// Stitch failed for this nh: — suppress if another OwnershipEdge
					// for the same prefix CAN be stitched to an IGP node. That sibling
					// edge connects the prefix correctly; keeping this one would leave
					// an orphaned nh: vertex (e.g. a peering interface IP like
					// nh:10.2.1.8 alongside the already-stitched loopback node).
					if _, canStitch := stitchablePfx[typed.SrcID]; canStitch {
						continue
					}
				}
				// No IGP stitch available — keep as-is.
				_ = out.AddEdge(e)
			default:
				_ = out.AddEdge(e)
			}
		}
		src.mu.RUnlock()
	}

	// --- pass 3: insert winning BGPReachabilityEdges per prefix ------------
	for pfxID, group := range bestReach {
		if group.allDedup {
			if _, wasStitched := stitchedPfxes[pfxID]; wasStitched {
				// OwnershipEdge stitching connected the prefix to its IGP
				// origin — the ASBR re-advertisement edges are not needed.
				continue
			}
			// No stitchable nexthop available — insert dedup edges as fallback
			// so the prefix is not left orphaned.
		}
		for _, e := range group.edges {
			_ = out.AddEdge(e)
		}
	}

	// --- pass 4: remove nh: vertices left with no edges --------------------
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

// reachGroup holds all BGPReachabilityEdge candidates for a single prefix that
// share the same best quality. allDedup is true when every winning edge was
// obtained via dedup rewrite (peer:X → IGP node) — indicating the edges
// represent ASBR re-advertisement rather than true prefix origination.
type reachGroup struct {
	quality  bgpPathQuality
	edges    map[string]*BGPReachabilityEdge // edgeID → edge
	allDedup bool
}

// bgpPathQuality captures the BGP path attributes used for best-path
// comparison.
type bgpPathQuality struct {
	asPathLen uint32
	localPref uint32
	med       uint32
}

func bgpQuality(e *BGPReachabilityEdge) bgpPathQuality {
	return bgpPathQuality{
		asPathLen: uint32(len(e.ASPath)),
		localPref: e.LocalPref,
		med:       e.MED,
	}
}

// compare returns -1 if q is strictly better than other, 0 if equal, +1 if
// worse. "Better" follows the standard BGP decision process:
//  1. Shorter AS_PATH
//  2. Higher LocalPref
//  3. Lower MED
func (q bgpPathQuality) compare(other bgpPathQuality) int {
	if q.asPathLen != other.asPathLen {
		if q.asPathLen < other.asPathLen {
			return -1
		}
		return 1
	}
	if q.localPref != other.localPref {
		if q.localPref > other.localPref {
			return -1
		}
		return 1
	}
	if q.med != other.med {
		if q.med < other.med {
			return -1
		}
		return 1
	}
	return 0
}
