package pathengine

import (
	"fmt"
	"net/netip"

	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/srv6"
)

// SegmentListMode controls how uSID containers are built from a computed path.
type SegmentListMode string

const (
	// ModeUA uses only the 16-bit function portion of each uA SID, placed in
	// the node-slot position so TryPackUSID treats it as a 16-bit slot.
	// When no uA SID is available for a hop, the 16-bit node SID is used as a
	// fallback (same slot width). Container capacity is 6 SIDs (96/16).
	// Requires adjacency function IDs to be globally unique within the fabric.
	ModeUA SegmentListMode = "ua"

	// ModeUN uses only node uN SIDs (16-bit slots). The source node is
	// omitted; all transit nodes and the destination are included. ECMP applies
	// within each node. Container capacity is 6 SIDs (96/16).
	ModeUN SegmentListMode = "un"

	// modeUAFull is the internal 32-bit default mode. Each hop contributes one
	// 32-bit slot (node(16)+function(16)) using the egress interface's uA SID,
	// with a fallback to the source node's uN SID. The destination uN SID is
	// appended as the final anchor. Container capacity is 3 SIDs (96/32).
	// Not exposed in the API; used when SegmentListMode is empty.
	modeUAFull SegmentListMode = "ua_full"
)

// BuildSegmentList constructs an SRv6 segment list for the given SPFResult.
//
// mode selects the encoding strategy:
//   - ModeUA (default): 16-bit function slot per hop + uN anchor (6/container);
//     falls back to 16-bit node slot when no uA SID is available for a hop.
//   - ModeUN: 16-bit node slot, transit+dst only, no source (6/container)
//   - "" or any unknown value: 32-bit node+function slot per hop + uN anchor
//     (3/container); classic full uA encoding.
//
// When tenantID is non-empty, the VRF's uDT SID is appended as the final
// segment (multi-tenant carrier for Options 1, 2b, 3).
//
// SIDs are passed through TryPackUSID; falls back to raw values if compression
// is not applicable.
func BuildSegmentList(g *graph.Graph, spf *SPFResult, algoID uint8, tenantID string, mode SegmentListMode) (srv6.SegmentList, error) {
	if len(spf.Edges) == 0 {
		return srv6.SegmentList{
			Encap:  srv6.EncapSRv6,
			Flavor: srv6.FlavorHEncapsRed,
		}, nil
	}

	var items []srv6.SIDItem
	var err error

	switch mode {
	case ModeUA:
		items, err = buildItemsUAOnly(g, spf, algoID, tenantID)
	case ModeUN:
		items, err = buildItemsUNOnly(g, spf, algoID, tenantID)
	default: // "" or modeUAFull — 32-bit full uA encoding
		items, err = buildItemsUA(g, spf, algoID, tenantID)
	}
	if err != nil {
		return srv6.SegmentList{}, err
	}

	sids, err := srv6.TryPackUSID(items)
	if err != nil {
		sids = srv6.FallbackValues(items)
	}

	return srv6.SegmentList{
		Encap:  srv6.EncapSRv6,
		Flavor: srv6.FlavorHEncapsRed,
		SIDs:   sids,
	}, nil
}

// --- per-mode item builders -----------------------------------------------

