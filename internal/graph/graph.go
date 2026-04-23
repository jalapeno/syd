package graph

import (
	"fmt"
	"sync"
)

// Graph is a thread-safe in-memory typed property graph. It stores vertices
// and edges by ID, and maintains forward and reverse adjacency indexes so that
// path computation can efficiently enumerate neighbors.
//
// The graph is keyed by topology ID so that the controller can maintain
// multiple independent topology views simultaneously (e.g. underlay fabric,
// overlay VPN, per-tenant view).
type Graph struct {
	mu       sync.RWMutex
	id       string
	vertices map[string]Vertex
	edges    map[string]Edge

	// outEdges[vertexID] → slice of edge IDs leaving that vertex
	outEdges map[string][]string
	// inEdges[vertexID] → slice of edge IDs arriving at that vertex
	inEdges map[string][]string

	// writeSeq is incremented on every structural write (AddVertex, AddEdge,
	// RemoveVertex, RemoveEdge). It is read by the auto-compose poller to
	// detect in-place mutations made by the BMP collector — which never calls
	// store.Put after the initial graph creation, so the store-level version
	// counter alone is insufficient for staleness detection.
	writeSeq int64
}

// New creates an empty graph with the given topology ID.
func New(id string) *Graph {
	return &Graph{
		id:       id,
		vertices: make(map[string]Vertex),
		edges:    make(map[string]Edge),
		outEdges: make(map[string][]string),
		inEdges:  make(map[string][]string),
	}
}

// ID returns the topology identifier.
func (g *Graph) ID() string { return g.id }

// WriteSeq returns the number of structural write operations (AddVertex,
// AddEdge, RemoveVertex, RemoveEdge) performed on this graph since creation.
// Callers can detect in-place mutations by comparing snapshots of this value.
func (g *Graph) WriteSeq() int64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.writeSeq
}

// --- Vertex operations ---------------------------------------------------

// AddVertex inserts or replaces a vertex. The vertex's ID must be non-empty.
func (g *Graph) AddVertex(v Vertex) error {
	if v.GetID() == "" {
		return fmt.Errorf("vertex ID must not be empty")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.vertices[v.GetID()] = v
	g.writeSeq++
	return nil
}

// GetVertex returns the vertex with the given ID, or nil if not found.
func (g *Graph) GetVertex(id string) Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.vertices[id]
}

// RemoveVertex removes a vertex and all edges incident to it.
func (g *Graph) RemoveVertex(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Collect all edge IDs touching this vertex.
	toRemove := make(map[string]struct{})
	for _, eid := range g.outEdges[id] {
		toRemove[eid] = struct{}{}
	}
	for _, eid := range g.inEdges[id] {
		toRemove[eid] = struct{}{}
	}

	// Remove each incident edge from the adjacency index and edge map.
	for eid := range toRemove {
		if e, ok := g.edges[eid]; ok {
			g.removeEdgeFromIndex(e)
			delete(g.edges, eid)
		}
	}

	delete(g.outEdges, id)
	delete(g.inEdges, id)
	delete(g.vertices, id)
	g.writeSeq++
}

// VerticesByType returns all vertices of the given type.
func (g *Graph) VerticesByType(vt VertexType) []Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var result []Vertex
	for _, v := range g.vertices {
		if v.GetType() == vt {
			result = append(result, v)
		}
	}
	return result
}

// AllVertices returns a snapshot of all vertices.
func (g *Graph) AllVertices() []Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Vertex, 0, len(g.vertices))
	for _, v := range g.vertices {
		out = append(out, v)
	}
	return out
}

// --- Edge operations -----------------------------------------------------

// AddEdge inserts or replaces an edge. Both SrcID and DstID must refer to
// vertices already present in the graph.
func (g *Graph) AddEdge(e Edge) error {
	if e.GetID() == "" {
		return fmt.Errorf("edge ID must not be empty")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.vertices[e.GetSrcID()]; !ok {
		return fmt.Errorf("src vertex %q not found", e.GetSrcID())
	}
	if _, ok := g.vertices[e.GetDstID()]; !ok {
		return fmt.Errorf("dst vertex %q not found", e.GetDstID())
	}

	// If replacing, remove old index entries first.
	if old, ok := g.edges[e.GetID()]; ok {
		g.removeEdgeFromIndex(old)
	}

	g.edges[e.GetID()] = e
	g.outEdges[e.GetSrcID()] = append(g.outEdges[e.GetSrcID()], e.GetID())
	g.inEdges[e.GetDstID()] = append(g.inEdges[e.GetDstID()], e.GetID())
	if !e.IsDirected() {
		// Undirected edges are traversable in both directions.
		g.outEdges[e.GetDstID()] = append(g.outEdges[e.GetDstID()], e.GetID())
		g.inEdges[e.GetSrcID()] = append(g.inEdges[e.GetSrcID()], e.GetID())
	}
	g.writeSeq++
	return nil
}

// GetEdge returns the edge with the given ID, or nil if not found.
func (g *Graph) GetEdge(id string) Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.edges[id]
}

