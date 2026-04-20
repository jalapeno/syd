import { useRef, useLayoutEffect, useState } from 'react';
import type { PopoverData } from '../App';
import type { GraphNode, GraphLink } from '../types/api';

interface PopoverProps {
  data: PopoverData;
}

export default function Popover({ data }: PopoverProps) {
  const { x, y, type, data: item } = data;
  const ref = useRef<HTMLDivElement>(null);
  const [offset, setOffset] = useState({ left: x + 12, top: y - 10 });

  useLayoutEffect(() => {
    if (!ref.current) return;
    const rect = ref.current.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;

    let left = x + 12;
    let top = y - 10;

    // Flip to left side if overflowing right
    if (left + rect.width > vw - 8) {
      left = x - rect.width - 12;
    }
    // Shift up if overflowing bottom
    if (top + rect.height > vh - 8) {
      top = vh - rect.height - 8;
    }
    // Prevent going off top
    if (top < 8) {
      top = 8;
    }

    setOffset({ left, top });
  }, [x, y]);

  return (
    <div
      ref={ref}
      className="absolute z-50 pointer-events-none"
      style={{ left: offset.left, top: offset.top }}
    >
      <div className="bg-kraken-navy/95 backdrop-blur-sm border border-kraken-border rounded-lg shadow-xl px-3 py-2 min-w-[160px]">
        <div className="flex items-center gap-2 mb-1">
          <div
            className={`w-2 h-2 rounded-full ${
              type === 'node' ? 'bg-kraken-ice' : 'bg-kraken-ice-dim'
            }`}
          />
          <span className="text-xs font-medium text-kraken-ice uppercase">
            {type}
          </span>
        </div>
        {type === 'node' && <NodePopover node={item as GraphNode} />}
        {type === 'link' && <LinkPopover link={item as GraphLink} />}
        <div className="mt-1.5 text-[10px] text-kraken-muted/60">
          Click for details
        </div>
      </div>
    </div>
  );
}

function NodePopover({ node }: { node: GraphNode }) {
  return (
    <div className="space-y-0.5">
      <div className="text-sm font-mono text-kraken-frost">
        {node.name || node.id}
      </div>
      {node.name && node.id !== node.name && (
        <div className="text-xs text-kraken-muted font-mono">{node.id}</div>
      )}
      {node.locators && node.locators.length > 0 && (
        <div className="text-xs text-kraken-muted">
          {node.locators.length} SRv6 locator{node.locators.length > 1 ? 's' : ''}
        </div>
      )}
    </div>
  );
}

function LinkPopover({ link }: { link: GraphLink }) {
  const srcId = typeof link.source === 'string' ? link.source : link.source.id;
  const tgtId = typeof link.target === 'string' ? link.target : link.target.id;

  return (
    <div className="space-y-0.5">
      <div className="text-xs font-mono text-kraken-frost">
        {srcId} — {tgtId}
      </div>
      {link.metric !== undefined && (
        <div className="text-xs text-kraken-muted">
          metric: {link.metric}
        </div>
      )}
      {link.bandwidth !== undefined && (
        <div className="text-xs text-kraken-muted">
          bw: {formatBW(link.bandwidth)}
        </div>
      )}
      {link.delay !== undefined && (
        <div className="text-xs text-kraken-muted">
          delay: {link.delay}us
        </div>
      )}
    </div>
  );
}

function formatBW(bps: number): string {
  if (bps >= 1e12) return `${(bps / 1e12).toFixed(1)} Tbps`;
  if (bps >= 1e9) return `${(bps / 1e9).toFixed(1)} Gbps`;
  if (bps >= 1e6) return `${(bps / 1e6).toFixed(1)} Mbps`;
  return `${bps} bps`;
}
