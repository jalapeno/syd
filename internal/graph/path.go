package graph

import "github.com/jalapeno/syd/internal/srv6"

// DisjointnessLevel specifies how isolated an allocated path must be from
// other allocated paths.
type DisjointnessLevel string

const (
	DisjointnessNone  DisjointnessLevel = "none"        // no isolation required
	DisjointnessLink  DisjointnessLevel = "link"         // no shared links
	DisjointnessNode  DisjointnessLevel = "node"         // no shared transit nodes
	DisjointnessSRLG  DisjointnessLevel = "srlg"         // no shared risk link groups
)

// SharingPolicy controls whether an allocated path can be reused by
// subsequent requests.
type SharingPolicy string

const (
	SharingExclusive SharingPolicy = "exclusive" // path removed from consideration entirely
	SharingAllowed   SharingPolicy = "shared"    // path available as fallback for sharing-tolerant requests
)

// PathConstraints captures what a caller requested when asking for a path.
type PathConstraints struct {
	MinBandwidthBPS uint64            `json:"min_bandwidth_bps,omitempty"`
	MaxLatencyUS    uint32            `json:"max_latency_us,omitempty"`
	Disjointness    DisjointnessLevel `json:"disjointness,omitempty"`
	Sharing         SharingPolicy     `json:"sharing,omitempty"`
	Color           uint32            `json:"color,omitempty"`    // SR policy color
	AdminGroup      uint32            `json:"admin_group,omitempty"` // affinity include bits
	ExcludeGroup    uint32            `json:"exclude_group,omitempty"` // affinity exclude bits
	AlgoID          uint8             `json:"algo_id,omitempty"`  // Flex-Algo
	TenantID        string            `json:"tenant_id,omitempty"` // VRF vertex ID; appends uDT SID to segment list

	// SegmentListMode controls how the SRv6 segment list is encoded.
	// "ua" (default): uA SID per hop (32-bit slots) + uN anchor at destination.
	// "ua_only":      16-bit function slot per hop; falls back to 16-bit node
	//                 slot when no uA SID is available. Up to 6 SIDs per container.
	// "un_only":      16-bit node slot for each transit node + destination;
	//                 source node is omitted. Up to 6 SIDs per container.
	SegmentListMode string `json:"segment_list_mode,omitempty"`
}

// PathMetric summarises the computed quality of a path.
type PathMetric struct {
	IGPMetric    uint32  `json:"igp_metric"`
	TEMetric     uint32  `json:"te_metric,omitempty"`
	DelayUS      uint32  `json:"delay_us,omitempty"`
	BottleneckBW uint64  `json:"bottleneck_bw_bps,omitempty"` // minimum BW along path
	HopCount     int     `json:"hop_count"`
}

// Path is a computed route from a source vertex to a destination vertex,
// together with the SRv6 segment list that implements it. Paths are produced
// by the path engine and stored in the allocation layer once a workload
// request is satisfied.
type Path struct {
	ID string `json:"id"`

	// Endpoints of the path (typically Endpoint vertex IDs)
	SrcID string `json:"src_id"`
	DstID string `json:"dst_id"`

	// Ordered traversal through the graph.
	// VertexIDs lists Node vertices visited; EdgeIDs lists LinkEdges traversed.
	VertexIDs []string `json:"vertex_ids"`
	EdgeIDs   []string `json:"edge_ids"`

	// The SRv6 segment list to push onto packets to realise this path.
	SegmentList srv6.SegmentList `json:"segment_list"`

	Metric      PathMetric      `json:"metric"`
	Constraints PathConstraints `json:"constraints"`
}
