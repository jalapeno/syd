import { useState } from 'react';
import { Zap, ChevronDown } from 'lucide-react';
import { api } from '../api/client';
import type { GraphNode, PathResponse } from '../types/api';

interface PathRequestPanelProps {
  topologyId: string;
  selectedNodes: GraphNode[];
  onPathResponse: (resp: PathResponse) => void;
}

const DISJOINTNESS_OPTIONS = [
  { value: 'none', label: 'None' },
  { value: 'link', label: 'Link-disjoint' },
  { value: 'node', label: 'Node-disjoint' },
  { value: 'srlg', label: 'SRLG-disjoint' },
];

const PAIRING_OPTIONS = [
  { value: 'bidir_paired', label: 'Bidirectional Paired (AI All-Reduce)' },
  { value: 'all_directed', label: 'All Directed (N*(N-1) flows)' },
];

export default function PathRequestPanel({
  topologyId,
  selectedNodes,
  onPathResponse,
}: PathRequestPanelProps) {
  const [disjointness, setDisjointness] = useState('link');
  const [pairingMode, setPairingMode] = useState('bidir_paired');
  const [segListMode, setSegListMode] = useState('ua');
  const [algoId, setAlgoId] = useState(0);
  const [policy, setPolicy] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showAdvanced, setShowAdvanced] = useState(false);

  const handleRequest = async () => {
    if (!topologyId || selectedNodes.length < 2) return;
    setLoading(true);
    setError(null);
    try {
      const resp = await api.requestPaths({
        topology_id: topologyId,
        workload_id: `ui-${Date.now()}`,
        endpoints: selectedNodes.map((n) => ({ id: n.id })),
        disjointness,
        pairing_mode: pairingMode,
        segment_list_mode: segListMode || undefined, // omit for classic
        constraints: algoId ? { algo_id: algoId } : undefined,
        policy: policy || undefined,
      });
      onPathResponse(resp);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Request failed');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="absolute bottom-4 left-1/2 -translate-x-1/2 z-40">
      <div className="bg-kraken-navy/95 backdrop-blur-sm border border-kraken-border rounded-xl shadow-2xl px-4 py-3 min-w-[480px]">
        {/* Header */}
        <div className="flex items-center gap-2 mb-2">
          <Zap className="w-4 h-4 text-kraken-ice" />
          <h3 className="text-sm font-semibold text-kraken-frost">
            Calculate Paths
          </h3>
          <span className="ml-auto text-xs text-kraken-muted font-mono">
            {selectedNodes.length} endpoints
          </span>
        </div>

        {/* Options + button row */}
        <div className="flex items-end gap-3">
          <div className="flex-1 min-w-0">
            <label className="text-[10px] text-kraken-muted block mb-0.5">Disjointness</label>
            <select
              value={disjointness}
              onChange={(e) => setDisjointness(e.target.value)}
              className="w-full bg-kraken-dark border border-kraken-border rounded px-2 py-1.5 text-sm text-kraken-frost focus:outline-none focus:border-kraken-ice transition-colors"
            >
              {DISJOINTNESS_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </div>
          <div className="flex-1 min-w-0">
            <label className="text-[10px] text-kraken-muted block mb-0.5">Pairing</label>
            <select
              value={pairingMode}
              onChange={(e) => setPairingMode(e.target.value)}
              className="w-full bg-kraken-dark border border-kraken-border rounded px-2 py-1.5 text-sm text-kraken-frost focus:outline-none focus:border-kraken-ice transition-colors"
            >
              {PAIRING_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </div>
          <div className="shrink-0">
            <label className="text-[10px] text-kraken-muted block mb-0.5">SID Mode</label>
            <div className="flex rounded overflow-hidden border border-kraken-border">
              {[
                { value: 'ua', label: 'uA' },
                { value: 'un', label: 'uN' },
                { value: '', label: 'Classic' },
              ].map((opt) => (
                <button
                  key={opt.value}
                  onClick={() => setSegListMode(opt.value)}
                  className={`px-2.5 py-1 text-xs font-medium transition-colors ${
                    segListMode === opt.value
                      ? 'bg-kraken-ice text-kraken-deep'
                      : 'bg-kraken-dark text-kraken-muted hover:text-kraken-frost'
                  }`}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          </div>
          <button
            onClick={() => setShowAdvanced(!showAdvanced)}
            className="flex items-center gap-0.5 text-xs text-kraken-muted hover:text-kraken-frost transition-colors pb-1.5 shrink-0"
          >
            <ChevronDown
              className={`w-3 h-3 transition-transform ${showAdvanced ? 'rotate-180' : ''}`}
            />
            More
          </button>
          <button
            onClick={handleRequest}
            disabled={loading || !topologyId}
            className="shrink-0 px-5 py-1.5 rounded-lg bg-kraken-ice text-kraken-deep font-semibold text-sm hover:bg-kraken-frost transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? 'Computing...' : 'Request Paths'}
          </button>
        </div>

        {/* Advanced constraints — expandable */}
        {showAdvanced && (
          <div className="flex gap-3 mt-2 pt-2 border-t border-kraken-border/50">
            <div className="flex-1">
              <label className="text-[10px] text-kraken-muted block mb-0.5">Flex-Algo ID</label>
              <input
                type="number"
                min={0}
                max={255}
                value={algoId}
                onChange={(e) => setAlgoId(Number(e.target.value))}
                className="w-full bg-kraken-dark border border-kraken-border rounded px-2 py-1 text-sm text-kraken-frost font-mono focus:outline-none focus:border-kraken-ice"
              />
            </div>
            <div className="flex-1">
              <label className="text-[10px] text-kraken-muted block mb-0.5">Policy Name</label>
              <input
                type="text"
                value={policy}
                onChange={(e) => setPolicy(e.target.value)}
                placeholder="e.g. carbon-optimized"
                className="w-full bg-kraken-dark border border-kraken-border rounded px-2 py-1 text-sm text-kraken-frost focus:outline-none focus:border-kraken-ice placeholder:text-kraken-muted/40"
              />
            </div>
          </div>
        )}

        {error && (
          <div className="mt-2 text-xs text-kraken-red bg-kraken-red/10 border border-kraken-red/20 rounded px-2 py-1">
            {error}
          </div>
        )}
      </div>
    </div>
  );
}
