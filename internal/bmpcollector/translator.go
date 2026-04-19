package bmpcollector

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	gobmpmsg "github.com/sbezverk/gobmp/pkg/message"
	gobmpsrv6 "github.com/sbezverk/gobmp/pkg/srv6"
	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/srv6"
)

// afSplitMTID is the IS-IS Multi-Topology Identifier for the IPv6/SRv6
// topology. BGP-LS ls_link messages carrying this MTID are placed in the
// primary topology graph (used for SRv6 path computation). All other MTID
// values — including the base topology (MTID=0) which carries IPv4
// adjacencies — are placed in the companion "<topoID>-v4" graph so that the
// SRv6 path engine never accidentally traverses an IPv4 link.
//
// MT-2 = IPv6 Unicast (RFC 5305 §3) is the standard IS-IS MT used for SRv6
// in IOS-XR and most other implementations.
const afSplitMTID uint16 = 2

// --- Vertex / edge ID helpers ------------------------------------------------

// nodeID returns the graph vertex ID for a BGP-LS node. We use IGPRouterID
// as the stable key (IS-IS system ID or OSPF router ID).
func nodeID(igpRouterID string) string {
	return igpRouterID
}

// ifaceID returns the Interface vertex ID for the local end of a link.
// Prefer the link-layer IP address for readability; fall back to the numeric
// local link ID when no IP is present (e.g. some IS-IS topologies).
func ifaceID(localNodeID, localLinkIP string, localLinkNum uint32) string {
	if localLinkIP != "" {
		return "iface:" + localNodeID + "/" + localLinkIP
	}
	return fmt.Sprintf("iface:%s/%d", localNodeID, localLinkNum)
}

// linkEdgeID returns a deterministic edge ID for a directed BGP-LS link.
func linkEdgeID(localNodeID, remoteNodeID, localLinkIP string, localLinkNum uint32) string {
	if localLinkIP != "" {
		return "link:" + localNodeID + ":" + remoteNodeID + ":" + localLinkIP
	}
	return fmt.Sprintf("link:%s:%s:%d", localNodeID, remoteNodeID, localLinkNum)
}

// ownershipEdgeID returns the ownership edge ID for an interface → node pair.
func ownershipEdgeID(ifID, nID string) string {
	return "own:" + ifID + "->" + nID
}

// peerEdgeID returns the BGP session edge ID for a local/remote BGP ID pair.
func peerEdgeID(localBGPID, remoteIP string) string {
	return "bgpsess:" + localBGPID + ":" + remoteIP
}

// prefixVertexID returns the Prefix vertex ID for an IP prefix/len.
func prefixVertexID(prefix string, prefixLen int32) string {
	return fmt.Sprintf("pfx:%s/%d", prefix, prefixLen)
}

// nexthopNodeID returns the stub Node vertex ID for a nexthop IP address.
// IPv6 nexthops may be "primary,link-local" — only the primary is used.
func nexthopNodeID(nexthop string) string {
	if i := strings.IndexByte(nexthop, ','); i >= 0 {
		nexthop = nexthop[:i]
	}
	if nexthop == "" {
		return ""
	}
	return "nh:" + nexthop
}

// prefixOwnerEdgeID returns the OwnershipEdge ID for a (prefix vertex, nexthop
// node) pair.
func prefixOwnerEdgeID(pfxID, nhID string) string {
	return "pfxown:" + pfxID + ":" + nhID
}

// --- Behavior code mapping ---------------------------------------------------

