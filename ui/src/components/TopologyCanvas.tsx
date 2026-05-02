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
  // PolarFly vertex types (when labels are present)
  const vType = d.labels?.vType;
  if (vType === '0') {
    // W (quadric/absolute): gold ring — backbone nodes
    return { fill: '#2d1f00', stroke: '#e8c840', radius: 14 };
  }
  if (vType === '1') {
    // V1 (cluster hub): bright cyan — fan blade centers
    return { fill: '#0a3040', stroke: '#40d8e8', radius: 12 };
  }
  if (vType === '2') {
    // V2 (fin vertex): muted teal — petal leaves
    return { fill: '#061e2e', stroke: '#2aa0b0', radius: 8 };
  }
  // Fabric nodes: tier-based Kraken palette
  const fills = ['#0f4477', '#0a3358', '#061e38'];
  const strokes = ['#68e8e8', '#3fbfbf', '#2a8a8a'];
  const radii = [16, 12, 8];
  return { fill: fills[tier], stroke: strokes[tier], radius: radii[tier] };
}

export type LayoutMode = 'auto' | 'clos' | 'ring' | 'polarfly';

// ── PolarFly cluster construction ──
//
// Two strategies, chosen automatically:
//
// A. LABEL-BASED (preferred): when nodes carry `labels.vType` ("0"/"1"/"2")
//    and `labels.cluster`, use the exact PolarFly partition:
//      vType "0" (W) = quadric/absolute backbone nodes
//      vType "1" (V1) = off-quadric cluster hubs (cluster == self)
//      vType "2" (V2) = fin vertices (cluster == their V1 hub)
//
// B. HEURISTIC (fallback): for arbitrary topologies without labels, use a
//    greedy independent set as quadric analog, then Algorithm 1-style
//    cluster construction.

interface PolarFlyCluster {
  id: number;
  type: 'quadric' | 'nonquadric';
  center: string;       // node ID of cluster center ('' for quadric)
  nodes: string[];      // all node IDs in this cluster
}

interface PolarFlyLayout {
  clusters: PolarFlyCluster[];
  nodeCluster: Map<string, number>;   // nodeID → clusterID
  quadricNodes: string[];             // backbone ring node IDs
}

