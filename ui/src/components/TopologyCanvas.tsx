import { useRef, useEffect, useState } from 'react';
import * as d3 from 'd3';
import { api } from '../api/client';
import type { GraphNode, GraphLink, PathResponse, TopologyGraph } from '../types/api';
import type { PopoverData } from '../App';

interface TopologyCanvasProps {
  topologyId: string;
  selectedNodes: GraphNode[];
  pathResponse: PathResponse | null;
  onNodeClick: (node: GraphNode, multiSelect: boolean) => void;
  onLinkClick: (link: GraphLink) => void;
  onHover: (data: PopoverData | null) => void;
  onCanvasClick: () => void;
}

// Demo topology for when no backend is available
function generateDemoTopology(): TopologyGraph {
  const spines = Array.from({ length: 4 }, (_, i) => ({
    id: `spine-${i + 1}`,
    name: `Spine ${i + 1}`,
  }));
  const leaves = Array.from({ length: 8 }, (_, i) => ({
    id: `leaf-${i + 1}`,
    name: `Leaf ${i + 1}`,
  }));
  const endpoints = Array.from({ length: 16 }, (_, i) => ({
    id: `gpu-${String(i + 1).padStart(2, '0')}`,
    name: `GPU ${i + 1}`,
  }));

  const nodes: GraphNode[] = [...spines, ...leaves, ...endpoints];
  const links: GraphLink[] = [];

  // Spine-to-leaf full mesh
  for (const spine of spines) {
    for (const leaf of leaves) {
      links.push({
        id: `${spine.id}-${leaf.id}`,
        source: spine.id,
        target: leaf.id,
        metric: 10,
        bandwidth: 400e9,
      });
    }
  }

  // Leaf-to-endpoint (2 endpoints per leaf)
  for (let i = 0; i < leaves.length; i++) {
    links.push({
      id: `${leaves[i].id}-${endpoints[i * 2].id}`,
      source: leaves[i].id,
      target: endpoints[i * 2].id,
      metric: 1,
      bandwidth: 100e9,
    });
    links.push({
      id: `${leaves[i].id}-${endpoints[i * 2 + 1].id}`,
      source: leaves[i].id,
      target: endpoints[i * 2 + 1].id,
      metric: 1,
      bandwidth: 100e9,
    });
  }

  return { nodes, links };
}

// Resolve visual tier from a D3 datum. Works outside the main render effect
// by checking the node's attached type or falling back to name heuristics.
function resolveTier(d: any): number {
  if (d.subtype === 'external_bgp') return 2;
  if (d.type === 'prefix' || d.type === 'endpoint') return 2;
  if (d.type === 'vrf') return 1;
  const id: string = d.id || '';
  if (id.includes('spine')) return 0;
  if (id.includes('leaf')) return 1;
  if (d.type === 'node') return 0;
  return 0;
}

// Node visual styles based on type/subtype — returns { fill, stroke, radius }
function nodeStyle(d: any, tier: number): { fill: string; stroke: string; radius: number } {
  // External BGP peers: amber/orange
  if (d.subtype === 'external_bgp') {
    return { fill: '#3d2800', stroke: '#f0a030', radius: 10 };
  }
  // Prefix nodes: muted green
  if (d.type === 'prefix') {
    return { fill: '#0a2e1a', stroke: '#40b870', radius: 7 };
  }
  // Fabric nodes: tier-based Kraken palette
  const fills = ['#0f4477', '#0a3358', '#061e38'];
  const strokes = ['#68e8e8', '#3fbfbf', '#2a8a8a'];
  const radii = [16, 12, 8];
  return { fill: fills[tier], stroke: strokes[tier], radius: radii[tier] };
}

export type LayoutMode = 'auto' | 'clos' | 'ring';