// RemoveEdge removes the edge with the given ID.
func (g *Graph) RemoveEdge(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, ok := g.edges[id]; ok {
		g.removeEdgeFromIndex(e)
		delete(g.edges, id)
		g.writeSeq++
	}
}

// OutEdges returns all edges leaving the given vertex (directed away from it).
func (g *Graph) OutEdges(vertexID string) []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.edgeSlice(g.outEdges[vertexID])
}

// InEdges returns all edges arriving at the given vertex.
func (g *Graph) InEdges(vertexID string) []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.edgeSlice(g.inEdges[vertexID])
}

// Neighbors returns the IDs of all vertices reachable from vertexID by
// following outgoing edges of the given types. If edgeTypes is empty, all
// outgoing edge types are followed.
func (g *Graph) Neighbors(vertexID string, edgeTypes ...EdgeType) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	filter := make(map[EdgeType]struct{}, len(edgeTypes))
	for _, et := range edgeTypes {
		filter[et] = struct{}{}
	}

	var result []string
	for _, eid := range g.outEdges[vertexID] {
		e := g.edges[eid]
		if len(filter) > 0 {
			if _, ok := filter[e.GetType()]; !ok {
				continue
			}
		}
		result = append(result, e.GetDstID())
	}
	return result
}

// AllEdges returns a snapshot of all edges.
func (g *Graph) AllEdges() []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Edge, 0, len(g.edges))
	for _, e := range g.edges {
		out = append(out, e)
	}
	return out
}

// Stats returns a summary of the graph's current size.
func (g *Graph) Stats() GraphStats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	s := GraphStats{TopologyID: g.id, TotalEdges: len(g.edges)}
	for _, v := range g.vertices {
		s.TotalVertices++
		switch v.GetType() {
		case VTNode:
			s.Nodes++
		case VTInterface:
			s.Interfaces++
		case VTEndpoint:
			s.Endpoints++
		case VTPrefix:
			s.Prefixes++
		case VTVRF:
			s.VRFs++
		}
	}
	return s
}

// --- internal helpers -----------------------------------------------------

func (g *Graph) edgeSlice(ids []string) []Edge {
	out := make([]Edge, 0, len(ids))
	for _, id := range ids {
		if e, ok := g.edges[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

func (g *Graph) removeEdgeFromIndex(e Edge) {
	g.outEdges[e.GetSrcID()] = removeString(g.outEdges[e.GetSrcID()], e.GetID())
	g.inEdges[e.GetDstID()] = removeString(g.inEdges[e.GetDstID()], e.GetID())
	if !e.IsDirected() {
		g.outEdges[e.GetDstID()] = removeString(g.outEdges[e.GetDstID()], e.GetID())
		g.inEdges[e.GetSrcID()] = removeString(g.inEdges[e.GetSrcID()], e.GetID())
	}
}

func removeString(slice []string, s string) []string {
	out := slice[:0]
	for _, v := range slice {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// GraphStats is returned by Graph.Stats().
type GraphStats struct {
	TopologyID     string `json:"topology_id"`
	TotalVertices  int    `json:"total_vertices"`
	TotalEdges     int    `json:"total_edges"`
	Nodes          int    `json:"nodes"`
	Interfaces     int    `json:"interfaces"`
	Endpoints      int    `json:"endpoints"`
	Prefixes       int    `json:"prefixes"`
	VRFs           int    `json:"vrfs"`
}

// Store manages a collection of named Graph instances.
type Store struct {
	mu       sync.RWMutex
	graphs   map[string]*Graph
	versions map[string]int64 // monotonically incremented on each Put
}

// NewStore creates an empty topology store.
func NewStore() *Store {
	return &Store{
		graphs:   make(map[string]*Graph),
		versions: make(map[string]int64),
	}
}

// Put inserts or replaces a graph in the store and increments its version
// counter so that auto-compose loops can detect stale snapshots.
func (s *Store) Put(g *Graph) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.graphs[g.id] = g
	s.versions[g.id]++
}

// Version returns the current write version for the graph with the given ID.
// Returns 0 if the graph has never been Put. Each successful Put increments
// the version by 1, so callers can detect changes by comparing snapshots.
func (s *Store) Version(id string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.versions[id]
}

// Get returns the graph with the given topology ID, or nil if not found.
func (s *Store) Get(id string) *Graph {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.graphs[id]
}

// Delete removes a graph from the store.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.graphs, id)
}

// List returns the IDs of all graphs in the store.
func (s *Store) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.graphs))
	for id := range s.graphs {
		ids = append(ids, id)
	}
	return ids
}
