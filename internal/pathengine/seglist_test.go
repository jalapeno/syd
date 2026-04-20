package pathengine

import (
	"testing"

	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/srv6"
)

// runDijkstra is a convenience wrapper for tests that need a real SPFResult.
func runDijkstra(t testing.TB, g *graph.Graph, src, dst string) *SPFResult {
	t.Helper()
	spf, err := Dijkstra(g, src, dst, CostFuncFor(MetricIGP), graph.PathConstraints{}, NewExcludedSet())
	if err != nil {
		t.Fatalf("Dijkstra(%s→%s): %v", src, dst, err)
	}
	return spf
}

func TestBuildSegmentList_UAChainWithUNAnchor(t *testing.T) {
	// Forward path leaf-1→leaf-2 through spine-1.
	// uA funcLen=16, uN funcLen=0 → maxFuncLen=16, slot=32bits=4bytes, capacity=3.
	// Slot bytes [4:8] from each SID:
	//   leaf-1-eth0 uA fc00:0:3:e001:: → 0x00,0x03,0xe0,0x01
	//   spine-1-eth1 uA fc00:0:2:e002:: → 0x00,0x02,0xe0,0x02
	//   leaf-2 uN fc00:0:4::           → 0x00,0x04,0x00,0x00
	// Container: fc00:0000:0003:e001:0002:e002:0004:0000 = fc00:0:3:e001:2:e002:4:0
	g := makeLeafSpineGraph(t)
	spf := runDijkstra(t, g, "leaf-1", "leaf-2")

	sl, err := BuildSegmentList(g, spf, 0, "", ModeUA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sl.SIDs) != 1 {
		t.Fatalf("want 1 packed container, got %d: %v", len(sl.SIDs), sl.SIDs)
	}
	const want = "fc00:0:3:e001:2:e002:4:0"
	if sl.SIDs[0] != want {
		t.Errorf("want %s, got %s", want, sl.SIDs[0])
	}
	if sl.Flavor != srv6.FlavorHEncapsRed {
		t.Errorf("want H.Encaps.Red flavor, got %s", sl.Flavor)
	}
}

func TestBuildSegmentList_WithTenantUDT(t *testing.T) {
	// Forward path with VRF tenant uDT appended: uA + uA + uN + uDT.
	// maxFuncLen=16 (uA and uDT both funcLen=16), slot=32bits=4bytes, capacity=3.
	// 4 items → 2 containers:
	//   Container 1: fc00:0:3:e001:2:e002:4:0  (uA + uA + uN)
	//   Container 2: fc00:0:4:d001::            (uDT)
	g := makeLeafSpineGraph(t)
	mustAdd(t, g.AddVertex(&graph.VRF{
		BaseVertex:  graph.BaseVertex{ID: "vrf-green", Type: graph.VTVRF},
		OwnerNodeID: "leaf-2",
		SRv6uDTSID: &srv6.SID{
			Value:     "fc00:0:4:d001::",
			Behavior:  srv6.BehaviorEndDT6,
			Structure: f3216,
		},
	}))

	spf := runDijkstra(t, g, "leaf-1", "leaf-2")
	sl, err := BuildSegmentList(g, spf, 0, "vrf-green", ModeUA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sl.SIDs) != 2 {
		t.Fatalf("want 2 containers (capacity=3, 4 items), got %d: %v", len(sl.SIDs), sl.SIDs)
	}
	const want0 = "fc00:0:3:e001:2:e002:4:0"
	const want1 = "fc00:0:4:d001::"
	if sl.SIDs[0] != want0 {
		t.Errorf("container 0: want %s, got %s", want0, sl.SIDs[0])
	}
	if sl.SIDs[1] != want1 {
		t.Errorf("container 1: want %s, got %s", want1, sl.SIDs[1])
	}
}

func TestBuildSegmentList_EmptyPath(t *testing.T) {
	// Zero-edge SPFResult (src==dst) produces a segment list with no SIDs.
	g := makeLeafSpineGraph(t)
	spf := &SPFResult{NodeIDs: []string{"leaf-1"}}

	sl, err := BuildSegmentList(g, spf, 0, "", ModeUA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sl.SIDs) != 0 {
		t.Errorf("want 0 SIDs for empty path, got %v", sl.SIDs)
	}
}

