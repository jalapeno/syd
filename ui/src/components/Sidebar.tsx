import { useState, useEffect } from 'react';
import {
  Network,
  Route,
  Layers,
  Activity,
  ChevronRight,
  X,
  Server,
} from 'lucide-react';
import { api } from '../api/client';
import type {
  GraphNode,
  GraphLink,
  PathResponse,
  PolicyEntry,
} from '../types/api';
import type { SidebarView } from '../App';
import WorkloadList from './WorkloadList';

interface SidebarProps {
  view: SidebarView;
  onViewChange: (view: SidebarView) => void;
  detailData: GraphNode | GraphLink | null;
  selectedNodes: GraphNode[];
  pathResponse: PathResponse | null;
  topologyId: string;
  onTopologyChange: (id: string) => void;
  onClearSelection: () => void;
  onPathResponse: (resp: PathResponse) => void;
}

export default function Sidebar({
  view,
  onViewChange,
  detailData,
  selectedNodes,
  pathResponse,
  topologyId,
  onTopologyChange,
  onClearSelection,
}: SidebarProps) {
  const [topologies, setTopologies] = useState<string[]>([]);
  const [policies, setPolicies] = useState<PolicyEntry[]>([]);

  useEffect(() => {
    api.listTopologies().then((r) => {
      setTopologies(r.topology_ids || []);
      if (!topologyId && r.topology_ids?.length) {
        onTopologyChange(r.topology_ids[0]);
      }
    }).catch(() => {});
  }, []);

  useEffect(() => {
    if (topologyId) {
      api.getPolicies(topologyId).then((r) => setPolicies(r.policies || [])).catch(() => {});
    }
  }, [topologyId]);

  return (
    <aside className="w-80 h-full bg-kraken-navy border-r border-kraken-border flex flex-col">
      {/* Header */}
      <div className="p-4 border-b border-kraken-border">
        <div className="flex items-center gap-2">
          <Network className="w-5 h-5 text-kraken-ice" />
          <h1 className="text-lg font-semibold text-kraken-frost tracking-tight">
            syd
          </h1>
          <span className="text-xs text-kraken-muted ml-auto font-mono">
            SRv6 SDN
          </span>
        </div>
      </div>

      {/* Topology Selector */}
      <div className="p-3 border-b border-kraken-border">
        <label className="text-xs font-medium text-kraken-muted uppercase tracking-wider">
          Topology
        </label>
        <select
          value={topologyId}
          onChange={(e) => onTopologyChange(e.target.value)}
          className="mt-1 w-full bg-kraken-dark border border-kraken-border rounded px-3 py-1.5 text-sm text-kraken-frost focus:outline-none focus:border-kraken-ice transition-colors"
        >
          {topologies.length === 0 && (
            <option value="">No topologies available</option>
          )}
          {topologies.map((id) => (
            <option key={id} value={id}>
              {id}
            </option>
          ))}
        </select>
      </div>

      {/* Navigation */}
      <nav className="p-2 border-b border-kraken-border">
        <NavItem
          icon={<Layers className="w-4 h-4" />}
          label="Topology"
          active={view === 'menu'}
          onClick={() => onViewChange('menu')}
        />
        <NavItem
          icon={<Route className="w-4 h-4" />}
          label="Paths"
          active={view === 'paths'}
          onClick={() => onViewChange('paths')}
          badge={pathResponse?.paths?.length}
        />
        <NavItem
          icon={<Activity className="w-4 h-4" />}
          label="Workloads"
          active={view === 'workloads'}
          onClick={() => onViewChange('workloads')}
        />
      </nav>

      {/* Content Area */}
      <div className="flex-1 overflow-y-auto p-3">
        {view === 'menu' && (
          <MenuContent
            selectedNodes={selectedNodes}
            policies={policies}
            onClearSelection={onClearSelection}
          />
        )}
        {view === 'detail' && detailData && (
          <DetailContent
            data={detailData}
            onClose={() => onViewChange('menu')}
          />
        )}
        {view === 'paths' && pathResponse && (
          <PathsContent response={pathResponse} />
        )}
        {view === 'workloads' && (
          <WorkloadList topologyId={topologyId} />
        )}
      </div>

      {/* Footer */}
      <div className="p-3 border-t border-kraken-border">
        <div className="flex items-center gap-2 text-xs text-kraken-muted">
          <div className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
          <span>Connected</span>
          <span className="ml-auto font-mono">:8080</span>
        </div>
      </div>
    </aside>
  );
}