// behaviorFromCode maps an IANA SRv6 Endpoint Behavior code (RFC 8986 §8.1)
// to our internal BehaviorType string. Codes not listed fall through to a
// hex-string representation so callers can still log/store them.
func behaviorFromCode(code uint16) srv6.BehaviorType {
	// RFC 8986 §8.1 base behaviors
	switch code {
	case 0x0001, 0x0002, 0x0003, 0x0004:
		// End, End.PSP, End.USP, End.PSP+USP
		return srv6.BehaviorEnd
	case 0x0005, 0x0006, 0x0007, 0x0008:
		// End.X, End.X.PSP, End.X.USP, End.X.PSP+USP
		return srv6.BehaviorEndX
	case 0x0012: // 18 End.DT6
		return srv6.BehaviorEndDT6
	case 0x0013: // 19 End.DT4
		return srv6.BehaviorEndDT4
	case 0x0014: // 20 End.DT46
		return srv6.BehaviorEndDT46
	case 0x0015: // 21 End.DX6
		return srv6.BehaviorEndDX6
	case 0x0016: // 22 End.DX4
		return srv6.BehaviorEndDX4
	case 0x0010: // 16 End.B6.Encaps
		return srv6.BehaviorEndB6Encaps
	case 0x0011: // 17 End.B6.Encaps.Red
		return srv6.BehaviorEndB6EncapsRed
	// uSID micro-segment behavior codes.
	// IOS-XR uses 0x0030 = uN (node locator, no function bits) and
	// 0x0039 = uA (micro-adjacency, 16-bit function). IANA registry uses
	// 0x0041/0x0042 for the same roles — treat both sets as End/End.X.
	case 0x0030:
		return srv6.BehaviorEnd
	case 0x0039:
		return srv6.BehaviorEndX
	case 0x0041:
		return srv6.BehaviorEnd
	case 0x0042:
		return srv6.BehaviorEndX
	default:
		return srv6.BehaviorType(fmt.Sprintf("0x%04x", code))
	}
}

// sidStructure converts a gobmp SIDStructure (field names differ from ours).
func sidStructure(s *gobmpsrv6.SIDStructure) *srv6.SIDStructure {
	if s == nil {
		return nil
	}
	return &srv6.SIDStructure{
		LocatorBlockLen: s.LBLength,
		LocatorNodeLen:  s.LNLength,
		FunctionLen:     s.FunLength,
		ArgumentLen:     s.ArgLength,
	}
}

// locatorPrefix formats a locator prefix string in CIDR notation.
func locatorPrefix(prefix string, prefixLen int32) string {
	if prefix == "" {
		return ""
	}
	return fmt.Sprintf("%s/%d", prefix, prefixLen)
}

// --- LSNode translation -------------------------------------------------------

// translateLSNode converts a GoBMP LSNode message to a graph.Node.
// The SRv6Locators slice is intentionally left empty here; locators arrive
// separately via LSSRv6SID messages and are merged by the updater.
func translateLSNode(msg *gobmpmsg.LSNode) *graph.Node {
	return &graph.Node{
		BaseVertex: graph.BaseVertex{
			ID:   nodeID(msg.IGPRouterID),
			Type: graph.VTNode,
		},
		Name:          msg.Name,
		RouterID:      msg.RouterID,
		IGPRouterID:   msg.IGPRouterID,
		ASN:           msg.ASN,
		AreaID:        msg.AreaID,
		DomainID:      msg.DomainID,
		Protocol:      msg.Protocol,
		BMPRouterHash: msg.RouterHash,
		BMPPeerHash:   msg.PeerHash,
	}
}

// --- LSSRv6SID translation ----------------------------------------------------

// translateLSSRv6SID extracts the node ID and a Locator from a GoBMP
// LSSRv6SID message. Returns ok=false when the message lacks the data needed
// to construct a useful locator (missing SID or structure).
func translateLSSRv6SID(msg *gobmpmsg.LSSRv6SID) (nID string, locator srv6.Locator, ok bool) {
	if msg.IGPRouterID == "" || msg.SRv6SID == "" {
		return "", srv6.Locator{}, false
	}

	var algoID uint8
	var behavior srv6.BehaviorType = srv6.BehaviorEnd
	if msg.SRv6EndpointBehavior != nil {
		behavior = behaviorFromCode(msg.SRv6EndpointBehavior.EndpointBehavior)
		algoID = msg.SRv6EndpointBehavior.Algorithm
	}

	nodeSID := &srv6.SID{
		Value:     msg.SRv6SID,
		Behavior:  behavior,
		Structure: sidStructure(msg.SRv6SIDStructure),
		AlgoID:    algoID,
	}

	locator = srv6.Locator{
		Prefix:  locatorPrefix(msg.Prefix, msg.PrefixLen),
		AlgoID:  algoID,
		NodeSID: nodeSID,
	}
	return nodeID(msg.IGPRouterID), locator, true
}