func TestBuildSegmentList_FallbackToNodeSID(t *testing.T) {
	// Build a graph where no interface vertex is attached to the egress link
	// (LocalIfaceID is empty). The segment list must fall back to the source
	// node's uN SID for that hop.
	g := graph.New("test")
	mustAdd(t, g.AddVertex(&graph.Node{
		BaseVertex: graph.BaseVertex{ID: "A", Type: graph.VTNode},
		SRv6Locators: []srv6.Locator{{
			AlgoID:  0,
			NodeSID: &srv6.SID{Value: "fc00:0:1::", Behavior: srv6.BehaviorEnd, Structure: f3216},
		}},
	}))
	mustAdd(t, g.AddVertex(&graph.Node{
		BaseVertex: graph.BaseVertex{ID: "B", Type: graph.VTNode},
		SRv6Locators: []srv6.Locator{{
			AlgoID:  0,
			NodeSID: &srv6.SID{Value: "fc00:0:2::", Behavior: srv6.BehaviorEnd, Structure: f3216},
		}},
	}))
	mustAdd(t, g.AddEdge(&graph.LinkEdge{
		BaseEdge:  graph.BaseEdge{ID: "e-ab", Type: graph.ETIGPAdjacency, SrcID: "A", DstID: "B", Directed: true},
		IGPMetric: 10,
		// LocalIfaceID intentionally left empty — no interface vertex.
	}))

	spf := runDijkstra(t, g, "A", "B")
	sl, err := BuildSegmentList(g, spf, 0, "", ModeUA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Hop A→B falls back to A's uN SID (fc00:0:1::).
	// Final anchor is B's uN SID (fc00:0:2::).
	// Both packed into one container: block + 0001 + 0002 = fc00:0:1:2::
	if len(sl.SIDs) == 0 {
		t.Fatal("want at least one SID, got none")
	}
}

func TestBuildSegmentList_UAOnly(t *testing.T) {
	// leaf-1→spine-1→leaf-2 with ModeUAOnly.
	// Expected 16-bit slots (function-only, placed in node position):
	//   leaf-1-eth0 uA fc00:0:3:e001:: → function e001 → item fc00:0:e001:: {32,16,0,0}
	//   spine-1-eth1 uA fc00:0:2:e002:: → function e002 → item fc00:0:e002:: {32,16,0,0}
	//   leaf-2 uN fc00:0:4::             → node-only item {32,16,0,0}
	// All 16-bit slots: slotBytes=2, capacity=6 → 3 items fit in one container.
	// Container: block(fc00:0:) + e001 + e002 + 0004 = fc00:0:e001:e002:4::
	g := makeLeafSpineGraph(t)
	spf := runDijkstra(t, g, "leaf-1", "leaf-2")

	sl, err := BuildSegmentList(g, spf, 0, "", ModeUAOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sl.SIDs) != 1 {
		t.Fatalf("want 1 packed container, got %d: %v", len(sl.SIDs), sl.SIDs)
	}
	const want = "fc00:0:e001:e002:4::"
	if sl.SIDs[0] != want {
		t.Errorf("want %s, got %s", want, sl.SIDs[0])
	}
}

func TestBuildSegmentList_UAOnly_FallbackToUN(t *testing.T) {
	// Verify that a hop without a uA SID falls back to a 16-bit node slot,
	// and that the mixed list still packs into a single container.
	// Remove leaf-1-eth0's uA SID so the first hop falls back to leaf-1's uN.
	g := makeLeafSpineGraph(t)

	// Replace leaf-1-eth0 with one that has no uA SIDs.
	_ = g.AddVertex(&graph.Interface{
		BaseVertex:  graph.BaseVertex{ID: "leaf-1-eth0", Type: graph.VTInterface},
		OwnerNodeID: "leaf-1",
		// SRv6uASIDs intentionally empty
	})

	spf := runDijkstra(t, g, "leaf-1", "leaf-2")
	sl, err := BuildSegmentList(g, spf, 0, "", ModeUAOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sl.SIDs) != 1 {
		t.Fatalf("want 1 packed container, got %d: %v", len(sl.SIDs), sl.SIDs)
	}
	// Hop 1: no uA → fall back to leaf-1 uN fc00:0:3:: → slot 0003
	// Hop 2: spine-1-eth1 uA fc00:0:2:e002:: → function e002 → slot e002
	// Dst:   leaf-2 uN fc00:0:4:: → slot 0004
	// Container: fc00:0:3:e002:4::
	const want = "fc00:0:3:e002:4::"
	if sl.SIDs[0] != want {
		t.Errorf("want %s, got %s", want, sl.SIDs[0])
	}
}

func TestBuildSegmentList_UNOnly(t *testing.T) {
	// leaf-1→spine-1→leaf-2 with ModeUNOnly.
	// Source (leaf-1) is skipped. Remaining nodes: spine-1, leaf-2.
	// 16-bit node slots: 0002 (spine-1) + 0004 (leaf-2).
	// Container: fc00:0:2:4::
	g := makeLeafSpineGraph(t)
	spf := runDijkstra(t, g, "leaf-1", "leaf-2")

	sl, err := BuildSegmentList(g, spf, 0, "", ModeUNOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sl.SIDs) != 1 {
		t.Fatalf("want 1 packed container, got %d: %v", len(sl.SIDs), sl.SIDs)
	}
	const want = "fc00:0:2:4::"
	if sl.SIDs[0] != want {
		t.Errorf("want %s, got %s", want, sl.SIDs[0])
	}
}

func TestBuildSegmentList_NoStructureFallback(t *testing.T) {
	// SIDs with no SIDStructure cannot be packed → raw SID values returned.
	g := graph.New("test")
	mustAdd(t, g.AddVertex(&graph.Node{
		BaseVertex:  graph.BaseVertex{ID: "A", Type: graph.VTNode},
		SRv6NodeSID: &srv6.SID{Value: "fc00:0:1::", Behavior: srv6.BehaviorEnd},
		// No Structure → SIDItem.Structure will be nil
	}))
	mustAdd(t, g.AddVertex(&graph.Node{
		BaseVertex:  graph.BaseVertex{ID: "B", Type: graph.VTNode},
		SRv6NodeSID: &srv6.SID{Value: "fc00:0:2::", Behavior: srv6.BehaviorEnd},
	}))
	mustAdd(t, g.AddEdge(&graph.LinkEdge{
		BaseEdge:  graph.BaseEdge{ID: "e-ab", Type: graph.ETIGPAdjacency, SrcID: "A", DstID: "B", Directed: true},
		IGPMetric: 10,
	}))

	spf := runDijkstra(t, g, "A", "B")
	sl, err := BuildSegmentList(g, spf, 0, "", ModeUA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expect raw SID strings (no compression) since Structure is nil.
	for _, sid := range sl.SIDs {
		if sid != "fc00:0:1::" && sid != "fc00:0:2::" {
			t.Errorf("unexpected SID value %q in fallback output", sid)
		}
	}
}
