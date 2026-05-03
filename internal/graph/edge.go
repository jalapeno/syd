package graph

// EdgeType identifies the relationship a edge represents between two vertices.
type EdgeType string

const (
	// ETPhysical is a physical wire between two interface vertices.
	ETPhysical EdgeType = "physical"

	// ETIGPAdjacency is an IS-IS or OSPF link-state adjacency. Carries TE
	// attributes (metric, bandwidth, delay, SRLG). This is the primary edge
	// type used by the path engine for SRv6 path computation.
	ETIGPAdjacency EdgeType = "igp_adjacency"

	// ETBGPSession is a BGP peering session between two node vertices.
	ETBGPSession EdgeType = "bgp_session"

	// ETTunnel is a logical/overlay edge: SRv6 policy, GRE, VXLAN, etc.
	ETTunnel EdgeType = "tunnel"

	// ETAttachment connects an Endpoint vertex to the Node/switch it is
	// attached to. Directed: endpoint → node.
	ETAttachment EdgeType = "attachment"

	// ETOwnership expresses a "belongs to" relationship:
	//   Interface → Node, Prefix → Node, VRF → Node, Prefix → VRF
	ETOwnership EdgeType = "ownership"

	// ETBGPReachability connects an external BGP peer Node vertex to the Prefix
	// vertices it announces. Directed: BGPPeerNode → Prefix.
	// Carries path attributes (AS-path, local-pref, MED, origin) on the edge.
	ETBGPReachability EdgeType = "bgp_reachability"

	// ETVRFMembership connects an Endpoint or Node vertex to the VRF it belongs
	// to. Directed: Endpoint/Node → VRF.
	//
	// This edge enables the path engine to automatically determine tenant
	// isolation from graph topology when no explicit tenant_id is provided in the
	// path request, and to validate that all endpoints in a request belong to the
	// same VRF.
	ETVRFMembership EdgeType = "vrf_membership"
)

// Edge is implemented by all edge types in the graph.
type Edge interface {
	GetID() string
	GetType() EdgeType
	GetSrcID() string
	GetDstID() string
	IsDirected() bool
}

// BaseEdge holds fields common to every edge type.
type BaseEdge struct {
	ID      string            `json:"id"`
	Type    EdgeType          `json:"type"`
	SrcID   string            `json:"src_id"`
	DstID   string            `json:"dst_id"`
	Directed bool             `json:"directed"`
	Labels  map[string]string `json:"labels,omitempty"`
	Source  TopologySource    `json:"source,omitempty"`
}

func (e *BaseEdge) GetID() string      { return e.ID }
func (e *BaseEdge) GetType() EdgeType  { return e.Type }
func (e *BaseEdge) GetSrcID() string   { return e.SrcID }
func (e *BaseEdge) GetDstID() string   { return e.DstID }
func (e *BaseEdge) IsDirected() bool   { return e.Directed }