// --- LSLink translation -------------------------------------------------------

// translateLSLink converts a GoBMP LSLink message to the three graph objects
// that model a directed link:
//   - iface: the Interface vertex on the local node
//   - edge:  the directed LinkEdge from local node to remote node
//   - own:   the OwnershipEdge tying iface to its node
//
// If the local or remote node IGP router IDs are missing the call returns
// nils; the caller must skip the message.
func translateLSLink(msg *gobmpmsg.LSLink) (iface *graph.Interface, edge *graph.LinkEdge, own *graph.OwnershipEdge) {
	localNID := nodeID(msg.IGPRouterID)
	remoteNID := nodeID(msg.RemoteIGPRouterID)
	if localNID == "" || remoteNID == "" {
		return nil, nil, nil
	}

	ifID := ifaceID(localNID, msg.LocalLinkIP, msg.LocalLinkID)
	remoteIfID := ifaceID(remoteNID, msg.RemoteLinkIP, msg.RemoteLinkID)
	edgeID := linkEdgeID(localNID, remoteNID, msg.LocalLinkIP, msg.LocalLinkID)

	// Translate SRv6 End.X (uA) SIDs from the link.
	uaSIDs := make([]srv6.UASID, 0, len(msg.SRv6ENDXSID))
	for _, x := range msg.SRv6ENDXSID {
		if x == nil || x.SID == "" {
			continue
		}
		// Extract SID Structure sub-TLV so TryPackUSID can compute correct slot width.
		var structure *srv6.SIDStructure
		for _, stlv := range x.SubTLVs {
			if s, ok := stlv.(*gobmpsrv6.SIDStructure); ok {
				structure = sidStructure(s)
				break
			}
		}
		uaSIDs = append(uaSIDs, srv6.UASID{
			SID: srv6.SID{
				Value:     x.SID,
				Behavior:  srv6.BehaviorEndX,
				Structure: structure,
				AlgoID:    x.Algorithm,
			},
			Weight: x.Weight,
		})
	}

	// Translate SR-MPLS adjacency SIDs.
	adjSIDs := make([]srv6.AdjSID, 0, len(msg.LSAdjacencySID))
	for _, a := range msg.LSAdjacencySID {
		if a == nil {
			continue
		}
		adjSIDs = append(adjSIDs, srv6.AdjSID{
			Label:  a.SID,
			Weight: a.Weight,
		})
	}

	iface = &graph.Interface{
		BaseVertex: graph.BaseVertex{
			ID:   ifID,
			Type: graph.VTInterface,
		},
		OwnerNodeID: localNID,
		Name:        msg.LinkName,
		LinkLocalID: msg.LocalLinkID,
		Bandwidth:   uint64(math.Float32frombits(msg.MaxLinkBW) * 8), // bytes/sec → bits/sec
		SRv6uASIDs:  uaSIDs,
		AdjSIDs:     adjSIDs,
	}

	// Build unreserved BW slice in bits/sec (IEEE 754 float32 bytes/sec → bits/sec).
	var unresvBW []uint64
	if len(msg.UnResvBW) > 0 {
		unresvBW = make([]uint64, len(msg.UnResvBW))
		for i, v := range msg.UnResvBW {
			unresvBW[i] = uint64(math.Float32frombits(v) * 8)
		}
	}

	var mtid uint16
	if msg.MTID != nil {
		mtid = msg.MTID.MTID
	}

	edge = &graph.LinkEdge{
		BaseEdge: graph.BaseEdge{
			ID:       edgeID,
			Type:     graph.ETIGPAdjacency,
			SrcID:    localNID,
			DstID:    remoteNID,
			Directed: true,
		},
		LocalIfaceID:  ifID,
		RemoteIfaceID: remoteIfID,
		Protocol:      msg.Protocol,
		AreaID:        msg.AreaID,
		DomainID:      msg.DomainID,
		MTID:          mtid,
		IGPMetric:     msg.IGPMetric,
		TEMetric:      msg.TEDefaultMetric,
		AdminGroup:    msg.AdminGroup,
		MaxBW:         uint64(math.Float32frombits(msg.MaxLinkBW) * 8),
		MaxResvBW:     uint64(math.Float32frombits(msg.MaxResvBW) * 8),
		UnresvBW:      unresvBW,
		SRLG:          msg.SRLG,
		// Unidirectional performance metrics (RFC 7471 / RFC 8570).
		// UnidirAvailableBW is in the same unit GoBMP uses; document as
		// needing verification against the router's actual advertisement.
		UnidirDelay:       msg.UnidirLinkDelay,
		UnidirDelayMinMax: msg.UnidirLinkDelayMinMax,
		UnidirDelayVar:    msg.UnidirDelayVariation,
		UnidirPacketLoss:  msg.UnidirPacketLoss,
		UnidirAvailBW:     uint64(msg.UnidirAvailableBW),
		UnidirBWUtil:      msg.UnidirBWUtilization,
	}

	own = &graph.OwnershipEdge{
		BaseEdge: graph.BaseEdge{
			ID:       ownershipEdgeID(ifID, localNID),
			Type:     graph.ETOwnership,
			SrcID:    ifID,
			DstID:    localNID,
			Directed: true,
		},
	}
	return iface, edge, own
}

