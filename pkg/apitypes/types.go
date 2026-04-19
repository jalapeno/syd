// Package apitypes defines the public HTTP API request and response types for
// syd. These are the structures callers (AI schedulers, PyTorch jobs, network
// operators) use to interact with the controller. They are kept in a separate
// package so that external programs can import them without pulling in internal
// dependencies.
package apitypes

import "github.com/jalapeno/syd/internal/srv6"

// --- Topology API ---------------------------------------------------------

// TopologyResponse is returned by GET /topology/{id}.
type TopologyResponse struct {
	TopologyID  string      `json:"topology_id"`
	Description string      `json:"description,omitempty"`
	Stats       interface{} `json:"stats"` // graph.GraphStats
}

// TopologyListResponse is returned by GET /topology.
type TopologyListResponse struct {
	TopologyIDs []string `json:"topology_ids"`
}

// NodeSummary is one entry in a TopologyNodesResponse.
type NodeSummary struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// TopologyNodesResponse is returned by GET /topology/{id}/nodes.
type TopologyNodesResponse struct {
	TopologyID string        `json:"topology_id"`
	Nodes      []NodeSummary `json:"nodes"`
}

// --- Path request API -----------------------------------------------------

// EndpointSpec identifies a workload endpoint in a path request.
type EndpointSpec struct {
	// ID is the vertex ID in the topology graph. Required if the endpoint
	// was pre-registered in the topology via a push.
	ID string `json:"id,omitempty"`

	// Address is the IPv4 or IPv6 address of the endpoint. Used for
	// resolution when ID is not known.
	Address string `json:"address,omitempty"`

	// Role is an optional hint about the endpoint's function in the workload,
	// e.g. "sender", "receiver", "all-to-all".
	Role string `json:"role,omitempty"`

	// Metadata is forwarded to allocation records for observability.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// PathRequest is the body of POST /paths/request.
type PathRequest struct {
	// TopologyID selects which topology graph to compute paths in.
	TopologyID string `json:"topology_id"`

	// WorkloadID is a stable identifier for this workload. The caller is
	// responsible for uniqueness. Used to correlate allocation records and
	// to release paths when the workload completes.
	WorkloadID string `json:"workload_id"`

	// Endpoints is the list of GPU/host endpoints that need to communicate.
	// The controller computes paths for all directed pairs (or all-to-all,
	// depending on the topology and constraints).
	Endpoints []EndpointSpec `json:"endpoints"`

	// Disjointness specifies the isolation required between paths in this
	// workload. "none" | "link" | "node" | "srlg"
	Disjointness string `json:"disjointness,omitempty"`

	// Sharing controls whether allocated paths can be reused by later
	// requests. "exclusive" | "shared"
	Sharing string `json:"sharing,omitempty"`

	// ServiceLevel is a named preset that maps to a (Disjointness, Sharing)
	// combination. Overrides Disjointness and Sharing if set.
	// "lossless-disjoint" | "best-effort" | "shared-fabric"
	ServiceLevel string `json:"service_level,omitempty"`

	// Constraints carries fine-grained TE parameters.
	Constraints *PathRequestConstraints `json:"constraints,omitempty"`

	// PairingMode controls how endpoint pairs are enumerated and how
	// disjointness is enforced across the workload's paths.
	//
	// "all_directed"  (default) — every directed flow (A→B, B→A, A→C, …) is
	//   computed independently. Disjointness accumulates across all flows.
	//   Suitable for asymmetric or unicast traffic patterns.
	//
	// "bidir_paired" — each unordered pair (A↔B) is treated as one unit.
	//   The forward path A→B is computed first; the reverse B→A is derived
	//   by following the same physical links in the opposite direction.
	//   Both directions are excluded together before the next pair, so
	//   disjointness is enforced at the physical-link level. Recommended for
	//   AI all-reduce workloads where forward and reverse traffic naturally
	//   share the same fabric links.
	PairingMode string `json:"pairing_mode,omitempty"`

	// LeaseDuration, if non-zero, is the number of seconds the allocation
	// is held without a heartbeat before being automatically released.
	LeaseDuration int `json:"lease_duration_seconds,omitempty"`

	// TenantID, if set, is the vertex ID of a VRF in the topology graph.
	// When present the controller appends that VRF's uDT SID as the final
	// segment, producing the multi-tenant carrier:
	//   [uA chain] | dest-locator/uA | uDT(TenantID)
	// Corresponds to Options 1, 2b, and 3 in the multi-tenancy whitepaper.
	TenantID string `json:"tenant_id,omitempty"`

	// Policy is an optional named policy string that maps to a Flex-Algo ID
	// registered via POST /topology/{id}/policies. When set it is resolved to
	// an algo_id before the path engine runs; it takes precedence over
	// Constraints.AlgoID if both are provided.
	// Example values: "carbon-optimized", "latency-sensitive", "backbone-algo128"
	Policy string `json:"policy,omitempty"`
}

// PathRequestConstraints carries optional fine-grained TE constraints.
type PathRequestConstraints struct {
	MinBandwidthBPS uint64 `json:"min_bandwidth_bps,omitempty"`
	MaxLatencyUS    uint32 `json:"max_latency_us,omitempty"`
	Color           uint32 `json:"color,omitempty"`
	AlgoID          uint8  `json:"algo_id,omitempty"`
	ExcludeGroup    uint32 `json:"exclude_group,omitempty"`
}

// PathResult describes a single computed path between two endpoints.
type PathResult struct {
	SrcID       string           `json:"src_id"`
	DstID       string           `json:"dst_id"`
	SrcAddress  string           `json:"src_address,omitempty"`
	DstAddress  string           `json:"dst_address,omitempty"`
	SegmentList srv6.SegmentList  `json:"segment_list"`
	Metric      PathResultMetric `json:"metric"`
	PathID      string           `json:"path_id"`
	// VertexIDs lists the Node vertices traversed in order (for visualization).
	VertexIDs []string `json:"vertex_ids,omitempty"`
	// EdgeIDs lists the LinkEdge IDs traversed in order (for visualization).
	EdgeIDs []string `json:"edge_ids,omitempty"`
}

// PathResultMetric summarises path quality for the caller.
type PathResultMetric struct {
	IGPMetric    uint32 `json:"igp_metric"`
	DelayUS      uint32 `json:"delay_us,omitempty"`
	BottleneckBW uint64 `json:"bottleneck_bw_bps,omitempty"`
	HopCount     int    `json:"hop_count"`
}

// PathResponse is returned by POST /paths/request.
type PathResponse struct {
	WorkloadID string       `json:"workload_id"`
	TopologyID string       `json:"topology_id"`
	Paths      []PathResult `json:"paths"`
	// AllocationState summarises how many paths were FREE vs. SHARED at
	// allocation time — useful for the caller to assess fabric contention.
	AllocationState AllocationSummary `json:"allocation_state"`
}

// AllocationSummary is embedded in PathResponse to give the caller visibility
// into fabric utilisation at the moment of allocation.
type AllocationSummary struct {
	PathsFromFree   int `json:"paths_from_free"`
	PathsFromShared int `json:"paths_from_shared"`
	TotalFreeAfter  int `json:"total_free_after"`
}

// --- Workload lifecycle ---------------------------------------------------

// WorkloadCompleteRequest is the body of POST /paths/{workload_id}/complete.
type WorkloadCompleteRequest struct {
	// Immediate, if true, skips the DRAINING grace period and releases paths
	// back to FREE immediately. Default is false (graceful drain).
	Immediate bool `json:"immediate,omitempty"`
}

// WorkloadStatusResponse is returned by GET /paths/{workload_id}.
type WorkloadStatusResponse struct {
	WorkloadID  string `json:"workload_id"`
	TopologyID  string `json:"topology_id"`
	State       string `json:"state"`
	PathCount   int    `json:"path_count"`
	CreatedAt   string `json:"created_at"`
	// DrainReason is non-empty when the workload is DRAINING or COMPLETE,
	// explaining why it left the ACTIVE state.
	// Values: "workload_complete" | "lease_expired" |
	//         "topology_change" | "topology_replaced"
	DrainReason string `json:"drain_reason,omitempty"`
}

// WorkloadEvent is the payload of each event in the
// GET /paths/{workload_id}/events SSE stream.
type WorkloadEvent struct {
	WorkloadID  string `json:"workload_id"`
	TopologyID  string `json:"topology_id"`
	State       string `json:"state"`
	PathCount   int    `json:"path_count"`
	// DrainReason is set as soon as the workload enters DRAINING, so the
	// subscriber knows why paths were invalidated and can act accordingly.
	DrainReason string `json:"drain_reason,omitempty"`
}

// --- Flows pull endpoint --------------------------------------------------

// FlowsResponse is returned by GET /paths/{workload_id}/flows.
// It carries the encoded SRv6 segment lists for each src→dst flow in the
// workload, ready for host-side programming via setsockopt or iproute2.
type FlowsResponse struct {
	WorkloadID string      `json:"workload_id"`
	TopologyID string      `json:"topology_id"`
	Flows      []FlowEntry `json:"flows"`
}

// FlowEntry describes the SRv6 encapsulation parameters for one src→dst flow.
type FlowEntry struct {
	// SrcNodeID and DstNodeID are the graph vertex IDs of the path endpoints.
	SrcNodeID string `json:"src_node_id"`
	DstNodeID string `json:"dst_node_id"`

	// PathID is the allocation table identifier for this flow.
	PathID string `json:"path_id"`

	// SegmentList is the final packed SID list. len==1 means a single uSID
	// container (≤6 original uSIDs); len>1 means multiple containers.
	SegmentList []string `json:"segment_list"`

	// EncapFlavor is "H.Encaps.Red" (single container) or "H.Encaps" (multiple).
	EncapFlavor string `json:"encap_flavor"`

	// OuterDA is the outer IPv6 destination address for the encapsulated packet.
	// Set this as the IPv6 DA when sending traffic for this flow.
	// Empty when SegmentList is empty.
	OuterDA string `json:"outer_da,omitempty"`

	// SRHRaw is the base64-encoded raw Type-4 IPv6 Routing Header. Present
	// only when len(SegmentList) > 1 (i.e. an SRH is required). The host
	// should attach this header to outgoing packets alongside the outer DA.
	SRHRaw string `json:"srh_raw,omitempty"` // base64(RFC 8754 SRH bytes)
}

// --- Allocation table -----------------------------------------------------

// AllocationTableResponse is returned by GET /paths/state.
type AllocationTableResponse struct {
	Topologies []interface{} `json:"topologies"` // []allocation.TableSnapshot
}

// --- Policy API -----------------------------------------------------------

// PolicyEntry maps a human-readable policy name to a Flex-Algo ID.
// Callers can then reference the policy by name in PathRequest.Policy rather
// than embedding numeric algo IDs in their job specifications.
type PolicyEntry struct {
	// Name is the human-readable identifier, e.g. "carbon-optimized".
	Name string `json:"name"`
	// AlgoID is the Flex-Algo identifier (typically 128–255) that the policy
	// resolves to. AlgoID 0 removes the policy.
	AlgoID uint8 `json:"algo_id"`
}

// PoliciesRequest is the body for POST /topology/{id}/policies.
// The supplied entries are merged into (or removed from) the existing mapping.
// To remove a policy, include it with algo_id=0.
type PoliciesRequest struct {
	Policies []PolicyEntry `json:"policies"`
}

// PoliciesResponse is returned by GET /topology/{id}/policies.
type PoliciesResponse struct {
	TopologyID string        `json:"topology_id"`
	Policies   []PolicyEntry `json:"policies"`
}

// --- Error response -------------------------------------------------------

// ErrorResponse is the standard error envelope returned on 4xx/5xx.
type ErrorResponse struct {
	Error  string   `json:"error"`
	Detail []string `json:"detail,omitempty"`
}
