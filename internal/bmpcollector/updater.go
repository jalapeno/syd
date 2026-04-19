package bmpcollector

import (
	"sync"

	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/srv6"
)

// Updater applies graph mutations on behalf of the message handlers.
//
// All public methods acquire mu before modifying the graph so that
// read-modify-write operations (e.g. merging a locator onto an existing node)
// are atomic with respect to other handler goroutines. The graph's own
// RWMutex still protects concurrent reads by the path engine.
type Updater struct {
	mu       sync.Mutex
	onRemove func(topoID, elementID string) // nil = no-op
}

// NewUpdater creates a ready-to-use Updater.
func NewUpdater() *Updater { return &Updater{} }

// SetRemovalCallback registers fn to be called after any vertex or edge is
// removed from the graph. fn receives the topology ID (g.ID()) and the
// removed element's ID. The callback is invoked outside the updater's
// internal mutex to avoid lock-ordering issues with the caller's state.
//
// The primary use case is invalidating active path allocations when a BMP del
// message removes a link or node that an allocated workload path traverses.
func (u *Updater) SetRemovalCallback(fn func(topoID, elementID string)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.onRemove = fn
}

// EnsureGraph returns the named graph from store, creating and registering a
// new empty graph if it does not exist yet.
func (u *Updater) EnsureGraph(store *graph.Store, topoID string) *graph.Graph {
	u.mu.Lock()
	defer u.mu.Unlock()
	if g := store.Get(topoID); g != nil {
		return g
	}
	g := graph.New(topoID)
	store.Put(g)
	return g
}

// EnsureNode creates a minimal stub Node vertex if no vertex with id exists.
// Used before adding edges so the graph's vertex-existence checks don't fail.
func (u *Updater) EnsureNode(g *graph.Graph, id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if g.GetVertex(id) == nil {
		_ = g.AddVertex(&graph.Node{
			BaseVertex: graph.BaseVertex{ID: id, Type: graph.VTNode},
		})
	}
}

// UpsertNode merges node into the graph. If a node with the same ID already
// exists, accumulated data that is absent from node (SRv6 locators, Flex-Algo
// definitions, MSD entries) is preserved so that out-of-order message arrival
// does not lose previously received data.
func (u *Updater) UpsertNode(g *graph.Graph, node *graph.Node) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if existing := g.GetVertex(node.ID); existing != nil {
		if en, ok := existing.(*graph.Node); ok {
			// Preserve locators accumulated from LSSRv6SID messages.
			if len(node.SRv6Locators) == 0 && len(en.SRv6Locators) > 0 {
				node.SRv6Locators = en.SRv6Locators
			}
			// Preserve Flex-Algo and MSD data if the new message omits them.
			if len(node.FlexAlgos) == 0 && len(en.FlexAlgos) > 0 {
				node.FlexAlgos = en.FlexAlgos
			}
			if len(node.NodeMSD) == 0 && len(en.NodeMSD) > 0 {
				node.NodeMSD = en.NodeMSD
			}
		}
	}
	_ = g.AddVertex(node)
}

// UpsertLocator adds or updates a locator on the node identified by nodeID.
// If the node does not yet exist a stub is created; it will be replaced when
// the corresponding LSNode message arrives. The locator is matched by
// (Prefix, AlgoID) — a matching entry is replaced in-place, new entries are
// appended.
func (u *Updater) UpsertLocator(g *graph.Graph, nID string, locator srv6.Locator) {
	u.mu.Lock()
	defer u.mu.Unlock()

	var node *graph.Node
	if v := g.GetVertex(nID); v != nil {
		if n, ok := v.(*graph.Node); ok {
			// Copy to avoid aliasing the stored slice.
			cp := *n
			cp.SRv6Locators = append([]srv6.Locator(nil), n.SRv6Locators...)
			node = &cp
		}
	}
	if node == nil {
		node = &graph.Node{BaseVertex: graph.BaseVertex{ID: nID, Type: graph.VTNode}}
	}

	for i, loc := range node.SRv6Locators {
		if loc.Prefix == locator.Prefix && loc.AlgoID == locator.AlgoID {
			node.SRv6Locators[i] = locator
			_ = g.AddVertex(node)
			return
		}
	}
	node.SRv6Locators = append(node.SRv6Locators, locator)
	_ = g.AddVertex(node)
}