// --- Peer translation ---------------------------------------------------------

// translatePeer converts a GoBMP PeerStateChange to a BGPSessionEdge.
// Action "add" produces IsUp=true; "del" or "down" produces IsUp=false.
func translatePeer(msg *gobmpmsg.PeerStateChange) *graph.BGPSessionEdge {
	isUp := strings.EqualFold(msg.Action, "add")
	return &graph.BGPSessionEdge{
		BaseEdge: graph.BaseEdge{
			ID:       peerEdgeID(msg.LocalBGPID, msg.RemoteIP),
			Type:     graph.ETBGPSession,
			SrcID:    msg.LocalBGPID,
			DstID:    msg.RemoteIP,
			Directed: true,
		},
		LocalASN:  msg.LocalASN,
		RemoteASN: msg.RemoteASN,
		LocalIP:   msg.LocalIP,
		RemoteIP:  msg.RemoteIP,
		IsUp:      isUp,
	}
}

// --- Unicast prefix translation -----------------------------------------------

// translateUnicastPrefix converts a GoBMP UnicastPrefix message to:
//   - pfx: a Prefix vertex (keyed by "<ip>/<len>")
//   - nh:  a stub Node vertex for the nexthop (keyed by "nh:<ip>"); nil when
//     the message carries no nexthop
//   - own: an OwnershipEdge connecting pfx → nh; nil when nh is nil
//
// The same prefix announced via different nexthops produces the same pfx vertex
// but distinct nh nodes and own edges, representing multiple equal-cost paths.
func translateUnicastPrefix(msg *gobmpmsg.UnicastPrefix) (pfx *graph.Prefix, nh *graph.Node, own *graph.OwnershipEdge) {
	pfxID := prefixVertexID(msg.Prefix, msg.PrefixLen)
	pfx = &graph.Prefix{
		BaseVertex: graph.BaseVertex{
			ID:   pfxID,
			Type: graph.VTPrefix,
		},
		Prefix:    fmt.Sprintf("%s/%d", msg.Prefix, msg.PrefixLen),
		PrefixLen: msg.PrefixLen,
	}

	nhID := nexthopNodeID(msg.Nexthop)
	if nhID == "" {
		return pfx, nil, nil
	}

	nh = &graph.Node{
		BaseVertex: graph.BaseVertex{
			ID:   nhID,
			Type: graph.VTNode,
		},
	}
	own = &graph.OwnershipEdge{
		BaseEdge: graph.BaseEdge{
			ID:       prefixOwnerEdgeID(pfxID, nhID),
			Type:     graph.ETOwnership,
			SrcID:    pfxID,
			DstID:    nhID,
			Directed: true,
		},
	}
	return pfx, nh, own
}

