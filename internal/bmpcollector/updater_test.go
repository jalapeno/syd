package bmpcollector

import (
	"fmt"
	"testing"

	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/srv6"
)

// newStore returns a fresh graph.Store with a single empty graph named "test".
func newStore(t *testing.T) (*graph.Store, *graph.Graph) {
	t.Helper()
	store := graph.NewStore()
	g := graph.New("test")
	store.Put(g)
	return store, g
}

// makeNode returns a minimal graph.Node with the given id.
func makeNode(id string) *graph.Node {
	return &graph.Node{
		BaseVertex: graph.BaseVertex{ID: id, Type: graph.VTNode},
	}
}

// makeLocator returns a locator with the given prefix and algo.
func makeLocator(prefix string, algo uint8) srv6.Locator {
	return srv6.Locator{
		Prefix: prefix,
		AlgoID: algo,
		NodeSID: &srv6.SID{
			Value:    prefix + "1",
			Behavior: srv6.BehaviorEnd,
		},
	}
}

// --- EnsureGraph tests -------------------------------------------------------

func TestEnsureGraph_CreatesNew(t *testing.T) {
	u := NewUpdater()
	store := graph.NewStore()
	g := u.EnsureGraph(store, "topo1")
	if g == nil {
		t.Fatal("EnsureGraph returned nil")
	}
	if store.Get("topo1") != g {
		t.Error("graph not registered in store")
	}
}

func TestEnsureGraph_ReturnsExisting(t *testing.T) {
	u := NewUpdater()
	store := graph.NewStore()
	g1 := u.EnsureGraph(store, "topo1")
	g2 := u.EnsureGraph(store, "topo1")
	if g1 != g2 {
		t.Error("EnsureGraph created a second graph for the same topoID")
	}
}

// --- EnsureNode tests --------------------------------------------------------

func TestEnsureNode_CreatesStub(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	u.EnsureNode(g, "r1")
	if v := g.GetVertex("r1"); v == nil {
		t.Error("expected stub node r1 to be created")
	}
}

func TestEnsureNode_Idempotent(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	// Place a real node first.
	n := makeNode("r1")
	n.Name = "spine-1"
	_ = g.AddVertex(n)
	// EnsureNode must not overwrite it.
	u.EnsureNode(g, "r1")
	v := g.GetVertex("r1")
	if v == nil {
		t.Fatal("node r1 disappeared")
	}
	if node, ok := v.(*graph.Node); ok {
		if node.Name != "spine-1" {
			t.Errorf("EnsureNode clobbered existing node: Name = %q, want %q", node.Name, "spine-1")
		}
	}
}

// --- UpsertNode tests --------------------------------------------------------

func TestUpsertNode_AddNew(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	n := makeNode("r1")
	n.Name = "leaf-1"
	u.UpsertNode(g, n)
	v := g.GetVertex("r1")
	if v == nil {
		t.Fatal("expected node r1 to be present")
	}
	if v.(*graph.Node).Name != "leaf-1" {
		t.Errorf("Name = %q, want leaf-1", v.(*graph.Node).Name)
	}
}

func TestUpsertNode_PreservesLocators(t *testing.T) {
	// If the existing node already has locators and the new message has none,
	// UpsertNode must preserve the accumulated locators.
	u := NewUpdater()
	_, g := newStore(t)

	// Seed with locators (as if LSSRv6SID arrived first).
	existing := makeNode("r1")
	existing.SRv6Locators = []srv6.Locator{makeLocator("fc00:0:1::/48", 0)}
	_ = g.AddVertex(existing)

	// LSNode message arrives with no locators.
	update := makeNode("r1")
	update.Name = "spine-1"
	u.UpsertNode(g, update)

	v := g.GetVertex("r1")
	if v == nil {
		t.Fatal("node r1 missing after upsert")
	}
	node := v.(*graph.Node)
	if node.Name != "spine-1" {
		t.Errorf("Name = %q, want spine-1", node.Name)
	}
	if len(node.SRv6Locators) != 1 {
		t.Errorf("SRv6Locators len = %d, want 1 (locators should be preserved)", len(node.SRv6Locators))
	}
}

func TestUpsertNode_PreservesFlexAlgosAndMSD(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)

	existing := makeNode("r1")
	existing.FlexAlgos = []graph.FlexAlgo{{AlgoID: 128, MetricType: "delay"}}
	existing.NodeMSD = []graph.MSDEntry{{Type: 1, Value: 10}}
	_ = g.AddVertex(existing)

	update := makeNode("r1")
	update.Name = "spine-1"
	u.UpsertNode(g, update)

	node := g.GetVertex("r1").(*graph.Node)
	if len(node.FlexAlgos) != 1 {
		t.Errorf("FlexAlgos len = %d, want 1 (preserved)", len(node.FlexAlgos))
	}
	if len(node.NodeMSD) != 1 {
		t.Errorf("NodeMSD len = %d, want 1 (preserved)", len(node.NodeMSD))
	}
}

