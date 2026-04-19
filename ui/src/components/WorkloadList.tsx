import { useState, useEffect } from 'react';
import { Activity, RefreshCw, Trash2 } from 'lucide-react';
import { api } from '../api/client';
import type { WorkloadStatusResponse } from '../types/api';

interface WorkloadListProps {
  topologyId: string;
}

export default function WorkloadList({ topologyId }: WorkloadListProps) {
  const [workloads, setWorkloads] = useState<WorkloadStatusResponse[]>([]);
  const [loading, setLoading] = useState(false);

  const fetchWorkloads = async () => {
    // The API doesn't have a "list all workloads" endpoint in apitypes,
    // but we can use GET /paths/state to get the allocation table.
    // For now we'll show what we can fetch or display a placeholder.
    setLoading(true);
    try {
      // Attempt to load allocation state
      const resp = await fetch('/paths/state');
      if (resp.ok) {
        const data = await resp.json();
        // Extract workloads from allocation table if available
        if (data.topologies) {
          const wls: WorkloadStatusResponse[] = [];
          for (const topo of data.topologies) {
            if (topo.workloads) {
              for (const w of topo.workloads) {
                wls.push(w);
              }
            }
          }
          setWorkloads(wls);
        }
      }
    } catch {
      // API not available
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchWorkloads();
  }, [topologyId]);

  const handleComplete = async (workloadId: string) => {
    try {
      await api.completeWorkload(workloadId);
      fetchWorkloads();
    } catch {
      // handle error
    }
  };

  const stateColor = (state: string) => {
    switch (state?.toLowerCase()) {
      case 'exclusive':
      case 'active':
        return 'text-green-400';
      case 'shared':
        return 'text-blue-400';
      case 'draining':
        return 'text-yellow-400';
      case 'complete':
        return 'text-kraken-muted';
      default:
        return 'text-kraken-muted';
    }
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-semibold text-kraken-ice flex items-center gap-2">
          <Activity className="w-4 h-4" />
          Active Workloads
        </h3>
        <button
          onClick={fetchWorkloads}
          disabled={loading}
          className="p-1 rounded hover:bg-kraken-dark transition-colors"
        >
          <RefreshCw
            className={`w-3.5 h-3.5 text-kraken-muted ${loading ? 'animate-spin' : ''}`}
          />
        </button>
      </div>

      {workloads.length === 0 ? (
        <div className="text-sm text-kraken-muted/70 p-3 bg-kraken-dark/50 rounded border border-kraken-border/50">
          No active workloads. Select nodes and request paths to create one.
        </div>
      ) : (
        <div className="space-y-2">
          {workloads.map((w) => (
            <div
              key={w.workload_id}
              className="p-2 bg-kraken-dark rounded border border-kraken-border/50"
            >
              <div className="flex items-center justify-between">
                <span className="text-xs font-mono text-kraken-frost truncate max-w-[180px]">
                  {w.workload_id}
                </span>
                <button
                  onClick={() => handleComplete(w.workload_id)}
                  className="p-1 rounded hover:bg-kraken-red/20 transition-colors"
                  title="Complete workload"
                >
                  <Trash2 className="w-3 h-3 text-kraken-muted hover:text-kraken-red" />
                </button>
              </div>
              <div className="flex items-center gap-2 mt-1 text-xs">
                <span className={stateColor(w.state)}>{w.state}</span>
                <span className="text-kraken-muted">
                  {w.path_count} path{w.path_count !== 1 ? 's' : ''}
                </span>
              </div>
              {w.drain_reason && (
                <div className="mt-1 text-[10px] text-yellow-500/70">
                  drain: {w.drain_reason}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
