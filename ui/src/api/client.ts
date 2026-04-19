import type {
  TopologyListResponse,
  TopologyResponse,
  TopologyNodesResponse,
  PathRequest,
  PathResponse,
  WorkloadStatusResponse,
  FlowsResponse,
  PoliciesResponse,
  TopologyGraph,
} from '../types/api';

const BASE = '';

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${url}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`${res.status}: ${body}`);
  }
  return res.json();
}

export const api = {
  // Topology
  listTopologies: () => fetchJSON<TopologyListResponse>('/topology'),

  getTopology: (id: string) => fetchJSON<TopologyResponse>(`/topology/${id}`),

  getTopologyNodes: (id: string) =>
    fetchJSON<TopologyNodesResponse>(`/topology/${id}/nodes`),

  getTopologyGraph: (id: string) =>
    fetchJSON<TopologyGraph>(`/topology/${id}/graph`),

  getPolicies: (id: string) =>
    fetchJSON<PoliciesResponse>(`/topology/${id}/policies`),

  // Paths
  requestPaths: (req: PathRequest) =>
    fetchJSON<PathResponse>('/paths/request', {
      method: 'POST',
      body: JSON.stringify(req),
    }),

  getWorkloadStatus: (id: string) =>
    fetchJSON<WorkloadStatusResponse>(`/paths/${id}`),

  getWorkloadFlows: (id: string) => fetchJSON<FlowsResponse>(`/paths/${id}/flows`),

  completeWorkload: (id: string, immediate = false) =>
    fetchJSON<void>(`/paths/${id}/complete`, {
      method: 'POST',
      body: JSON.stringify({ immediate }),
    }),

  heartbeat: (id: string) =>
    fetchJSON<void>(`/paths/${id}/heartbeat`, { method: 'POST' }),
};