// --- MessageHandler implementations ------------------------------------------

// lsNodeHandler processes gobmp.parsed.ls_node messages.
type lsNodeHandler struct {
	updater  *Updater
	topoID   string
	v4TopoID string // companion graph; node is mirrored here when it exists
}

func (h *lsNodeHandler) Subject() string { return SubjectLSNode }

func (h *lsNodeHandler) Handle(data []byte, store *graph.Store) error {
	var msg gobmpmsg.LSNode
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("unmarshal LSNode: %w", err)
	}
	if msg.IGPRouterID == "" {
		return nil // incomplete message, skip
	}
	g := h.updater.EnsureGraph(store, h.topoID)
	if msg.Action == "del" {
		h.updater.RemoveVertex(g, nodeID(msg.IGPRouterID))
		// Also remove from the companion v4 graph if it has been created.
		if h.v4TopoID != "" {
			if g4 := store.Get(h.v4TopoID); g4 != nil {
				h.updater.RemoveVertex(g4, nodeID(msg.IGPRouterID))
			}
		}
		return nil
	}
	h.updater.UpsertNode(g, translateLSNode(&msg))
	// Mirror to the companion v4 graph when it already exists (created lazily
	// by lsLinkHandler on the first IPv4 link). This ensures the v4 graph has
	// full node data (name, router-id, etc.) and not just link-created stubs.
	if h.v4TopoID != "" {
		if g4 := store.Get(h.v4TopoID); g4 != nil {
			h.updater.UpsertNode(g4, translateLSNode(&msg))
		}
	}
	return nil
}

// lsLinkHandler processes gobmp.parsed.ls_link messages.
type lsLinkHandler struct {
	updater  *Updater
	topoID   string // primary (IPv6/SRv6) topology graph
	v4TopoID string // companion IPv4 topology graph (receives MTID != afSplitMTID)
}

func (h *lsLinkHandler) Subject() string { return SubjectLSLink }

func (h *lsLinkHandler) Handle(data []byte, store *graph.Store) error {
	var msg gobmpmsg.LSLink
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("unmarshal LSLink: %w", err)
	}
	iface, edge, own := translateLSLink(&msg)
	if iface == nil {
		return nil // missing node IDs, skip
	}

	// Route to the correct graph based on the IS-IS Multi-Topology ID.
	// MTID=2 (MT-IPv6) carries SRv6 adjacencies → primary graph.
	// MTID=0 (base topology) and absent MTID carry IPv4 adjacencies →
	// companion v4 graph so the SRv6 SPF never traverses IPv4-only links.
	targetTopoID := h.topoID
	if h.v4TopoID != "" && (msg.MTID == nil || msg.MTID.MTID != afSplitMTID) {
		targetTopoID = h.v4TopoID
	}

	g := h.updater.EnsureGraph(store, targetTopoID)
	if msg.Action == "del" {
		h.updater.RemoveEdge(g, edge.GetID())
		h.updater.RemoveVertex(g, iface.GetID())
		return nil
	}
	// Ensure stub nodes exist for both ends before adding edges.
	h.updater.EnsureNode(g, edge.SrcID)
	h.updater.EnsureNode(g, edge.DstID)
	h.updater.UpsertInterface(g, iface, own)
	h.updater.UpsertLinkEdge(g, edge)
	return nil
}

// lsSRv6SIDHandler processes gobmp.parsed.ls_srv6_sid messages.
type lsSRv6SIDHandler struct {
	updater *Updater
	topoID  string
}

func (h *lsSRv6SIDHandler) Subject() string { return SubjectLSSRv6SID }

func (h *lsSRv6SIDHandler) Handle(data []byte, store *graph.Store) error {
	var msg gobmpmsg.LSSRv6SID
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("unmarshal LSSRv6SID: %w", err)
	}
	nID, locator, ok := translateLSSRv6SID(&msg)
	if !ok {
		return nil
	}
	g := h.updater.EnsureGraph(store, h.topoID)
	if msg.Action == "del" {
		h.updater.RemoveLocator(g, nID, locator.Prefix)
		return nil
	}
	h.updater.EnsureNode(g, nID)
	h.updater.UpsertLocator(g, nID, locator)
	return nil
}