// buildItemsUA is the default mode: uA SID (or uN fallback) for each hop as
// a 32-bit slot, plus destination uN as the final anchor.
func buildItemsUA(g *graph.Graph, spf *SPFResult, algoID uint8, tenantID string) ([]srv6.SIDItem, error) {
	items := make([]srv6.SIDItem, 0, len(spf.Edges)+2)

	for i, le := range spf.Edges {
		item, err := uaSIDItemForEdge(g, le, algoID)
		if err != nil {
			return nil, fmt.Errorf("hop %d (%s→%s): %w", i, le.GetSrcID(), le.GetDstID(), err)
		}
		items = append(items, item)
	}

	dstID := spf.NodeIDs[len(spf.NodeIDs)-1]
	if item, ok := nodeUNSIDItem(g, dstID, algoID); ok {
		items = append(items, item)
	}

	if tenantID != "" {
		if item, ok := tenantUDTSIDItem(g, tenantID); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

// buildItemsUAOnly builds compact 16-bit slots using only the function portion
// of each uA SID. When no uA SID is available for a hop, the source node's
// uN SID (also a 16-bit slot) is used as a fallback. The destination uN anchor
// is always appended as a 16-bit node slot.
//
// Both function-extracted items and uN fallback items use structure {32,16,0,0}
// so TryPackUSID keeps slot widths consistent and packs 6 per container.
func buildItemsUAOnly(g *graph.Graph, spf *SPFResult, algoID uint8, tenantID string) ([]srv6.SIDItem, error) {
	items := make([]srv6.SIDItem, 0, len(spf.Edges)+2)

	for i, le := range spf.Edges {
		item, err := functionOnlySIDItem(g, le, algoID)
		if err != nil {
			return nil, fmt.Errorf("hop %d (%s→%s): %w", i, le.GetSrcID(), le.GetDstID(), err)
		}
		items = append(items, item)
	}

	dstID := spf.NodeIDs[len(spf.NodeIDs)-1]
	if item, ok := nodeUNSIDItem(g, dstID, algoID); ok {
		items = append(items, item)
	}

	if tenantID != "" {
		if item, ok := tenantUDTSIDItem(g, tenantID); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

// buildItemsUNOnly builds 16-bit node-slot items for every node except the
// source. NodeIDs = [src, transit..., dst]; src is skipped because the
// originating node does not need to appear in its own segment list.
func buildItemsUNOnly(g *graph.Graph, spf *SPFResult, algoID uint8, tenantID string) ([]srv6.SIDItem, error) {
	if len(spf.NodeIDs) < 2 {
		return nil, nil
	}
	items := make([]srv6.SIDItem, 0, len(spf.NodeIDs))

	for _, nodeID := range spf.NodeIDs[1:] { // skip source at index 0
		item, ok := nodeUNSIDItem(g, nodeID, algoID)
		if !ok {
			return nil, fmt.Errorf("no uN SID for node %q", nodeID)
		}
		items = append(items, item)
	}

	if tenantID != "" {
		if item, ok := tenantUDTSIDItem(g, tenantID); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

// --- per-hop SID helpers --------------------------------------------------

// uaSIDItemForEdge returns the SIDItem for the egress interface of a LinkEdge
// (ModeUA). Falls back to the source node's uN SID if no uA SID is found.
func uaSIDItemForEdge(g *graph.Graph, le *graph.LinkEdge, algoID uint8) (srv6.SIDItem, error) {
	if le.LocalIfaceID != "" {
		v := g.GetVertex(le.LocalIfaceID)
		if v != nil {
			if iface, ok := v.(*graph.Interface); ok {
				if item, found := pickUASIDItem(iface.SRv6uASIDs, algoID); found {
					return item, nil
				}
			}
		}
	}

	// Fall back to source node uN SID.
	if item, ok := nodeUNSIDItem(g, le.GetSrcID(), algoID); ok {
		return item, nil
	}

	return srv6.SIDItem{}, fmt.Errorf(
		"no uA SID on interface %q and no uN SID on node %q — topology missing SRv6 SID data",
		le.LocalIfaceID, le.GetSrcID(),
	)
}

// functionOnlySIDItem returns a 16-bit SIDItem for ModeUAOnly.
//
// If a uA SID is found on the egress interface, extractFunctionSlot strips
// the node portion and places the function bits in the node-slot position,
// yielding structure {32,16,0,0}. If no uA SID is available, the source
// node's uN SID (also {32,16,0,0}) is used as a fallback. Both produce
// consistent 16-bit slots so TryPackUSID packs 6 per container.
func functionOnlySIDItem(g *graph.Graph, le *graph.LinkEdge, algoID uint8) (srv6.SIDItem, error) {
	if le.LocalIfaceID != "" {
		v := g.GetVertex(le.LocalIfaceID)
		if v != nil {
			if iface, ok := v.(*graph.Interface); ok {
				if rawItem, found := pickUASIDItem(iface.SRv6uASIDs, algoID); found {
					if funcItem, ok := extractFunctionSlot(rawItem); ok {
						return funcItem, nil
					}
				}
			}
		}
	}

	// Fall back to source node uN SID (16-bit node slot).
	if item, ok := nodeUNSIDItem(g, le.GetSrcID(), algoID); ok {
		return item, nil
	}

	return srv6.SIDItem{}, fmt.Errorf(
		"no uA SID on interface %q and no uN SID on node %q",
		le.LocalIfaceID, le.GetSrcID(),
	)
}

// extractFunctionSlot extracts the function bits from a uA SIDItem and returns
// a new SIDItem with the function placed immediately after the block (in the
// "node" position). This makes it a 16-bit slot compatible with uN SID items
// so that TryPackUSID treats both uniformly.
//
//	Input:  fc00:0:NODE:FUNC:: with structure {blockLen, nodeLen, funcLen, 0}
//	Output: fc00:0:FUNC::       with structure {blockLen, funcLen, 0, 0}
//
// Returns (item, false) if the SID has no function bits or is unparseable.
func extractFunctionSlot(ua srv6.SIDItem) (srv6.SIDItem, bool) {
	s := ua.Structure
	if s == nil || s.FunctionLen == 0 || s.FunctionLen%8 != 0 {
		return srv6.SIDItem{}, false
	}

	blockBytes := int(s.LocatorBlockLen / 8)
	nodeBytes := int(s.LocatorNodeLen / 8)
	funcBytes := int(s.FunctionLen / 8)

	a, err := netip.ParseAddr(ua.Value)
	if err != nil {
		return srv6.SIDItem{}, false
	}
	raw := a.As16()

	// Build a new SID: block bytes unchanged, then function bytes moved
	// immediately after the block (displacing the node portion), zeros after.
	var newRaw [16]byte
	copy(newRaw[:blockBytes], raw[:blockBytes])
	copy(newRaw[blockBytes:blockBytes+funcBytes], raw[blockBytes+nodeBytes:blockBytes+nodeBytes+funcBytes])

	return srv6.SIDItem{
		Value:    netip.AddrFrom16(newRaw).String(),
		Behavior: ua.Behavior,
		Structure: &srv6.SIDStructure{
			LocatorBlockLen: s.LocatorBlockLen,
			LocatorNodeLen:  s.FunctionLen, // function occupies the node slot for packing
			FunctionLen:     0,
			ArgumentLen:     0,
		},
	}, true
}

// pickUASIDItem selects the uA SIDItem matching algoID from a UASID slice,
// falling back to algo 0 then the first entry.
func pickUASIDItem(sids []srv6.UASID, algoID uint8) (srv6.SIDItem, bool) {
	var fallback0, fallbackFirst *srv6.UASID
	for i := range sids {
		s := &sids[i]
		if s.AlgoID == algoID {
			return toSIDItem(&s.SID), true
		}
		if s.AlgoID == 0 && fallback0 == nil {
			fallback0 = s
		}
		if fallbackFirst == nil {
			fallbackFirst = s
		}
	}
	if fallback0 != nil {
		return toSIDItem(&fallback0.SID), true
	}
	if fallbackFirst != nil {
		return toSIDItem(&fallbackFirst.SID), true
	}
	return srv6.SIDItem{}, false
}

// nodeUNSIDItem returns the SIDItem for a node's uN SID, checking
// per-locator algo-specific SIDs first, then the top-level node SID.
func nodeUNSIDItem(g *graph.Graph, nodeID string, algoID uint8) (srv6.SIDItem, bool) {
	v := g.GetVertex(nodeID)
	if v == nil {
		return srv6.SIDItem{}, false
	}
	n, ok := v.(*graph.Node)
	if !ok {
		return srv6.SIDItem{}, false
	}
	for _, loc := range n.SRv6Locators {
		if loc.AlgoID == algoID && loc.NodeSID != nil {
			return toSIDItem(loc.NodeSID), true
		}
	}
	if n.SRv6NodeSID != nil {
		return toSIDItem(n.SRv6NodeSID), true
	}
	return srv6.SIDItem{}, false
}

// tenantUDTSIDItem returns the SIDItem for a VRF vertex's uDT SID.
// The tenantID must be the vertex ID of a VRF that carries a non-nil SRv6uDTSID.
func tenantUDTSIDItem(g *graph.Graph, tenantID string) (srv6.SIDItem, bool) {
	v := g.GetVertex(tenantID)
	if v == nil {
		return srv6.SIDItem{}, false
	}
	vrf, ok := v.(*graph.VRF)
	if !ok {
		return srv6.SIDItem{}, false
	}
	if vrf.SRv6uDTSID == nil {
		return srv6.SIDItem{}, false
	}
	return toSIDItem(vrf.SRv6uDTSID), true
}

func toSIDItem(s *srv6.SID) srv6.SIDItem {
	return srv6.SIDItem{Value: s.Value, Behavior: s.Behavior, Structure: s.Structure}
}
