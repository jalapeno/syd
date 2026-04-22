package graph

import "github.com/jalapeno/syd/internal/srv6"

// NodeSubtype classifies what kind of network forwarding element a Node is.
type NodeSubtype string

const (
	NSRouter       NodeSubtype = "router"
	NSSwitch       NodeSubtype = "switch"
	NSFirewall     NodeSubtype = "firewall"
	NSLoadBalancer NodeSubtype = "load_balancer"
	NSCNI          NodeSubtype = "cni"
	NSProxy        NodeSubtype = "proxy"
	// NSExternalBGP is an eBGP peer that lies outside the IGP domain — it has
	// no SRv6 SIDs and cannot be a transit node for path computation. It exists
	// as a topology anchor so that BGPReachabilityEdges can connect it to the
	// prefix vertices it announces.
	NSExternalBGP  NodeSubtype = "external_bgp"
)

// FlexAlgo describes a Flexible Algorithm definition advertised by a node
// (RFC 9350). Multiple algos can coexist on a single node.
type FlexAlgo struct {
	AlgoID     uint8  `json:"algo_id"`
	MetricType string `json:"metric_type"` // "igp" | "delay" | "te"
	CalcType   uint8  `json:"calc_type"`   // 0 = SPF
}

// MSDEntry is a Maximum SID Depth advertisement (RFC 8491).
type MSDEntry struct {
	Type  uint8 `json:"type"`
	Value uint8 `json:"value"`
}

// SRCapability describes the SR-MPLS global block advertised by a node.
type SRCapability struct {
	Flags uint8  `json:"flags,omitempty"`
	SRGB  []SRRange `json:"srgb,omitempty"` // SR Global Block ranges
}

// SRRange is a label range used in SR-MPLS capability TLVs.
type SRRange struct {
	Base  uint32 `json:"base"`
	Range uint32 `json:"range"`
}

// Node is a vertex that forwards transit traffic: a router, switch, or other
// network function. Interfaces on the node are modeled as separate Interface
// vertices connected via Ownership edges.
//
// Protocol-derived fields (IGPRouterID, AreaID, DomainID, Protocol) are
// populated by the BMP ingestion path and omitted for push-via-JSON or
// statically provisioned topologies.
type Node struct {
	BaseVertex
	Subtype     NodeSubtype    `json:"subtype"`
	Name        string         `json:"name,omitempty"`

	// Routing identity — optional for static fabrics
	RouterID    string         `json:"router_id,omitempty"`    // BGP/IGP router-id (IPv4 or IPv6)
	IGPRouterID string         `json:"igp_router_id,omitempty"`
	ASN         uint32         `json:"asn,omitempty"`
	AreaID      string         `json:"area_id,omitempty"`
	DomainID    int64          `json:"domain_id,omitempty"`
	Protocol    string         `json:"protocol,omitempty"` // "IS-IS" | "OSPFv2" | "OSPFv3" | "BGP" | "static"

	// SRv6 capabilities
	SRv6Locators []srv6.Locator   `json:"srv6_locators,omitempty"`
	SRv6NodeSID  *srv6.SID        `json:"srv6_node_sid,omitempty"` // uN SID

	// SR-MPLS capabilities (present on dual-plane or SR-MPLS-only nodes)
	SRCapability *SRCapability    `json:"sr_capability,omitempty"`
	SRAlgorithms []int            `json:"sr_algorithms,omitempty"`

	// Flex-Algo definitions advertised by this node
	FlexAlgos    []FlexAlgo       `json:"flex_algos,omitempty"`

	// Maximum SID Depth — important for SRH feasibility checks
	NodeMSD      []MSDEntry       `json:"node_msd,omitempty"`

	// BMP provenance — empty when source == SourcePush or SourceStatic
	BMPRouterHash string          `json:"bmp_router_hash,omitempty"`
	BMPPeerHash   string          `json:"bmp_peer_hash,omitempty"`
}