func TestUpsertNode_NewLocatorsWin(t *testing.T) {
	// If the incoming node has locators, they replace (not append to) existing.
	u := NewUpdater()
	_, g := newStore(t)

	existing := makeNode("r1")
	existing.SRv6Locators = []srv6.Locator{makeLocator("fc00:0:1::/48", 0)}
	_ = g.AddVertex(existing)

	update := makeNode("r1")
	update.SRv6Locators = []srv6.Locator{makeLocator("fc00:0:2::/48", 0)}
	u.UpsertNode(g, update)

	node := g.GetVertex("r1").(*graph.Node)
	if len(node.SRv6Locators) != 1 {
		t.Fatalf("SRv6Locators len = %d, want 1", len(node.SRv6Locators))
	}
	if node.SRv6Locators[0].Prefix != "fc00:0:2::/48" {
		t.Errorf("locator prefix = %q, want fc00:0:2::/48 (new value wins)", node.SRv6Locators[0].Prefix)
	}
}

// --- UpsertLocator tests -----------------------------------------------------

func TestUpsertLocator_CreatesStubIfNodeAbsent(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	loc := makeLocator("fc00:0:1::/48", 0)
	u.UpsertLocator(g, "r1", loc)

	v := g.GetVertex("r1")
	if v == nil {
		t.Fatal("expected stub node to be created by UpsertLocator")
	}
	node := v.(*graph.Node)
	if len(node.SRv6Locators) != 1 {
		t.Errorf("SRv6Locators len = %d, want 1", len(node.SRv6Locators))
	}
}

func TestUpsertLocator_AppendsNew(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	_ = g.AddVertex(makeNode("r1"))

	u.UpsertLocator(g, "r1", makeLocator("fc00:0:1::/48", 0))
	u.UpsertLocator(g, "r1", makeLocator("fc00:0:1::/48", 128)) // different algo

	node := g.GetVertex("r1").(*graph.Node)
	if len(node.SRv6Locators) != 2 {
		t.Errorf("SRv6Locators len = %d, want 2", len(node.SRv6Locators))
	}
}

func TestUpsertLocator_UpdatesExisting(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	_ = g.AddVertex(makeNode("r1"))

	loc1 := makeLocator("fc00:0:1::/48", 0)
	u.UpsertLocator(g, "r1", loc1)

	// Same prefix+algo, different SID value.
	loc2 := srv6.Locator{
		Prefix:  "fc00:0:1::/48",
		AlgoID:  0,
		NodeSID: &srv6.SID{Value: "fc00:0:1::updated", Behavior: srv6.BehaviorEnd},
	}
	u.UpsertLocator(g, "r1", loc2)

	node := g.GetVertex("r1").(*graph.Node)
	if len(node.SRv6Locators) != 1 {
		t.Errorf("SRv6Locators len = %d, want 1 (update in-place)", len(node.SRv6Locators))
	}
	if node.SRv6Locators[0].NodeSID.Value != "fc00:0:1::updated" {
		t.Errorf("SID value = %q, want fc00:0:1::updated", node.SRv6Locators[0].NodeSID.Value)
	}
}

// --- RemoveLocator tests -----------------------------------------------------

func TestRemoveLocator_RemovesMatching(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)

	n := makeNode("r1")
	n.SRv6Locators = []srv6.Locator{
		makeLocator("fc00:0:1::/48", 0),
		makeLocator("fc00:0:2::/48", 0),
	}
	_ = g.AddVertex(n)

	u.RemoveLocator(g, "r1", "fc00:0:1::/48")

	node := g.GetVertex("r1").(*graph.Node)
	if len(node.SRv6Locators) != 1 {
		t.Errorf("SRv6Locators len = %d, want 1 after remove", len(node.SRv6Locators))
	}
	if node.SRv6Locators[0].Prefix != "fc00:0:2::/48" {
		t.Errorf("remaining locator = %q, want fc00:0:2::/48", node.SRv6Locators[0].Prefix)
	}
}

func TestRemoveLocator_NoOpWhenAbsent(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	_ = g.AddVertex(makeNode("r1"))

	// Should not panic.
	u.RemoveLocator(g, "r1", "fc00:0:99::/48")
}

