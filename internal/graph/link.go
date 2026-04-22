package graph

// LinkEdge represents a directed link between two Node vertices, corresponding
// to a physical connection or an IGP adjacency. Each direction of a
// bidirectional link is modeled as a separate directed LinkEdge — this is
// required because TE attributes (delay, available bandwidth, utilization) are
// unidirectional by nature, and because SRv6 uA SIDs are direction-specific.
//
// In BMP-sourced topologies each LSLink message maps to one directed LinkEdge.
// The field names mirror LSLink closely to simplify the BMP ingestion adapter.
//
// The LocalIfaceID and RemoteIfaceID fields reference the Interface vertices on
// each end. These are the vertices that carry the uA SIDs and are what the path
// engine programs into SRv6 segment lists.
type LinkEdge struct {
	BaseEdge

	// Interface vertex IDs on each end of this directed link.
	// LocalIfaceID is the egress interface on the SrcID node.
	// RemoteIfaceID is the ingress interface on the DstID node.
	LocalIfaceID  string `json:"local_iface_id,omitempty"`
	RemoteIfaceID string `json:"remote_iface_id,omitempty"`

	// IGP / routing context — optional for static fabrics
	Protocol  string `json:"protocol,omitempty"`  // "IS-IS" | "OSPFv3" | "static"
	AreaID    string `json:"area_id,omitempty"`
	DomainID  int64  `json:"domain_id,omitempty"`
	MTID      uint16 `json:"mt_id,omitempty"` // Multi-Topology ID (IS-IS MT)

	// Basic TE attributes
	IGPMetric  uint32 `json:"igp_metric,omitempty"`
	TEMetric   uint32 `json:"te_metric,omitempty"`
	AdminGroup uint32 `json:"admin_group,omitempty"` // RFC 3630 affinity/color bits

	// Bandwidth — sourced from LSLink max_link_bw_kbps / max_resv_bw_kbps.
	// All values in bits per second.
	MaxBW      uint64   `json:"max_bw_bps,omitempty"`
	MaxResvBW  uint64   `json:"max_resv_bw_bps,omitempty"`
	UnresvBW   []uint64 `json:"unresv_bw_bps,omitempty"` // 8 CoS/priority classes

	// Shared Risk Link Group membership.
	SRLG []uint32 `json:"srlg,omitempty"`

	// Unidirectional performance metrics (RFC 7471 / RFC 8570 TE extensions).
	// These are sourced from LSLink unidir_* fields when available via BMP,
	// or set directly for static/operator-pushed topologies.
	UnidirDelay       uint32   `json:"unidir_delay_us,omitempty"`
	UnidirDelayMinMax []uint32 `json:"unidir_delay_min_max_us,omitempty"` // [min, max]
	UnidirDelayVar    uint32   `json:"unidir_delay_variation_us,omitempty"`
	UnidirPacketLoss  uint32   `json:"unidir_packet_loss,omitempty"` // millionths
	UnidirAvailBW     uint64   `json:"unidir_avail_bw_bps,omitempty"`
	UnidirBWUtil      uint32   `json:"unidir_bw_util,omitempty"` // millionths
}

// BGPSessionEdge represents a BGP peering session between two Node vertices.
// Populated by PeerStateChange messages in BMP-sourced topologies.
type BGPSessionEdge struct {
	BaseEdge
	LocalASN  uint32 `json:"local_asn,omitempty"`
	RemoteASN uint32 `json:"remote_asn,omitempty"`
	LocalIP   string `json:"local_ip,omitempty"`
	RemoteIP  string `json:"remote_ip,omitempty"`
	PeerType  uint8  `json:"peer_type,omitempty"` // iBGP=0, eBGP=1, etc.
	IsUp      bool   `json:"is_up"`
}

// AttachmentEdge connects an Endpoint vertex to the Node it is attached to.
// Directed: Endpoint → Node.
type AttachmentEdge struct {
	BaseEdge
	// AccessIfaceID optionally references the Interface vertex on the Node
	// through which the endpoint is connected (e.g. a ToR switch port).
	AccessIfaceID string `json:"access_iface_id,omitempty"`
	// VlanID and other L2 attributes can be added here as the model evolves.
}

// BGPReachabilityEdge connects an external BGP peer Node to a Prefix vertex.
// It represents a BGP route advertisement: SrcID (the eBGP peer) announces
// DstID (the prefix) with the given path attributes.
//
// These edges live in the underlay-prefixes-v4 or underlay-prefixes-v6 graph
// alongside the Prefix and peer Node vertices they connect.
type BGPReachabilityEdge struct {
	BaseEdge
	// Path attributes from the BGP UPDATE.
	ASPath    []uint32 `json:"as_path,omitempty"`    // full AS_PATH, first element is the direct peer's AS
	OriginAS  uint32   `json:"origin_as,omitempty"`  // rightmost ASN in AS_PATH (origin)
	LocalPref uint32   `json:"local_pref,omitempty"` // BGP LOCAL_PREF; 0 = not set / eBGP default
	MED       uint32   `json:"med,omitempty"`        // BGP MULTI_EXIT_DISC; 0 = not set
	Origin    string   `json:"origin,omitempty"`     // "igp" | "egp" | "incomplete"
	NextHop   string   `json:"nexthop,omitempty"`    // BGP NEXT_HOP attribute
}

// OwnershipEdge expresses a "belongs to" or "is hosted on" relationship.
// Examples:
//   Interface → Node  (this interface is on this node)
//   Prefix    → Node  (this node originates this prefix)
//   Prefix    → VRF   (this prefix lives in this VRF)
//   VRF       → Node  (this VRF is instantiated on this node)
type OwnershipEdge struct {
	BaseEdge
}
