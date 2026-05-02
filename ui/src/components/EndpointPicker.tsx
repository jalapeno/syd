import { useState, useEffect, useMemo } from 'react';
import { Search, CheckSquare, Square, ChevronRight, ChevronDown } from 'lucide-react';
import { api } from '../api/client';
import type { GraphNode, TopologyGraph } from '../types/api';

interface EndpointPickerProps {
  topologyId: string;
  selectedNodes: GraphNode[];
  onSelectionChange: (nodes: GraphNode[]) => void;
}

interface LeafGroup {
  leafId: string;
  leafName: string;
  endpoints: GraphNode[];
}

export default function EndpointPicker({
  topologyId,
  selectedNodes,
  onSelectionChange,
}: EndpointPickerProps) {
  const [graph, setGraph] = useState<TopologyGraph | null>(null);
  const [search, setSearch] = useState('');
  const [expandedLeaves, setExpandedLeaves] = useState<Set<string>>(new Set());

  useEffect(() => {
    if (!topologyId) return;
    api.getTopologyGraph(topologyId).then(setGraph).catch(() => {});
  }, [topologyId]);

  // Build leaf-grouped endpoint list from graph data
  const leafGroups = useMemo(() => {
    if (!graph) return [];

    // Build adjacency
    const adjacency = new Map<string, Set<string>>();
    for (const n of graph.nodes) adjacency.set(n.id, new Set());
    for (const l of graph.links) {
      const src = typeof l.source === 'string' ? l.source : l.source.id;
      const dst = typeof l.target === 'string' ? l.target : l.target.id;
      adjacency.get(src)?.add(dst);
      adjacency.get(dst)?.add(src);
    }

    // Find endpoints and their parent leaves using structural detection
    const endpoints = graph.nodes.filter((n) => n.type === 'endpoint');
    const endpointIds = new Set(endpoints.map((n) => n.id));

    // Leaves = nodes adjacent to at least one endpoint
    const leafIds = new Set<string>();
    for (const n of graph.nodes) {
      if (endpointIds.has(n.id)) continue;
      const neighbors = adjacency.get(n.id) || new Set();
      if (Array.from(neighbors).some((nb) => endpointIds.has(nb))) {
        leafIds.add(n.id);
      }
    }

    // Group endpoints by parent leaf
    const groups = new Map<string, GraphNode[]>();
    const ungrouped: GraphNode[] = [];
    for (const ep of endpoints) {
      const neighbors = adjacency.get(ep.id) || new Set();
      const parentLeaf = Array.from(neighbors).find((nb) => leafIds.has(nb));
      if (parentLeaf) {
        if (!groups.has(parentLeaf)) groups.set(parentLeaf, []);
        groups.get(parentLeaf)!.push(ep);
      } else {
        ungrouped.push(ep);
      }
    }

    const nodeMap = new Map(graph.nodes.map((n) => [n.id, n]));
    const result: LeafGroup[] = [];

    // Sort leaves by ID
    const sortedLeafIds = Array.from(groups.keys()).sort();
    for (const leafId of sortedLeafIds) {
      const eps = groups.get(leafId)!;
      eps.sort((a, b) => a.id.localeCompare(b.id));
      result.push({
        leafId,
        leafName: nodeMap.get(leafId)?.name || leafId,
        endpoints: eps,
      });
    }

    if (ungrouped.length > 0) {
      ungrouped.sort((a, b) => a.id.localeCompare(b.id));
      result.push({ leafId: '__ungrouped__', leafName: 'Ungrouped', endpoints: ungrouped });
    }

    return result;
  }, [graph]);

  const selectedIds = useMemo(() => new Set(selectedNodes.map((n) => n.id)), [selectedNodes]);

  // Filter by search
  const filteredGroups = useMemo(() => {
    if (!search.trim()) return leafGroups;
    const q = search.toLowerCase();
    return leafGroups
      .map((g) => ({
        ...g,
        endpoints: g.endpoints.filter(
          (ep) =>
            ep.id.toLowerCase().includes(q) ||
            (ep.name || '').toLowerCase().includes(q) ||
            g.leafName.toLowerCase().includes(q)
        ),
      }))
      .filter((g) => g.endpoints.length > 0);
  }, [leafGroups, search]);

  const totalEndpoints = leafGroups.reduce((s, g) => s + g.endpoints.length, 0);

  const toggleEndpoint = (ep: GraphNode) => {
    if (selectedIds.has(ep.id)) {
      onSelectionChange(selectedNodes.filter((n) => n.id !== ep.id));
    } else {
      onSelectionChange([...selectedNodes, ep]);
    }
  };

  const toggleLeafGroup = (group: LeafGroup) => {
    const allSelected = group.endpoints.every((ep) => selectedIds.has(ep.id));
    if (allSelected) {
      // Deselect all in this group
      const groupIds = new Set(group.endpoints.map((ep) => ep.id));
      onSelectionChange(selectedNodes.filter((n) => !groupIds.has(n.id)));
    } else {
      // Select all in this group
      const existing = new Set(selectedIds);
      const toAdd = group.endpoints.filter((ep) => !existing.has(ep.id));
      onSelectionChange([...selectedNodes, ...toAdd]);
    }
  };

  const toggleExpanded = (leafId: string) => {
    setExpandedLeaves((prev) => {
      const next = new Set(prev);
      if (next.has(leafId)) next.delete(leafId);
      else next.add(leafId);
      return next;
    });
  };

  const selectAll = () => {
    const allEps = leafGroups.flatMap((g) => g.endpoints);
    onSelectionChange(allEps);
  };

  const clearAll = () => {
    onSelectionChange([]);
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold text-kraken-frost">
          Select Endpoints
        </h3>
        <span className="text-xs text-kraken-muted font-mono">
          {selectedIds.size}/{totalEndpoints}
        </span>
      </div>

      {/* Search */}
      <div className="relative">
        <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-kraken-muted" />
        <input
          type="text"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Filter endpoints..."
          className="w-full bg-kraken-dark border border-kraken-border rounded pl-7 pr-3 py-1.5 text-sm text-kraken-frost focus:outline-none focus:border-kraken-ice placeholder:text-kraken-muted/40"
        />
      </div>

      {/* Bulk actions */}
      <div className="flex gap-2">
        <button
          onClick={selectAll}
          className="flex-1 px-2 py-1 text-xs text-kraken-muted hover:text-kraken-frost bg-kraken-dark border border-kraken-border rounded hover:border-kraken-ice transition-colors"
        >
          Select All
        </button>
        <button
          onClick={clearAll}
          className="flex-1 px-2 py-1 text-xs text-kraken-muted hover:text-kraken-frost bg-kraken-dark border border-kraken-border rounded hover:border-kraken-ice transition-colors"
        >
          Clear
        </button>
      </div>

      {/* Leaf-grouped list */}
      <div className="space-y-1 max-h-[calc(100vh-340px)] overflow-y-auto">
        {filteredGroups.map((group) => {
          const groupSelected = group.endpoints.filter((ep) => selectedIds.has(ep.id)).length;
          const allSelected = groupSelected === group.endpoints.length;
          const someSelected = groupSelected > 0 && !allSelected;
          const expanded = expandedLeaves.has(group.leafId);

          return (
            <div key={group.leafId} className="rounded border border-kraken-border/50 overflow-hidden">
              {/* Leaf header */}
              <div
                className="flex items-center gap-1.5 px-2 py-1.5 bg-kraken-dark/50 cursor-pointer hover:bg-kraken-dark transition-colors"
                onClick={() => toggleExpanded(group.leafId)}
              >
                {expanded
                  ? <ChevronDown className="w-3 h-3 text-kraken-muted shrink-0" />
                  : <ChevronRight className="w-3 h-3 text-kraken-muted shrink-0" />
                }
                <button
                  onClick={(e) => { e.stopPropagation(); toggleLeafGroup(group); }}
                  className="shrink-0"
                >
                  {allSelected
                    ? <CheckSquare className="w-3.5 h-3.5 text-kraken-ice" />
                    : someSelected
                    ? <CheckSquare className="w-3.5 h-3.5 text-kraken-ice/50" />
                    : <Square className="w-3.5 h-3.5 text-kraken-muted" />
                  }
                </button>
                <span className="text-xs font-mono text-kraken-frost truncate flex-1">
                  {group.leafName}
                </span>
                <span className="text-[10px] text-kraken-muted font-mono shrink-0">
                  {groupSelected}/{group.endpoints.length}
                </span>
              </div>

              {/* Endpoint list */}
              {expanded && (
                <div className="px-1 py-0.5 space-y-0 bg-kraken-deep/30">
                  {group.endpoints.map((ep) => {
                    const selected = selectedIds.has(ep.id);
                    return (
                      <div
                        key={ep.id}
                        onClick={() => toggleEndpoint(ep)}
                        className={`flex items-center gap-1.5 px-2 py-0.5 rounded cursor-pointer transition-colors ${
                          selected
                            ? 'bg-kraken-ice/10 hover:bg-kraken-ice/15'
                            : 'hover:bg-kraken-dark/50'
                        }`}
                      >
                        {selected
                          ? <CheckSquare className="w-3 h-3 text-kraken-ice shrink-0" />
                          : <Square className="w-3 h-3 text-kraken-muted shrink-0" />
                        }
                        <span className={`text-xs font-mono truncate ${
                          selected ? 'text-kraken-ice' : 'text-kraken-muted'
                        }`}>
                          {ep.name || ep.id}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          );
        })}
        {filteredGroups.length === 0 && (
          <div className="text-xs text-kraken-muted text-center py-4">
            {graph ? 'No endpoints found' : 'Loading...'}
          </div>
        )}
      </div>
    </div>
  );
}
