package pathengine

import (
	"container/heap"
	"fmt"
	"math"

	"github.com/jalapeno/syd/internal/graph"
)

// SPFResult holds the output of a single shortest-path computation.
type SPFResult struct {
	// NodeIDs is the ordered list of Node vertex IDs from src to dst inclusive.
	NodeIDs []string
	// EdgeIDs is the ordered list of edge IDs traversed (len == len(NodeIDs)-1).
	EdgeIDs []string
	// Edges is the resolved edge slice for convenient metric aggregation.
	// Edges may be of any type (LinkEdge, BGPSessionEdge, etc.).
	Edges []graph.Edge
	// TotalCost is the sum of edge costs along the path.
	TotalCost float64
}

// Dijkstra runs a constrained weighted shortest-path search from srcID to
// dstID over the Node subgraph. Any edge whose destination is a Node vertex
// is eligible for traversal — LinkEdges, BGPSessionEdges, and any future
// node-to-node edge type — keeping the path engine topology-agnostic.
//
// The ExcludedSet and PathConstraints are applied per-edge and per-node during
// traversal so that disjointness and TE constraints are respected without
// post-processing.
//
// srcID and dstID are never excluded even if they appear in ex.Nodes — they
// are the endpoints and must always be reachable.
func Dijkstra(
	g *graph.Graph,
	srcID, dstID string,
	cf CostFunc,
	constraints graph.PathConstraints,
	ex *ExcludedSet,
) (*SPFResult, error) {
	if srcID == dstID {
		return &SPFResult{NodeIDs: []string{srcID}}, nil
	}

	const inf = math.MaxFloat64

	dist := make(map[string]float64)
	prev := make(map[string]string) // node → previous node
	prevEdge := make(map[string]string) // node → edge used to reach it

	dist[srcID] = 0

	pq := &nodeHeap{{id: srcID, cost: 0}}
	heap.Init(pq)

	visited := make(map[string]bool)

	for pq.Len() > 0 {
		item := heap.Pop(pq).(heapItem)
		u := item.id

		if visited[u] {
			continue
		}
		visited[u] = true

		if u == dstID {
			break
		}

		for _, e := range g.OutEdges(u) {
			v := e.GetDstID()
			// For undirected edges, AddEdge places the edge in outEdges for
			// both endpoints. When traversing from the DstID end, GetDstID()
			// returns the current node — the actual neighbor is GetSrcID().
			if v == u {
				v = e.GetSrcID()
			}
			if v == u {
				continue // genuine self-loop
			}

			// Only traverse edges that lead to a Node vertex. This skips
			// attachment, ownership, reachability, and other non-transit edges
			// while remaining agnostic to the specific edge type used.
			dstVtx := g.GetVertex(v)
			if dstVtx == nil || dstVtx.GetType() != graph.VTNode {
				continue
			}

			// Never exclude the destination node, but do exclude transit nodes.
			if v != dstID && !NodeAllowed(v, ex) {
				continue
			}
			if !EdgeAllowed(e, ex, constraints, g) {
				continue
			}

			edgeCost := cf(e)
			if edgeCost == inf {
				continue
			}

			uDist, ok := dist[u]
			if !ok {
				uDist = inf
			}
			newDist := uDist + edgeCost

			vDist, ok := dist[v]
			if !ok {
				vDist = inf
			}

			if newDist < vDist {
				dist[v] = newDist
				prev[v] = u
				prevEdge[v] = e.GetID()
				heap.Push(pq, heapItem{id: v, cost: newDist})
			}
		}
	}

	if _, reached := dist[dstID]; !reached {
		return nil, fmt.Errorf("no path from %q to %q (constraints or disjointness may be too strict)", srcID, dstID)
	}
	if dist[dstID] == inf {
		return nil, fmt.Errorf("no path from %q to %q (constraints or disjointness may be too strict)", srcID, dstID)
	}

	return reconstructPath(g, srcID, dstID, prev, prevEdge, dist[dstID])
}

func reconstructPath(
	g *graph.Graph,
	srcID, dstID string,
	prev, prevEdge map[string]string,
	totalCost float64,
) (*SPFResult, error) {
	// Walk backwards from dst to src.
	var nodeIDs []string
	var edgeIDs []string

	cur := dstID
	for cur != srcID {
		nodeIDs = append([]string{cur}, nodeIDs...)
		p, ok := prev[cur]
		if !ok {
			return nil, fmt.Errorf("path reconstruction failed: no predecessor for %q", cur)
		}
		edgeIDs = append([]string{prevEdge[cur]}, edgeIDs...)
		cur = p
	}
	nodeIDs = append([]string{srcID}, nodeIDs...)

	// Resolve edge IDs to graph.Edge for convenient metric aggregation.
	edges := make([]graph.Edge, len(edgeIDs))
	for i, eid := range edgeIDs {
		e := g.GetEdge(eid)
		if e == nil {
			return nil, fmt.Errorf("edge %q not found during path reconstruction", eid)
		}
		edges[i] = e
	}

	return &SPFResult{
		NodeIDs:   nodeIDs,
		EdgeIDs:   edgeIDs,
		Edges:     edges,
		TotalCost: totalCost,
	}, nil
}

// --- min-heap for Dijkstra priority queue --------------------------------

type heapItem struct {
	id   string
	cost float64
}

type nodeHeap []heapItem

func (h nodeHeap) Len() int            { return len(h) }
func (h nodeHeap) Less(i, j int) bool  { return h[i].cost < h[j].cost }
func (h nodeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x interface{}) { *h = append(*h, x.(heapItem)) }
func (h *nodeHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