export default function TopologyCanvas({
  topologyId,
  selectedNodes,
  pathResponse,
  onNodeClick,
  onLinkClick,
  onHover,
  onCanvasClick,
}: TopologyCanvasProps) {
  const svgRef = useRef<SVGSVGElement>(null);
  const simulationRef = useRef<d3.Simulation<GraphNode, GraphLink> | null>(null);
  const [topology, setTopology] = useState<TopologyGraph | null>(null);
  const [usingDemo, setUsingDemo] = useState(false);
  const [layoutMode, setLayoutMode] = useState<LayoutMode>('auto');

  // Fetch topology data
  useEffect(() => {
    if (!topologyId) {
      setTopology(generateDemoTopology());
      setUsingDemo(true);
      return;
    }
    setUsingDemo(false);
    api
      .getTopologyGraph(topologyId)
      .then((data) => {
        setTopology(data);
        setUsingDemo(false);
        // Auto-detect composite graphs (contain prefix or external BGP nodes)
        // and default to force-directed layout since Clos tiers don't apply
        setLayoutMode('auto');
      })
      .catch(() => {
        // /graph endpoint not available — fall back to /nodes
        console.info('No /graph endpoint, falling back to /nodes');
        api.getTopologyNodes(topologyId).then((data) => {
          setTopology({
            nodes: data.nodes.map((n) => ({ id: n.id, name: n.name })),
            links: [],
          });
          setUsingDemo(false);
        }).catch((err) => {
          console.warn('Failed to fetch topology:', err.message);
          setTopology(generateDemoTopology());
          setUsingDemo(true);
        });
      });
  }, [topologyId]);

  // (Path highlighting handled in the useEffect below)

  // Render D3 graph
  useEffect(() => {
    if (!topology || !svgRef.current) return;

    const svg = d3.select(svgRef.current);
    svg.selectAll('*').remove();

    const width = svgRef.current.clientWidth;
    const height = svgRef.current.clientHeight;

    // Create zoom container
    const g = svg.append('g');

    const zoom = d3.zoom<SVGSVGElement, unknown>()
      .scaleExtent([0.2, 5])
      .on('zoom', (event) => {
        g.attr('transform', event.transform);
      });

    svg.call(zoom);
    svg.on('click', (event) => {
      if (event.target === svgRef.current) onCanvasClick();
    });

    // Prepare data (clone to avoid mutation)
    const nodes: GraphNode[] = topology.nodes.map((n) => ({ ...n }));
    const links: GraphLink[] = topology.links.map((l) => ({ ...l }));

    // --- Clos tier detection via structural analysis ---
    // Build adjacency and degree maps
    const degreeMap = new Map<string, number>();
    const adjacency = new Map<string, Set<string>>();
    for (const n of nodes) {
      degreeMap.set(n.id, 0);
      adjacency.set(n.id, new Set());
    }
    for (const l of links) {
      const src = typeof l.source === 'string' ? l.source : l.source.id;
      const dst = typeof l.target === 'string' ? l.target : l.target.id;
      degreeMap.set(src, (degreeMap.get(src) || 0) + 1);
      degreeMap.set(dst, (degreeMap.get(dst) || 0) + 1);
      adjacency.get(src)?.add(dst);
      adjacency.get(dst)?.add(src);
    }

    // Structural Clos detection:
    // 1. Endpoints = type "endpoint" OR degree-1 nodes
    // 2. Leaves = nodes adjacent to at least one endpoint
    // 3. Spines = nodes adjacent to leaves but NOT adjacent to any endpoint
    const endpointIds = new Set<string>();
    const leafIds = new Set<string>();
    const spineIds = new Set<string>();

    for (const n of nodes) {
      if (n.type === 'endpoint' || (n.type === 'node' && (degreeMap.get(n.id) || 0) === 1)) {
        endpointIds.add(n.id);
      }
    }
    for (const n of nodes) {
      if (endpointIds.has(n.id)) continue;
      const neighbors = adjacency.get(n.id) || new Set();
      const hasEndpointNeighbor = Array.from(neighbors).some((nb) => endpointIds.has(nb));
      if (hasEndpointNeighbor) {
        leafIds.add(n.id);
      }
    }
    for (const n of nodes) {
      if (endpointIds.has(n.id) || leafIds.has(n.id)) continue;
      const neighbors = adjacency.get(n.id) || new Set();
      const hasLeafNeighbor = Array.from(neighbors).some((nb) => leafIds.has(nb));
      if (hasLeafNeighbor) {
        spineIds.add(n.id);
      }
    }

    // Determine node visual tier
    const getNodeTier = (node: GraphNode): number => {
      const t = node.type || '';
      if (t === 'prefix') return 2;
      if (t === 'vrf') return 1;

      if (layoutMode === 'clos') {
        if (spineIds.has(node.id)) return 0;
        if (leafIds.has(node.id)) return 1;
        if (endpointIds.has(node.id)) return 2;
        // Fallback: nodes not connected to the Clos structure
        return 0;
      }

      // Auto/Ring: name heuristics only
      if (t === 'endpoint') return 2;
      const id = node.id;
      if (id.includes('spine')) return 0;
      if (id.includes('leaf')) return 1;
      return 0;
    };

    // Build tier lookup
    const tierMap = new Map<string, number>();
    for (const n of nodes) {
      tierMap.set(n.id, getNodeTier(n));
    }
    const getTier = (id: string) => tierMap.get(id) ?? 0;

    // --- Layout-specific force configuration ---
    const simulation = d3.forceSimulation<GraphNode>(nodes);

    if (layoutMode === 'ring') {
      // Position nodes in a circle sorted by ID for consistent ordering
      const sorted = [...nodes].sort((a, b) => a.id.localeCompare(b.id));
      const idOrder = new Map(sorted.map((n, i) => [n.id, i]));
      const radius = Math.min(width, height) * 0.35;
      const angleStep = (2 * Math.PI) / nodes.length;
      for (const node of nodes) {
        const i = idOrder.get(node.id) || 0;
        node.x = width / 2 + radius * Math.cos(angleStep * i - Math.PI / 2);
        node.y = height / 2 + radius * Math.sin(angleStep * i - Math.PI / 2);
      }
      simulation
        .force(
          'link',
          d3.forceLink<GraphNode, GraphLink>(links).id((d) => d.id).distance(50).strength(0.1)
        )
        .force('charge', d3.forceManyBody().strength(-30))
        .force(
          'radial',
          d3.forceRadial(radius, width / 2, height / 2).strength(1.0)
        )
        .alpha(0.3);
    } else if (layoutMode === 'clos') {
      // Fixed-position Clos layout: spines top row, leaves middle, endpoints bottom.
      // Nodes are pinned (fx/fy) so the simulation can't move them.
      // Scales to any fabric size (512 leaves, 256 spines, etc.)
      const tierNodes: GraphNode[][] = [[], [], []];
      for (const node of nodes) {
        tierNodes[getTier(node.id)].push(node);
      }

      // Sort each tier for consistent ordering
      for (const tier of tierNodes) {
        tier.sort((a, b) => a.id.localeCompare(b.id));
      }

      // Compute vertical positions — distribute tiers evenly with padding
      const padding = 60;
      const usableHeight = height - padding * 2;
      const tierYPositions = [
        padding + usableHeight * 0.08,  // spines at top
        padding + usableHeight * 0.38,  // leaves in middle
        padding + usableHeight * 0.68,  // endpoints — above path panel
      ];

      // Place each tier in a horizontal line
      for (let tier = 0; tier < 3; tier++) {
        const nodesInTier = tierNodes[tier];
        const count = nodesInTier.length;
        if (count === 0) continue;
        const spacing = (width - padding * 2) / (count + 1);
        for (let i = 0; i < count; i++) {
          const node = nodesInTier[i];
          node.x = padding + spacing * (i + 1);
          node.y = tierYPositions[tier];
          node.fx = node.x;
          node.fy = node.y;
        }
      }

      // Minimal simulation — just needed for D3 link rendering, nodes won't move
      simulation
        .force(
          'link',
          d3.forceLink<GraphNode, GraphLink>(links).id((d) => d.id)
        )
        .alpha(0.01); // near-zero alpha so it settles immediately
    } else {
      // Auto: pure force-directed, no tier constraints — spacious layout
      simulation
        .force(
          'link',
          d3.forceLink<GraphNode, GraphLink>(links)
            .id((d) => d.id)
            .distance(140)
            .strength(0.6)
        )
        .force('charge', d3.forceManyBody().strength(-600))
        .force('center', d3.forceCenter(width / 2, height / 2))
        .force('collision', d3.forceCollide(40));
    }

    simulationRef.current = simulation;

    // Edge style helper — returns stroke color and dash pattern per edge type
    const edgeStyle = (d: GraphLink) => {
      switch (d.type) {
        case 'bgp_session':
          return { stroke: '#4a80d0', dash: '6,3' };       // dashed blue
        case 'bgp_reachability':
          return { stroke: '#40b870', dash: '2,3' };       // dotted green
        default:
          return { stroke: '#3a7faa', dash: 'none' };      // solid gray (igp_adjacency, etc.)
      }
    };

    // Render links
    const linkGroup = g
      .append('g')
      .attr('class', 'links')
      .selectAll('line')
      .data(links)
      .join('line')
      .attr('stroke', (d) => edgeStyle(d).stroke)
      .attr('stroke-width', 1.8)
      .attr('stroke-opacity', 0.7)
      .attr('stroke-dasharray', (d) => edgeStyle(d).dash)
      .style('cursor', 'pointer')
      .on('mouseenter', function (event, d) {
        d3.select(this).attr('stroke', '#68e8e8').attr('stroke-width', 3.5);
        const rect = svgRef.current!.getBoundingClientRect();
        onHover({
          x: event.clientX - rect.left,
          y: event.clientY - rect.top,
          type: 'link',
          data: d,
        });
      })
      .on('mouseleave', function (_event, d) {
        const style = edgeStyle(d);
        d3.select(this).attr('stroke', style.stroke).attr('stroke-width', 1.8);
        onHover(null);
      })
      .on('click', (event, d) => {
        event.stopPropagation();
        onLinkClick(d);
      });

    // Render nodes
    const nodeGroup = g
      .append('g')
      .attr('class', 'nodes')
      .selectAll('g')
      .data(nodes)
      .join('g')
      .style('cursor', 'pointer')
      .call(
        d3.drag<any, GraphNode>()
          .on('start', (event, d) => {
            if (!event.active) simulation.alphaTarget(0.3).restart();
            d.fx = d.x;
            d.fy = d.y;
          })
          .on('drag', (event, d) => {
            d.fx = event.x;
            d.fy = event.y;
          })
          .on('end', (event, d) => {
            if (!event.active) simulation.alphaTarget(0);
            if (layoutMode === 'clos') {
              // In Clos mode, snap back to original grid position
              const tier = getTier(d.id);
              const tieredNodes = nodes.filter((n) => getTier(n.id) === tier);
              tieredNodes.sort((a, b) => a.id.localeCompare(b.id));
              const idx = tieredNodes.findIndex((n) => n.id === d.id);
              const count = tieredNodes.length;
              const padding = 60;
              const spacing = (width - padding * 2) / (count + 1);
              const tierYPositions = [
                padding + (height - padding * 2) * 0.08,
                padding + (height - padding * 2) * 0.38,
                padding + (height - padding * 2) * 0.68,
              ];
              d.fx = padding + spacing * (idx + 1);
              d.fy = tierYPositions[tier];
            } else {
              d.fx = null;
              d.fy = null;
            }
          })
      );

    // Node circles with type/subtype-aware styling
    nodeGroup
      .append('circle')
      .attr('r', (d) => nodeStyle(d, getTier(d.id)).radius)
      .attr('fill', (d) => nodeStyle(d, getTier(d.id)).fill)
      .attr('stroke', (d) => nodeStyle(d, getTier(d.id)).stroke)
      .attr('stroke-width', 2.5);

    // Node labels
    nodeGroup
      .append('text')
      .text((d) => d.name || d.id)
      .attr('dy', (d) => {
        const tier = getTier(d.id);
        return tier === 2 ? 20 : -22;
      })
      .attr('text-anchor', 'middle')
      .attr('fill', '#a0cfdf')
      .attr('font-size', '10px')
      .attr('font-family', 'JetBrains Mono, monospace');

    // Node interactions
    nodeGroup
      .on('mouseenter', function (event, d) {
        d3.select(this).select('circle').attr('stroke-width', 3.5).attr('stroke', '#68e8e8');
        const rect = svgRef.current!.getBoundingClientRect();
        onHover({
          x: event.clientX - rect.left,
          y: event.clientY - rect.top,
          type: 'node',
          data: d,
        });
      })
      .on('mouseleave', function (_event, d) {
        const tier = getTier(d.id);
        const isSelected = selectedNodes.some((n) => n.id === d.id);
        d3.select(this)
          .select('circle')
          .attr('stroke-width', isSelected ? 3.5 : 2.5)
          .attr('stroke', isSelected ? '#ff2d55' : tier === 0 ? '#68e8e8' : tier === 1 ? '#3fbfbf' : '#2a8a8a');
        onHover(null);
      })
      .on('click', (event, d) => {
        event.stopPropagation();
        onNodeClick(d, event.shiftKey);
      });

    // Tick update
    simulation.on('tick', () => {
      linkGroup
        .attr('x1', (d) => (d.source as GraphNode).x!)
        .attr('y1', (d) => (d.source as GraphNode).y!)
        .attr('x2', (d) => (d.target as GraphNode).x!)
        .attr('y2', (d) => (d.target as GraphNode).y!);

      nodeGroup.attr('transform', (d) => `translate(${d.x},${d.y})`);
    });

    // Initial zoom to fit
    setTimeout(() => {
      const bounds = (g.node() as SVGGElement)?.getBBox();
      if (bounds) {
        const fullWidth = bounds.width + 80;
        const fullHeight = bounds.height + 80;
        const midX = bounds.x + bounds.width / 2;
        const midY = bounds.y + bounds.height / 2;
        const scale = Math.min(width / fullWidth, height / fullHeight, 1.2);
        const transform = d3.zoomIdentity
          .translate(width / 2 - midX * scale, height / 2 - midY * scale)
          .scale(scale);
        svg.transition().duration(750).call(zoom.transform, transform);
      }
    }, 1500);

    return () => {
      simulation.stop();
    };
  }, [topology, layoutMode]);

  // Update node selection styling
  useEffect(() => {
    if (!svgRef.current || !topology) return;
    const svg = d3.select(svgRef.current);

    svg.selectAll('.nodes g').each(function (d: any) {
      const isSelected = selectedNodes.some((n) => n.id === d.id);
      const tier = resolveTier(d);
      const style = nodeStyle(d, tier);
      d3.select(this)
        .select('circle')
        .transition()
        .duration(200)
        .attr('stroke', isSelected ? '#ff2d55' : style.stroke)
        .attr('stroke-width', isSelected ? 3.5 : 2.5)
        .attr('fill', isSelected ? '#3d0a15' : style.fill);
    });
  }, [selectedNodes, topology]);

  // Highlight paths
  useEffect(() => {
    if (!svgRef.current || !topology) return;
    const svg = d3.select(svgRef.current);

    if (!pathResponse) {
      svg.selectAll('.links line').each(function (d: any) {
        const style = d.type === 'bgp_session'
          ? { stroke: '#4a80d0', dash: '6,3' }
          : d.type === 'bgp_reachability'
          ? { stroke: '#40b870', dash: '2,3' }
          : { stroke: '#3a7faa', dash: 'none' };
        d3.select(this)
          .attr('stroke', style.stroke)
          .attr('stroke-width', 1.8)
          .attr('stroke-opacity', 0.7)
          .attr('stroke-dasharray', style.dash);
      });
      svg.selectAll('.nodes g').each(function (d: any) {
        const tier = resolveTier(d);
        const style = nodeStyle(d, tier);
        d3.select(this)
          .select('circle')
          .attr('stroke', style.stroke)
          .attr('stroke-width', 2.5)
          .attr('fill', style.fill);
      });
      return;
    }

    // Collect all vertex IDs and edge IDs from the computed paths
    const pathVertices = new Set<string>();
    const pathEdges = new Set<string>();
    for (const p of pathResponse.paths) {
      pathVertices.add(p.src_id);
      pathVertices.add(p.dst_id);
      if (p.vertex_ids) {
        for (const v of p.vertex_ids) pathVertices.add(v);
      }
      if (p.edge_ids) {
        for (const e of p.edge_ids) pathEdges.add(e);
      }
    }

    // Collect path endpoint IDs (src_id/dst_id from all paths)
    const pathEndpoints = new Set<string>();
    for (const p of pathResponse.paths) {
      pathEndpoints.add(p.src_id);
      pathEndpoints.add(p.dst_id);
    }

    // Highlight links on the path in bright cyan, dim others
    svg.selectAll('.links line').each(function (d: any) {
      const edgeId = d.id;
      const sourceId = typeof d.source === 'object' ? d.source.id : d.source;
      const targetId = typeof d.target === 'object' ? d.target.id : d.target;

      // A link is on-path if:
      // 1. Its edge ID is in the path's edge_ids (LinkEdge between routers), OR
      // 2. It connects a path endpoint to a vertex in the path (AttachmentEdge)
      const onPath = pathEdges.has(edgeId) ||
        (pathEndpoints.has(sourceId) && pathVertices.has(targetId)) ||
        (pathEndpoints.has(targetId) && pathVertices.has(sourceId));

      d3.select(this)
        .transition()
        .duration(300)
        .attr('stroke', onPath ? '#68e8e8' : '#2a5f7e')
        .attr('stroke-width', onPath ? 3.5 : 1.2)
        .attr('stroke-opacity', onPath ? 1.0 : 0.45)
        .attr('stroke-dasharray', onPath ? 'none' : (
          d.type === 'bgp_session' ? '6,3' :
          d.type === 'bgp_reachability' ? '2,3' : 'none'
        ));
    });

    // Highlight path nodes: endpoints in red, transit nodes in cyan
    svg.selectAll('.nodes g').each(function (d: any) {
      const isEndpoint = pathResponse.paths.some(
        (p) => p.src_id === d.id || p.dst_id === d.id
      );
      const isTransit = !isEndpoint && pathVertices.has(d.id);
      if (isEndpoint) {
        d3.select(this)
          .select('circle')
          .transition()
          .duration(300)
          .attr('stroke', '#ff2d55')
          .attr('stroke-width', 4)
          .attr('fill', '#3d0a15');
      } else if (isTransit) {
        d3.select(this)
          .select('circle')
          .transition()
          .duration(300)
          .attr('stroke', '#68e8e8')
          .attr('stroke-width', 3.5)
          .attr('fill', '#0a4060');
      } else {
        const tier = resolveTier(d);
        const style = nodeStyle(d, tier);
        d3.select(this)
          .select('circle')
          .transition()
          .duration(300)
          .attr('stroke', style.stroke)
          .attr('stroke-width', 2.5)
          .attr('stroke-opacity', 0.3)
          .attr('fill', style.fill);
      }
    });
  }, [pathResponse, topology]);

  return (
    <div className="w-full h-full bg-kraken-deep relative">
      {/* Grid background */}
      <div
        className="absolute inset-0 opacity-[0.03]"
        style={{
          backgroundImage:
            'linear-gradient(#99d9d9 1px, transparent 1px), linear-gradient(90deg, #99d9d9 1px, transparent 1px)',
          backgroundSize: '40px 40px',
        }}
      />
      <svg
        ref={svgRef}
        className="w-full h-full relative z-10"
        style={{ cursor: 'grab' }}
      />
      {/* Layout toggle toolbar */}
      <div className="absolute top-4 right-4 z-20 flex bg-kraken-navy/90 backdrop-blur-sm border border-kraken-border rounded-lg overflow-hidden">
        {(['auto', 'clos', 'ring'] as LayoutMode[]).map((mode) => (
          <button
            key={mode}
            onClick={() => setLayoutMode(mode)}
            className={`px-3 py-1.5 text-xs font-medium transition-colors capitalize ${
              layoutMode === mode
                ? 'bg-kraken-ice/20 text-kraken-ice border-kraken-ice'
                : 'text-kraken-muted hover:text-kraken-frost hover:bg-kraken-dark/50'
            }`}
          >
            {mode}
          </button>
        ))}
      </div>
      {/* Legend */}
      <div className="absolute bottom-4 right-4 z-20 bg-kraken-navy/80 backdrop-blur-sm border border-kraken-border rounded-lg px-3 py-2">
        <div className="flex items-center gap-4 text-xs text-kraken-muted">
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded-full border-2 border-kraken-ice bg-kraken-mid" />
            <span>Spine</span>
          </div>
          <div className="flex items-center gap-1.5">
            <div className="w-2.5 h-2.5 rounded-full border-2 border-kraken-ice-dim bg-kraken-surface" />
            <span>Leaf</span>
          </div>
          <div className="flex items-center gap-1.5">
            <div className="w-2 h-2 rounded-full border-2 border-kraken-border bg-kraken-navy" />
            <span>Endpoint</span>
          </div>
          <div className="flex items-center gap-1.5">
            <div className="w-3 h-3 rounded-full border-2 border-kraken-red bg-kraken-red/20" />
            <span>Selected</span>
          </div>
        </div>
      </div>
      {usingDemo && (
        <div className="absolute top-4 left-1/2 -translate-x-1/2 z-20 bg-yellow-900/90 backdrop-blur-sm border border-yellow-600/50 rounded-lg px-4 py-2 text-xs text-yellow-200">
          Demo topology — backend unreachable or no topology loaded
        </div>
      )}
    </div>
  );
}