function buildPolarFlyClusters(
  nodes: GraphNode[],
  adjacency: Map<string, Set<string>>,
  degreeMap: Map<string, number>,
): PolarFlyLayout {
  // ── Strategy A: label-based classification ──
  // Check if the topology carries PolarFly labels.
  const hasLabels = nodes.some(n => n.labels?.vType !== undefined);

  if (hasLabels) {
    const wNodes: string[] = [];          // vType "0" — quadric backbone
    const v1Nodes: string[] = [];         // vType "1" — cluster hubs
    const v2ByCluster = new Map<string, string[]>(); // hub ID → [fin IDs]

    for (const n of nodes) {
      const vt = n.labels?.vType;
      if (vt === '0') {
        wNodes.push(n.id);
      } else if (vt === '1') {
        v1Nodes.push(n.id);
        // Initialize cluster fins list
        if (!v2ByCluster.has(n.id)) v2ByCluster.set(n.id, []);
      } else if (vt === '2') {
        const hub = n.labels?.cluster || '';
        if (!v2ByCluster.has(hub)) v2ByCluster.set(hub, []);
        v2ByCluster.get(hub)!.push(n.id);
      }
    }

    const clusters: PolarFlyCluster[] = [];
    const nodeCluster = new Map<string, number>();

    // C0: quadric cluster
    clusters.push({ id: 0, type: 'quadric', center: '', nodes: [...wNodes] });
    for (const nid of wNodes) nodeCluster.set(nid, 0);

    // C1..Cq: one cluster per V1 hub
    v1Nodes.sort((a, b) => a.localeCompare(b));
    let clusterIdx = 1;
    for (const hub of v1Nodes) {
      const fins = v2ByCluster.get(hub) || [];
      fins.sort((a, b) => a.localeCompare(b));
      const clusterNodes = [hub, ...fins];
      clusters.push({ id: clusterIdx, type: 'nonquadric', center: hub, nodes: clusterNodes });
      for (const nid of clusterNodes) nodeCluster.set(nid, clusterIdx);
      clusterIdx++;
    }

    // Assign any uncategorized nodes (endpoints, etc.) to quadric cluster
    for (const n of nodes) {
      if (!nodeCluster.has(n.id)) {
        nodeCluster.set(n.id, 0);
        clusters[0].nodes.push(n.id);
      }
    }

    return { clusters, nodeCluster, quadricNodes: wNodes };
  }

  // ── Strategy B: heuristic fallback for arbitrary topologies ──
  const nodeIds = nodes.map(n => n.id);
  const nodeSet = new Set(nodeIds);

  // Sort by descending degree for greedy independent set
  const sorted = [...nodeIds].sort((a, b) => (degreeMap.get(b) || 0) - (degreeMap.get(a) || 0));

  // Greedy maximal independent set (quadric analog)
  const backbone = new Set<string>();
  const excluded = new Set<string>();
  for (const nid of sorted) {
    if (excluded.has(nid)) continue;
    backbone.add(nid);
    const neighbors = adjacency.get(nid) || new Set();
    for (const nb of neighbors) excluded.add(nb);
  }

  if (backbone.size < 2) {
    return {
      clusters: [{ id: 0, type: 'quadric', center: '', nodes: nodeIds }],
      nodeCluster: new Map(nodeIds.map(id => [id, 0])),
      quadricNodes: nodeIds,
    };
  }

  const clusters: PolarFlyCluster[] = [];
  const nodeCluster = new Map<string, number>();

  const quadricNodes = [...backbone];
  clusters.push({ id: 0, type: 'quadric', center: '', nodes: quadricNodes });
  for (const nid of quadricNodes) nodeCluster.set(nid, 0);

  const starter = quadricNodes[0];
  const starterNeighbors = adjacency.get(starter) || new Set();
  const centers = [...starterNeighbors].filter(n => !backbone.has(n) && nodeSet.has(n));

  let clusterIdx = 1;
  for (const center of centers) {
    if (nodeCluster.has(center)) continue;
    const clusterNodes = [center];
    nodeCluster.set(center, clusterIdx);

    const centerNeighbors = adjacency.get(center) || new Set();
    for (const nb of centerNeighbors) {
      if (!backbone.has(nb) && !nodeCluster.has(nb) && nodeSet.has(nb)) {
        clusterNodes.push(nb);
        nodeCluster.set(nb, clusterIdx);
      }
    }

    clusters.push({ id: clusterIdx, type: 'nonquadric', center, nodes: clusterNodes });
    clusterIdx++;
  }

  for (const nid of nodeIds) {
    if (nodeCluster.has(nid)) continue;
    const neighbors = adjacency.get(nid) || new Set();
    let bestCluster = 0;
    let bestCount = 0;
    for (const nb of neighbors) {
      const c = nodeCluster.get(nb);
      if (c !== undefined && c > 0) {
        const count = clusters[c].nodes.filter(cn =>
          (adjacency.get(cn) || new Set()).has(nid)
        ).length;
        if (count > bestCount) {
          bestCount = count;
          bestCluster = c;
        }
      }
    }
    nodeCluster.set(nid, bestCluster);
    clusters[bestCluster].nodes.push(nid);
  }

  return { clusters, nodeCluster, quadricNodes };
}

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
      // Scales to large fabrics (512+ endpoints, 64 leaves, 32 spines) by expanding
      // the layout canvas beyond the viewport and relying on zoom/pan.
      const tierNodes: GraphNode[][] = [[], [], []];
      for (const node of nodes) {
        tierNodes[getTier(node.id)].push(node);
      }

      // Sort each tier for consistent ordering
      for (const tier of tierNodes) {
        tier.sort((a, b) => a.id.localeCompare(b.id));
      }

      const spineCount = tierNodes[0].length;
      const leafCount = tierNodes[1].length;
      const epCount = tierNodes[2].length;
      const maxTierCount = Math.max(spineCount, leafCount, epCount);

      // Adaptive spacing: ensure minimum pixel gap between nodes per tier
      // For small fabrics, fit in viewport; for large, expand canvas
      const minSpacing = maxTierCount > 100 ? 18 : maxTierCount > 32 ? 28 : 50;
      const padding = 40;

      // Group endpoints by parent leaf for columnar layout
      const epsByLeaf = new Map<string, GraphNode[]>();
      for (const ep of tierNodes[2]) {
        const neighbors = adjacency.get(ep.id) || new Set();
        const parentLeaf = Array.from(neighbors).find((nb) => leafIds.has(nb));
        if (parentLeaf) {
          if (!epsByLeaf.has(parentLeaf)) epsByLeaf.set(parentLeaf, []);
          epsByLeaf.get(parentLeaf)!.push(ep);
        } else {
          // Orphan endpoint — group under a dummy key
          if (!epsByLeaf.has('__orphan__')) epsByLeaf.set('__orphan__', []);
          epsByLeaf.get('__orphan__')!.push(ep);
        }
      }
      const maxEpsPerLeaf = Math.max(1, ...Array.from(epsByLeaf.values()).map((v) => v.length));

      // Layout width driven by leaf count (endpoints are grouped under leaves)
      const leafSpacing = Math.max(minSpacing * maxEpsPerLeaf, minSpacing * 2);
      const canvasWidth = Math.max(width, padding * 2 + leafCount * leafSpacing);

      // Spine spacing — center spines over the leaf span
      const spineSpacing = leafCount > 0
        ? (canvasWidth - padding * 2) / (spineCount + 1)
        : minSpacing;

      // Adaptive node sizing based on scale
      const scaleFactor = maxTierCount > 200 ? 0.35
        : maxTierCount > 100 ? 0.5
        : maxTierCount > 32 ? 0.7
        : 1.0;

      // Vertical positions — compute from canvas dimensions
      const rowGap = Math.max(150, height * 0.35);
      const tierYPositions = [
        padding + 20,                          // spines at top
        padding + 20 + rowGap,                 // leaves in middle
        padding + 20 + rowGap * 2,             // endpoint row start
      ];
      const canvasHeight = tierYPositions[2] + maxEpsPerLeaf * (minSpacing * scaleFactor) + padding + 40;

      // Place spines — evenly across canvas width
      for (let i = 0; i < spineCount; i++) {
        const node = tierNodes[0][i];
        node.x = padding + spineSpacing * (i + 1);
        node.y = tierYPositions[0];
        node.fx = node.x;
        node.fy = node.y;
      }

      // Place leaves — evenly across canvas width
      const leafXStart = padding + leafSpacing / 2;
      const leafPositions = new Map<string, number>(); // leafID → x position
      for (let i = 0; i < leafCount; i++) {
        const node = tierNodes[1][i];
        const xPos = leafXStart + i * leafSpacing;
        node.x = xPos;
        node.y = tierYPositions[1];
        node.fx = node.x;
        node.fy = node.y;
        leafPositions.set(node.id, xPos);
      }

      // Place endpoints — columnar under their parent leaf
      const epSpacingY = Math.max(minSpacing * scaleFactor, 12);
      const epSpacingX = Math.max(minSpacing * scaleFactor, 12);
      for (const [leafId, eps] of epsByLeaf) {
        const leafX = leafPositions.get(leafId) ?? (padding + canvasWidth / 2);
        eps.sort((a, b) => a.id.localeCompare(b.id));
        const cols = Math.ceil(Math.sqrt(eps.length));
        for (let i = 0; i < eps.length; i++) {
          const col = i % cols;
          const row = Math.floor(i / cols);
          const xOffset = (col - (cols - 1) / 2) * epSpacingX;
          eps[i].x = leafX + xOffset;
          eps[i].y = tierYPositions[2] + row * epSpacingY;
          eps[i].fx = eps[i].x;
          eps[i].fy = eps[i].y;
        }
      }

      // Store layout parameters for reuse (node sizing, zoom-to-fit, drag snap-back)
      const closLayout = { canvasWidth, canvasHeight, scaleFactor, tierYPositions,
        spineSpacing, leafSpacing, leafXStart, padding, epSpacingX, epSpacingY,
        leafPositions, epsByLeaf, maxEpsPerLeaf };
      (simulation as any).__closLayout = closLayout;

      // Minimal simulation — just needed for D3 link rendering, nodes won't move
      simulation
        .force(
          'link',
          d3.forceLink<GraphNode, GraphLink>(links).id((d) => d.id)
        )
        .alpha(0.01); // near-zero alpha so it settles immediately

      // Zoom-to-fit the full Clos layout
      const fitScale = Math.min(
        width / canvasWidth,
        height / canvasHeight,
        1.0  // don't zoom in beyond 1:1
      ) * 0.95; // small margin
      const fitX = (width - canvasWidth * fitScale) / 2;
      const fitY = (height - canvasHeight * fitScale) / 2;
      svg.call(zoom.transform, d3.zoomIdentity.translate(fitX, fitY).scale(fitScale));
    } else if (layoutMode === 'polarfly') {
      // ── PolarFly layout ──
      // Quadric ring at center, cluster petals radiating outward like fan blades.
      // 2D top-down projection of the PolarFly cylinder layout.
      const pfLayout = buildPolarFlyClusters(nodes, adjacency, degreeMap);
      const { clusters: pfClusters, quadricNodes } = pfLayout;

      const nonQuadricClusters = pfClusters.filter(c => c.type === 'nonquadric');
      const nC = nonQuadricClusters.length;
      const q = quadricNodes.length; // approximate q+1

      // Sizing: scale ring radius with node count
      const baseRadius = Math.min(width, height) * 0.18;
      const ringRadius = Math.max(baseRadius, 80 + q * 4);
      const cx = width / 2;
      const cy = height / 2;

      // Place quadric nodes in a ring
      for (let i = 0; i < quadricNodes.length; i++) {
        const theta = (2 * Math.PI * i) / quadricNodes.length - Math.PI / 2;
        const node = nodes.find(n => n.id === quadricNodes[i]);
        if (node) {
          node.x = cx + ringRadius * Math.cos(theta);
          node.y = cy + ringRadius * Math.sin(theta);
          node.fx = node.x;
          node.fy = node.y;
        }
      }

      // Place each non-quadric cluster as a radial petal
      const sliceAngle = nC > 0 ? (2 * Math.PI) / nC : 0;
      const centerDistance = ringRadius * 1.6;  // cluster centers just outside the ring
      const finStartDistance = ringRadius * 2.0; // fins start further out
      const finEndDistance = ringRadius * 3.2;   // fins extend to here

      for (let ci = 0; ci < nonQuadricClusters.length; ci++) {
        const cluster = nonQuadricClusters[ci];
        const baseTheta = sliceAngle * ci - Math.PI / 2;
        const center = cluster.center;
        const others = cluster.nodes.filter(n => n !== center);

        // Place cluster center
        const centerNode = nodes.find(n => n.id === center);
        if (centerNode) {
          centerNode.x = cx + centerDistance * Math.cos(baseTheta);
          centerNode.y = cy + centerDistance * Math.sin(baseTheta);
          centerNode.fx = centerNode.x;
          centerNode.fy = centerNode.y;
        }

        // Discover triangle pairs (a, b) where adj[a] contains b
        const paired = new Set<string>();
        const tris: string[][] = [];
        for (const a of others) {
          if (paired.has(a)) continue;
          const aNeighbors = adjacency.get(a) || new Set();
          const b = others.find(x => x !== a && !paired.has(x) && aNeighbors.has(x));
          if (b) {
            tris.push([a, b]);
            paired.add(a);
            paired.add(b);
          }
        }
        // Unpaired leftovers
        for (const n of others) {
          if (!paired.has(n)) tris.push([n]);
        }

        const nTris = tris.length;
        // Angular spread for fins within this cluster's wedge (70% of wedge)
        const angularSpread = sliceAngle * 0.7;

        tris.forEach((pair, ti) => {
          const finFrac = nTris > 1 ? (ti - (nTris - 1) / 2) / nTris : 0;
          const finTheta = baseTheta + finFrac * angularSpread;

          // Radial distance: interpolate between finStart and finEnd
          const tFrac = nTris > 1 ? ti / (nTris - 1) : 0.5;
          const finR = finStartDistance + (finEndDistance - finStartDistance) * tFrac;

          pair.forEach((nid, ni) => {
            const node = nodes.find(n => n.id === nid);
            if (!node) return;
            // Split pair endpoints slightly in angle so triangles are visible
            const pairOffset = pair.length > 1 ? (ni === 0 ? -0.02 : 0.02) : 0;
            const theta = finTheta + pairOffset;
            // Also split radially: inner/outer for the two endpoints
            const rOffset = pair.length > 1 ? (ni === 0 ? -8 : 8) : 0;
            node.x = cx + (finR + rOffset) * Math.cos(theta);
            node.y = cy + (finR + rOffset) * Math.sin(theta);
            node.fx = node.x;
            node.fy = node.y;
          });
        });
      }

      // Store layout info for node sizing
      const pfScaleFactor = nodes.length > 200 ? 0.5 : nodes.length > 80 ? 0.7 : 1.0;
      (simulation as any).__closLayout = { scaleFactor: pfScaleFactor };

      // Minimal simulation for link rendering
      simulation
        .force(
          'link',
          d3.forceLink<GraphNode, GraphLink>(links).id((d) => d.id)
        )
        .alpha(0.01);

      // Zoom-to-fit
      const maxR = finEndDistance + 40;
      const fitScale = Math.min(
        width / (maxR * 2 + 80),
        height / (maxR * 2 + 80),
        1.0
      ) * 0.95;
      const fitX = (width - width * fitScale) / 2;
      const fitY = (height - height * fitScale) / 2;
      svg.call(zoom.transform, d3.zoomIdentity.translate(fitX, fitY).scale(fitScale));
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

    // Render links — scale stroke for dense fabrics
    const linkSf = (simulation as any).__closLayout?.scaleFactor ?? 1.0;
    const baseLinkWidth = Math.max(0.5, 1.8 * linkSf);
    const baseLinkOpacity = links.length > 2000 ? 0.3 : links.length > 500 ? 0.5 : 0.7;
    const linkGroup = g
      .append('g')
      .attr('class', 'links')
      .selectAll('line')
      .data(links)
      .join('line')
      .attr('stroke', (d) => edgeStyle(d).stroke)
      .attr('stroke-width', baseLinkWidth)
      .attr('stroke-opacity', baseLinkOpacity)
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
            if (layoutMode === 'clos' || layoutMode === 'polarfly') {
              // In Clos mode, snap back to the originally computed position
              // which was stored in __closLayout on the simulation
              const cl = (simulation as any).__closLayout;
              if (cl) {
                const tier = getTier(d.id);
                if (tier === 0) {
                  // Spine: find index in sorted spine tier
                  const spineNodes = nodes.filter((n) => getTier(n.id) === 0);
                  spineNodes.sort((a, b) => a.id.localeCompare(b.id));
                  const idx = spineNodes.findIndex((n) => n.id === d.id);
                  d.fx = cl.padding + cl.spineSpacing * (idx + 1);
                  d.fy = cl.tierYPositions[0];
                } else if (tier === 1) {
                  // Leaf: find index in sorted leaf tier
                  d.fx = cl.leafPositions.get(d.id) ?? d.x;
                  d.fy = cl.tierYPositions[1];
                } else {
                  // Endpoint: find position in parent leaf's column
                  for (const [leafId, eps] of cl.epsByLeaf) {
                    const epIdx = eps.findIndex((e: GraphNode) => e.id === d.id);
                    if (epIdx >= 0) {
                      const leafX = cl.leafPositions.get(leafId) ?? cl.padding;
                      const cols = Math.ceil(Math.sqrt(eps.length));
                      const col = epIdx % cols;
                      const row = Math.floor(epIdx / cols);
                      d.fx = leafX + (col - (cols - 1) / 2) * cl.epSpacingX;
                      d.fy = cl.tierYPositions[2] + row * cl.epSpacingY;
                      break;
                    }
                  }
                }
              }
            } else {
              d.fx = null;
              d.fy = null;
            }
          })
      );

    // Node circles with type/subtype-aware styling
    // Scale factor for large Clos fabrics
    const sf = (simulation as any).__closLayout?.scaleFactor ?? 1.0;
    nodeGroup
      .append('circle')
      .attr('r', (d) => nodeStyle(d, getTier(d.id)).radius * sf)
      .attr('fill', (d) => nodeStyle(d, getTier(d.id)).fill)
      .attr('stroke', (d) => nodeStyle(d, getTier(d.id)).stroke)
      .attr('stroke-width', Math.max(1, 2.5 * sf));

    // Node labels — hide at very small scales, shrink at medium
    const labelSize = Math.max(5, Math.round(10 * sf));
    const showLabels = sf >= 0.35;
    if (showLabels) {
      nodeGroup
        .append('text')
        .text((d) => d.name || d.id)
        .attr('dy', (d) => {
          const tier = getTier(d.id);
          const r = nodeStyle(d, tier).radius * sf;
          return tier === 2 ? r + 10 : -(r + 8);
        })
        .attr('text-anchor', 'middle')
        .attr('fill', '#a0cfdf')
        .attr('font-size', `${labelSize}px`)
        .attr('font-family', 'JetBrains Mono, monospace');
    }

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
        const style = nodeStyle(d, tier);
        const isSelected = selectedNodes.some((n) => n.id === d.id);
        d3.select(this)
          .select('circle')
          .attr('stroke-width', isSelected ? 3.5 * sf : Math.max(1, 2.5 * sf))
          .attr('stroke', isSelected ? '#ff2d55' : style.stroke);
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
        {(['auto', 'clos', 'ring', 'polarfly'] as LayoutMode[]).map((mode) => (
          <button
            key={mode}
            onClick={() => setLayoutMode(mode)}
            className={`px-3 py-1.5 text-xs font-medium transition-colors ${
              layoutMode === mode
                ? 'bg-kraken-ice/20 text-kraken-ice border-kraken-ice'
                : 'text-kraken-muted hover:text-kraken-frost hover:bg-kraken-dark/50'
            }`}
          >
            {mode === 'polarfly' ? 'PolarFly' : mode.charAt(0).toUpperCase() + mode.slice(1)}
          </button>
        ))}
      </div>
      {/* Legend */}
      <div className="absolute bottom-4 right-4 z-20 bg-kraken-navy/80 backdrop-blur-sm border border-kraken-border rounded-lg px-3 py-2">
        <div className="flex items-center gap-4 text-xs text-kraken-muted">
          {layoutMode === 'polarfly' ? (
            <>
              <div className="flex items-center gap-1.5">
                <div className="w-3.5 h-3.5 rounded-full border-2" style={{ borderColor: '#e8c840', background: '#2d1f00' }} />
                <span>W (quadric)</span>
              </div>
              <div className="flex items-center gap-1.5">
                <div className="w-3 h-3 rounded-full border-2" style={{ borderColor: '#40d8e8', background: '#0a3040' }} />
                <span>V1 (hub)</span>
              </div>
              <div className="flex items-center gap-1.5">
                <div className="w-2 h-2 rounded-full border-2" style={{ borderColor: '#2aa0b0', background: '#061e2e' }} />
                <span>V2 (fin)</span>
              </div>
            </>
          ) : (
            <>
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
            </>
          )}
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