function NavItem({
  icon,
  label,
  active,
  onClick,
  badge,
}: {
  icon: React.ReactNode;
  label: string;
  active: boolean;
  onClick: () => void;
  badge?: number;
}) {
  return (
    <button
      onClick={onClick}
      className={`w-full flex items-center gap-2 px-3 py-2 rounded text-sm transition-all ${
        active
          ? 'bg-kraken-dark text-kraken-ice border border-kraken-border'
          : 'text-kraken-muted hover:text-kraken-frost hover:bg-kraken-dark/50'
      }`}
    >
      {icon}
      <span>{label}</span>
      {badge !== undefined && badge > 0 && (
        <span className="ml-auto bg-kraken-ice/20 text-kraken-ice text-xs px-1.5 py-0.5 rounded-full font-mono">
          {badge}
        </span>
      )}
      <ChevronRight className="w-3 h-3 ml-auto opacity-40" />
    </button>
  );
}

function MenuContent({
  selectedNodes,
  policies,
  onClearSelection,
}: {
  selectedNodes: GraphNode[];
  policies: PolicyEntry[];
  onClearSelection: () => void;
}) {
  return (
    <div className="space-y-4">
      {/* Selection info */}
      <div>
        <h3 className="text-xs font-medium text-kraken-muted uppercase tracking-wider mb-2">
          Selection
        </h3>
        {selectedNodes.length === 0 ? (
          <p className="text-sm text-kraken-muted/70">
            Click a node to select it. Shift+click to select multiple nodes for
            path calculation.
          </p>
        ) : (
          <div className="space-y-1">
            {selectedNodes.map((n, i) => (
              <div
                key={n.id}
                className="flex items-center gap-2 px-2 py-1 bg-kraken-dark rounded text-sm"
              >
                <Server className="w-3 h-3 text-kraken-ice" />
                <span className="font-mono text-xs">{n.name || n.id}</span>
                <span className="ml-auto text-xs text-kraken-muted">
                  #{i + 1}
                </span>
              </div>
            ))}
            <button
              onClick={onClearSelection}
              className="mt-2 text-xs text-kraken-red hover:text-kraken-red-dim transition-colors"
            >
              Clear selection
            </button>
          </div>
        )}
      </div>

      {/* Policies */}
      {policies.length > 0 && (
        <div>
          <h3 className="text-xs font-medium text-kraken-muted uppercase tracking-wider mb-2">
            Policies
          </h3>
          <div className="space-y-1">
            {policies.map((p) => (
              <div
                key={p.name}
                className="flex items-center justify-between px-2 py-1 bg-kraken-dark rounded text-sm"
              >
                <span>{p.name}</span>
                <span className="font-mono text-xs text-kraken-muted">
                  algo {p.algo_id}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Instructions */}
      <div className="mt-4 p-3 bg-kraken-dark/50 rounded border border-kraken-border/50">
        <h4 className="text-xs font-medium text-kraken-ice mb-1">Quick Start</h4>
        <ul className="text-xs text-kraken-muted space-y-1">
          <li>- Hover nodes/links for info</li>
          <li>- Click to see details</li>
          <li>- Shift+click to multi-select</li>
          <li>- Select 2+ nodes for path calc</li>
          <li>- Drag nodes to reposition</li>
          <li>- Scroll to zoom</li>
        </ul>
      </div>
    </div>
  );
}

function DetailContent({
  data,
  onClose,
}: {
  data: GraphNode | GraphLink;
  onClose: () => void;
}) {
  const isNode = 'id' in data && !('source' in data);

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-semibold text-kraken-ice">
          {isNode ? 'Node' : 'Link'} Detail
        </h3>
        <button
          onClick={onClose}
          className="p-1 rounded hover:bg-kraken-dark transition-colors"
        >
          <X className="w-4 h-4 text-kraken-muted" />
        </button>
      </div>
      <div className="space-y-2">
        {Object.entries(data).map(([key, value]) => {
          if (key === 'x' || key === 'y' || key === 'fx' || key === 'fy' || key === 'index' || key === 'vx' || key === 'vy')
            return null;
          if (typeof value === 'object' && value !== null) {
            return (
              <div key={key} className="px-2 py-1.5 bg-kraken-dark rounded">
                <span className="text-xs text-kraken-muted">{key}</span>
                <pre className="text-xs text-kraken-frost font-mono mt-0.5 whitespace-pre-wrap">
                  {JSON.stringify(value, null, 2)}
                </pre>
              </div>
            );
          }
          return (
            <div
              key={key}
              className="flex items-center justify-between px-2 py-1.5 bg-kraken-dark rounded"
            >
              <span className="text-xs text-kraken-muted">{key}</span>
              <span className="text-xs text-kraken-frost font-mono">
                {String(value)}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function PathsContent({ response }: { response: PathResponse }) {
  return (
    <div>
      <h3 className="text-sm font-semibold text-kraken-ice mb-2">
        Computed Paths
      </h3>
      <div className="mb-3 space-y-1">
        <div className="flex justify-between text-xs text-kraken-muted">
          <span>Workload</span>
          <span className="font-mono">{response.workload_id}</span>
        </div>
        <div className="flex justify-between text-xs text-kraken-muted">
          <span>Paths</span>
          <span className="font-mono">{response.paths.length}</span>
        </div>
        <div className="flex justify-between text-xs text-kraken-muted">
          <span>From free</span>
          <span className="font-mono">
            {response.allocation_state.paths_from_free}
          </span>
        </div>
      </div>
      <div className="space-y-3 max-h-[calc(100vh-320px)] overflow-y-auto">
        {response.paths.map((p, i) => (
          <div
            key={p.path_id || i}
            className="p-3 bg-kraken-dark rounded border border-kraken-border/50"
          >
            {/* Flow direction */}
            <div className="flex items-center gap-1 text-xs mb-2">
              <span className="font-mono text-kraken-ice">{p.src_id}</span>
              <ChevronRight className="w-3 h-3 text-kraken-muted" />
              <span className="font-mono text-kraken-ice">{p.dst_id}</span>
            </div>

            {/* Metrics */}
            <div className="flex gap-3 text-xs text-kraken-muted mb-2">
              <span>hops: {p.metric.hop_count}</span>
              <span>metric: {p.metric.igp_metric}</span>
              {p.metric.delay_us ? <span>delay: {p.metric.delay_us}us</span> : null}
            </div>

            {/* Segment List */}
            {p.segment_list && (
              <div className="mt-2 pt-2 border-t border-kraken-border/30">
                <div className="flex items-center gap-2 mb-1.5">
                  <span className="text-[10px] uppercase tracking-wider text-kraken-muted font-medium">
                    {p.segment_list.encap}
                  </span>
                  <span className="text-[10px] bg-kraken-mid/50 text-kraken-ice-dim px-1.5 py-0.5 rounded font-mono">
                    {p.segment_list.flavor}
                  </span>
                </div>
                <div className="space-y-1">
                  {p.segment_list.sids.map((sid, j) => (
                    <div
                      key={j}
                      className="flex items-center gap-2 px-2 py-1 bg-kraken-navy rounded"
                    >
                      <span className="text-[10px] text-kraken-muted w-4">{j}</span>
                      <span className="text-xs font-mono text-kraken-frost break-all">
                        {sid}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
