// Types mirroring the Go apitypes package

export interface NodeSummary {
  id: string;
  name?: string;
}

export interface TopologyNodesResponse {
  topology_id: string;
  nodes: NodeSummary[];
}

export interface TopologyListResponse {
  topology_ids: string[];
}

export interface TopologyResponse {
  topology_id: string;
  description?: string;
  stats: Record<string, unknown>;
}

export interface EndpointSpec {
  id?: string;
  address?: string;
  role?: string;
  metadata?: Record<string, string>;
}

export interface PathRequestConstraints {
  min_bandwidth_bps?: number;
  max_latency_us?: number;
  color?: number;
  algo_id?: number;
  exclude_group?: number;
}

export interface PathRequest {
  topology_id: string;
  workload_id: string;
  endpoints: EndpointSpec[];
  disjointness?: string;
  sharing?: string;
  service_level?: string;
  constraints?: PathRequestConstraints;
  pairing_mode?: string;
  segment_list_mode?: string; // "ua" | "un" | "" (classic 32-bit)
  lease_duration_seconds?: number;
  tenant_id?: string;
  policy?: string;
}

export interface PathResultMetric {
  igp_metric: number;
  delay_us?: number;
  bottleneck_bw_bps?: number;
  hop_count: number;
}

export interface SegmentList {
  encap: string;
  flavor: string;
  sids: string[];
}

export interface PathResult {
  src_id: string;
  dst_id: string;
  src_address?: string;
  dst_address?: string;
  segment_list: SegmentList;
  metric: PathResultMetric;
  path_id: string;
  vertex_ids?: string[];
  edge_ids?: string[];
}

export interface AllocationSummary {
  paths_from_free: number;
  paths_from_shared: number;
  total_free_after: number;
}

export interface PathResponse {
  workload_id: string;
  topology_id: string;
  paths: PathResult[];
  allocation_state: AllocationSummary;
}

export interface WorkloadStatusResponse {
  workload_id: string;
  topology_id: string;
  state: string;
  path_count: number;
  created_at: string;
  drain_reason?: string;
}

export interface FlowEntry {
  src_node_id: string;
  dst_node_id: string;
  path_id: string;
  segment_list: string[];
  encap_flavor: string;
  outer_da?: string;
  srh_raw?: string;
}

export interface FlowsResponse {
  workload_id: string;
  topology_id: string;
  flows: FlowEntry[];
}

export interface PolicyEntry {
  name: string;
  algo_id: number;
}

export interface PoliciesResponse {
  topology_id: string;
  policies: PolicyEntry[];
}

// UI-specific graph types
export interface GraphNode {
  id: string;
  name?: string;
  type?: string; // "node" | "endpoint" | "prefix" | "vrf"
  subtype?: string; // "external_bgp" for eBGP peer nodes
  x?: number;
  y?: number;
  fx?: number | null;
  fy?: number | null;
  // Computed properties
  selected?: boolean;
  highlighted?: boolean;
  locators?: string[];
}

export interface GraphLink {
  id: string;
  source: string | GraphNode;
  target: string | GraphNode;
  type?: string; // "physical" | "igp_adjacency" | "bgp_session" | "ownership" | "attachment"
  metric?: number;
  bandwidth?: number;
  delay?: number;
  highlighted?: boolean;
}

export interface TopologyGraph {
  nodes: GraphNode[];
  links: GraphLink[];
}