// peerHandler processes gobmp.parsed.peer messages.
type peerHandler struct {
	updater *Updater
	topoID  string
}

func (h *peerHandler) Subject() string { return SubjectPeer }

func (h *peerHandler) Handle(data []byte, store *graph.Store) error {
	var msg gobmpmsg.PeerStateChange
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("unmarshal PeerStateChange: %w", err)
	}
	if msg.LocalBGPID == "" || msg.RemoteIP == "" {
		return nil
	}
	g := h.updater.EnsureGraph(store, h.topoID)
	// Upsert the session edge for both up and down so callers can observe
	// peer state. We do not remove topology data on peer down — the BGP-LS
	// routes remain valid until explicitly withdrawn.
	h.updater.UpsertBGPSession(g, translatePeer(&msg))
	return nil
}

// unicastPrefixHandler processes gobmp.parsed.unicast_prefix_v4 or _v6
// messages. A single struct handles both address families; the subject and
// topoID differentiate them.
type unicastPrefixHandler struct {
	updater *Updater
	topoID  string
	subject string // SubjectUnicastV4 or SubjectUnicastV6
}

func (h *unicastPrefixHandler) Subject() string { return h.subject }

func (h *unicastPrefixHandler) Handle(data []byte, store *graph.Store) error {
	var msg gobmpmsg.UnicastPrefix
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("unmarshal UnicastPrefix: %w", err)
	}
	if msg.Prefix == "" {
		return nil // EoR marker or incomplete message — skip
	}
	pfx, nh, own := translateUnicastPrefix(&msg)
	g := h.updater.EnsureGraph(store, h.topoID)
	if msg.Action == "del" {
		// On withdrawal, remove the ownership edge for this (prefix, nexthop)
		// pair. The Prefix vertex is left in place: other nexthops may still
		// advertise the same prefix, and the vertex is harmless when orphaned.
		if own != nil {
			h.updater.RemoveEdge(g, own.GetID())
		}
		return nil
	}
	h.updater.UpsertPrefix(g, pfx, nh, own)
	return nil
}

// DefaultHandlers returns the six handlers that populate all topology graphs.
// Register these with Collector before calling Start. Additional AFI/SAFI
// handlers can be registered independently.
//
// Six graphs are populated (all derived from topoID, e.g. "underlay"):
//
//   - topoID:                  IS-IS MT-2 (IPv6/SRv6) links, path computation
//   - topoID+"-v4":            IS-IS base-topology (MTID=0) IPv4 links
//   - topoID+"-peers":         BGP session topology (IP-addressed node stubs)
//   - topoID+"-prefixes-v4":   BGP IPv4 unicast prefixes → nexthop nodes
//   - topoID+"-prefixes-v6":   BGP IPv6 unicast prefixes → nexthop nodes
//
// Node vertices are written to the primary graph and mirrored to the v4
// companion once the companion is created by the first IPv4 link.
func DefaultHandlers(updater *Updater, topoID string) []MessageHandler {
	v4TopoID := topoID + "-v4"
	peersTopoID := topoID + "-peers"
	prefV4TopoID := topoID + "-prefixes-v4"
	prefV6TopoID := topoID + "-prefixes-v6"
	return []MessageHandler{
		&lsNodeHandler{updater: updater, topoID: topoID, v4TopoID: v4TopoID},
		&lsLinkHandler{updater: updater, topoID: topoID, v4TopoID: v4TopoID},
		&lsSRv6SIDHandler{updater: updater, topoID: topoID},
		&peerHandler{updater: updater, topoID: peersTopoID},
		&unicastPrefixHandler{updater: updater, topoID: prefV4TopoID, subject: SubjectUnicastV4},
		&unicastPrefixHandler{updater: updater, topoID: prefV6TopoID, subject: SubjectUnicastV6},
	}
}