func TestRemoveLocator_NoOpWhenNodeAbsent(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)

	// Should not panic.
	u.RemoveLocator(g, "nonexistent", "fc00:0:1::/48")
}

// --- UpsertInterface tests ---------------------------------------------------

func TestUpsertInterface(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	_ = g.AddVertex(makeNode("r1")) // owner must exist for OwnershipEdge dst

	iface := &graph.Interface{
		BaseVertex: graph.BaseVertex{ID: "iface:r1/10.0.0.1", Type: graph.VTInterface},
		OwnerNodeID: "r1",
	}
	own := &graph.OwnershipEdge{
		BaseEdge: graph.BaseEdge{
			ID:       "own:iface:r1/10.0.0.1->r1",
			Type:     graph.ETOwnership,
			SrcID:    "iface:r1/10.0.0.1",
			DstID:    "r1",
			Directed: true,
		},
	}
	u.UpsertInterface(g, iface, own)

	if v := g.GetVertex("iface:r1/10.0.0.1"); v == nil {
		t.Error("interface vertex not found after UpsertInterface")
	}
	if e := g.GetEdge("own:iface:r1/10.0.0.1->r1"); e == nil {
		t.Error("ownership edge not found after UpsertInterface")
	}
}

// --- UpsertLinkEdge tests ----------------------------------------------------

func TestUpsertLinkEdge(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	_ = g.AddVertex(makeNode("r1"))
	_ = g.AddVertex(makeNode("r2"))

	edge := &graph.LinkEdge{
		BaseEdge: graph.BaseEdge{
			ID:       "link:r1:r2:10.0.0.1",
			Type:     graph.ETIGPAdjacency,
			SrcID:    "r1",
			DstID:    "r2",
			Directed: true,
		},
		IGPMetric: 10,
	}
	u.UpsertLinkEdge(g, edge)

	if e := g.GetEdge("link:r1:r2:10.0.0.1"); e == nil {
		t.Error("link edge not found after UpsertLinkEdge")
	}
}

// --- UpsertBGPSession tests --------------------------------------------------

func TestUpsertBGPSession_CreatesStubVertices(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	// No pre-existing vertices.
	sess := &graph.BGPSessionEdge{
		BaseEdge: graph.BaseEdge{
			ID:       "bgpsess:192.0.2.1:192.0.2.2",
			Type:     graph.ETBGPSession,
			SrcID:    "192.0.2.1",
			DstID:    "192.0.2.2",
			Directed: true,
		},
		LocalASN:  65001,
		RemoteASN: 65002,
		IsUp:      true,
	}
	u.UpsertBGPSession(g, sess)

	if g.GetVertex("192.0.2.1") == nil {
		t.Error("src stub vertex 192.0.2.1 not created")
	}
	if g.GetVertex("192.0.2.2") == nil {
		t.Error("dst stub vertex 192.0.2.2 not created")
	}
	if e := g.GetEdge("bgpsess:192.0.2.1:192.0.2.2"); e == nil {
		t.Error("BGP session edge not found")
	}
}

// --- RemoveVertex / RemoveEdge tests -----------------------------------------

func TestRemoveVertex(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	_ = g.AddVertex(makeNode("r1"))
	u.RemoveVertex(g, "r1")
	if g.GetVertex("r1") != nil {
		t.Error("expected r1 to be removed")
	}
}

func TestRemoveEdge(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)
	_ = g.AddVertex(makeNode("r1"))
	_ = g.AddVertex(makeNode("r2"))
	edge := &graph.LinkEdge{
		BaseEdge: graph.BaseEdge{
			ID:    "link:r1:r2:1",
			Type:  graph.ETIGPAdjacency,
			SrcID: "r1",
			DstID: "r2",
		},
	}
	_ = g.AddEdge(edge)
	u.RemoveEdge(g, "link:r1:r2:1")
	if g.GetEdge("link:r1:r2:1") != nil {
		t.Error("expected edge to be removed")
	}
}

// --- Concurrency smoke test --------------------------------------------------

func TestUpdater_ConcurrentUpserts(t *testing.T) {
	u := NewUpdater()
	_, g := newStore(t)

	done := make(chan struct{}, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			nID := fmt.Sprintf("r%d", i)
			u.EnsureNode(g, nID)
			loc := makeLocator(fmt.Sprintf("fc00:0:%d::/48", i), 0)
			u.UpsertLocator(g, nID, loc)
			done <- struct{}{}
		}(i)
	}
	// Drain — we just want no data race (run with -race).
	for i := 0; i < 20; i++ {
		<-done
	}
}