// RemoveLocator removes the locator with the given prefix (and algo 0) from
// the node. It is a no-op when the node or the locator is not found.
func (u *Updater) RemoveLocator(g *graph.Graph, nID, prefix string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	v := g.GetVertex(nID)
	if v == nil {
		return
	}
	n, ok := v.(*graph.Node)
	if !ok {
		return
	}
	cp := *n
	filtered := cp.SRv6Locators[:0]
	for _, loc := range n.SRv6Locators {
		if loc.Prefix != prefix {
			filtered = append(filtered, loc)
		}
	}
	cp.SRv6Locators = filtered
	_ = g.AddVertex(&cp)
}

// UpsertInterface adds or replaces the Interface vertex and its OwnershipEdge.
// The ownership edge src (interface) must already exist as a vertex; this
// method ensures the interface vertex exists before adding the edge.
func (u *Updater) UpsertInterface(g *graph.Graph, iface *graph.Interface, own *graph.OwnershipEdge) {
	u.mu.Lock()
	defer u.mu.Unlock()
	_ = g.AddVertex(iface)
	// OwnershipEdge dst must exist; it is the node vertex. The caller is
	// responsible for calling EnsureNode before UpsertInterface.
	_ = g.AddEdge(own)
}

// UpsertLinkEdge adds or replaces the directed LinkEdge. Both the src and dst
// node vertices must exist; call EnsureNode for each before this method.
func (u *Updater) UpsertLinkEdge(g *graph.Graph, edge *graph.LinkEdge) {
	u.mu.Lock()
	defer u.mu.Unlock()
	_ = g.AddEdge(edge)
}

// UpsertBGPSession adds or replaces a BGPSessionEdge, creating stub Node
// vertices for the src and dst endpoints if they do not already exist.
// In the peers graph, node IDs are IP addresses (BGP IDs / peer IPs), which is
// correct for that graph but would pollute an IS-IS underlay graph with
// IP-addressed stubs — callers should route peer messages to a dedicated
// "<topoID>-peers" graph via the peerHandler.
func (u *Updater) UpsertBGPSession(g *graph.Graph, sess *graph.BGPSessionEdge) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if g.GetVertex(sess.SrcID) == nil {
		_ = g.AddVertex(&graph.Node{
			BaseVertex: graph.BaseVertex{ID: sess.SrcID, Type: graph.VTNode},
		})
	}
	if g.GetVertex(sess.DstID) == nil {
		_ = g.AddVertex(&graph.Node{
			BaseVertex: graph.BaseVertex{ID: sess.DstID, Type: graph.VTNode},
		})
	}
	_ = g.AddEdge(sess)
}

// UpsertPrefix adds or replaces a Prefix vertex and, when a nexthop node and
// ownership edge are provided, ensures the nexthop stub Node exists and inserts
// the OwnershipEdge linking the prefix to its nexthop.
// The same prefix may have multiple nexthops (ECMP): each call adds a separate
// OwnershipEdge; the Prefix vertex itself is shared across all of them.
func (u *Updater) UpsertPrefix(g *graph.Graph, pfx *graph.Prefix, nh *graph.Node, own *graph.OwnershipEdge) {
	u.mu.Lock()
	defer u.mu.Unlock()
	_ = g.AddVertex(pfx)
	if nh == nil {
		return
	}
	if g.GetVertex(nh.ID) == nil {
		_ = g.AddVertex(nh)
	}
	if own != nil {
		_ = g.AddEdge(own)
	}
}

// RemoveVertex removes the vertex and all incident edges from the graph, then
// fires the removal callback (if set) so that dependent allocations can be
// invalidated. The callback is called outside the mutex.
func (u *Updater) RemoveVertex(g *graph.Graph, id string) {
	u.mu.Lock()
	g.RemoveVertex(id)
	fn := u.onRemove
	topoID := g.ID()
	u.mu.Unlock()
	if fn != nil {
		fn(topoID, id)
	}
}

// RemoveEdge removes a single edge from the graph, then fires the removal
// callback (if set) so that dependent allocations can be invalidated. The
// callback is called outside the mutex.
func (u *Updater) RemoveEdge(g *graph.Graph, id string) {
	u.mu.Lock()
	g.RemoveEdge(id)
	fn := u.onRemove
	topoID := g.ID()
	u.mu.Unlock()
	if fn != nil {
		fn(topoID, id)
	}
}
