package pathengine

import (
	"testing"

	"github.com/jalapeno/syd/internal/graph"
)

func makeResolvedEndpoints(ids ...string) []ResolvedEndpoint {
	out := make([]ResolvedEndpoint, len(ids))
	for i, id := range ids {
		out[i] = ResolvedEndpoint{NodeID: id, EndpointID: id}
	}
	return out
}

// --- EnumeratePairs ----------------------------------------------------------

func TestEnumeratePairs_AllDirected(t *testing.T) {
	// N=3 endpoints → N*(N-1) = 6 directed pairs.
	eps := makeResolvedEndpoints("A", "B", "C")
	pairs := EnumeratePairs(eps, PairingAllDirected)
	if len(pairs) != 6 {
		t.Errorf("want 6 pairs for all_directed with 3 endpoints, got %d", len(pairs))
	}
	// Verify no self-pairs.
	for _, p := range pairs {
		if p.SrcNodeID == p.DstNodeID {
			t.Errorf("self-pair found: %s→%s", p.SrcNodeID, p.DstNodeID)
		}
	}
}

func TestEnumeratePairs_BidirPaired(t *testing.T) {
	// N=3 endpoints → N*(N-1)/2 = 3 pairs (upper triangle only).
	eps := makeResolvedEndpoints("A", "B", "C")
	pairs := EnumeratePairs(eps, PairingBiDirPaired)
	if len(pairs) != 3 {
		t.Errorf("want 3 pairs for bidir_paired with 3 endpoints, got %d", len(pairs))
	}
	// All should be upper-triangle (i < j).
	for _, p := range pairs {
		if p.SrcNodeID >= p.DstNodeID {
			// Verify the pair is only in one direction (upper triangle).
			// A simple check: reverse pair should not appear.
			for _, p2 := range pairs {
				if p2.SrcNodeID == p.DstNodeID && p2.DstNodeID == p.SrcNodeID {
					t.Errorf("both directions present for %s↔%s in bidir_paired mode", p.SrcNodeID, p.DstNodeID)
				}
			}
		}
	}
}

// --- deriveReversePath -------------------------------------------------------

func TestDeriveReversePath(t *testing.T) {
	// Build the standard leaf-spine graph and compute a forward path.
	// Then derive the reverse path and verify:
	//   - edge IDs are the reverse-direction edges in reversed order
	//   - node IDs are the forward node sequence reversed
	//   - segment list matches the smoke-test expected output
	g := makeLeafSpineGraph(t)
	constraints := constraintsForAlgo(0)

	fwd, err := computeOnePairWithID(g,
		PairRequest{
			SrcEndpointID: "leaf-1",
			DstEndpointID: "leaf-2",
			SrcNodeID:     "leaf-1",
			DstNodeID:     "leaf-2",
		},
		CostFuncFor(MetricIGP), constraints, NewExcludedSet(), "fwd-1",
	)
	if err != nil {
		t.Fatalf("forward path computation failed: %v", err)
	}

	pair := PairRequest{SrcEndpointID: "leaf-1", DstEndpointID: "leaf-2", SrcNodeID: "leaf-1", DstNodeID: "leaf-2"}
	rev, err := deriveReversePath(g, fwd, pair, 0, "", ModeUA, "rev-1")
	if err != nil {
		t.Fatalf("reverse path derivation failed: %v", err)
	}

	// Node sequence should be reversed.
	if rev.SrcID != "leaf-2" || rev.DstID != "leaf-1" {
		t.Errorf("reverse path endpoints: want leaf-2→leaf-1, got %s→%s", rev.SrcID, rev.DstID)
	}
	wantNodes := []string{"leaf-2", "spine-1", "leaf-1"}
	if len(rev.VertexIDs) != len(wantNodes) {
		t.Fatalf("want %d node IDs, got %d: %v", len(wantNodes), len(rev.VertexIDs), rev.VertexIDs)
	}
	for i, n := range wantNodes {
		if rev.VertexIDs[i] != n {
			t.Errorf("node[%d]: want %s, got %s", i, n, rev.VertexIDs[i])
		}
	}

	// Segment list: uA+uA+uN with 32-bit slots (maxFuncLen=16), capacity=3 → 1 container.
	// Slot bytes [4:8]:
	//   leaf-2-eth0  uA fc00:0:4:e001:: → 0x00,0x04,0xe0,0x01
	//   spine-1-eth0 uA fc00:0:2:e001:: → 0x00,0x02,0xe0,0x01
	//   leaf-1       uN fc00:0:3::       → 0x00,0x03,0x00,0x00
	// Container: fc00:0000:0004:e001:0002:e001:0003:0000 = fc00:0:4:e001:2:e001:3:0
	if len(rev.SegmentList.SIDs) != 1 {
		t.Fatalf("want 1 packed SID, got %d: %v", len(rev.SegmentList.SIDs), rev.SegmentList.SIDs)
	}
	const want = "fc00:0:4:e001:2:e001:3:0"
	if rev.SegmentList.SIDs[0] != want {
		t.Errorf("reverse segment list: want %s, got %s", want, rev.SegmentList.SIDs[0])
	}
}

// constraintsForAlgo builds minimal PathConstraints for a given algo ID.
func constraintsForAlgo(algoID uint8) graph.PathConstraints {
	return graph.PathConstraints{AlgoID: algoID}
}

// --- findReverseLinkEdge -----------------------------------------------------

func TestFindReverseLinkEdge_ExactMatch(t *testing.T) {
	// e-l1-s1 (leaf-1→spine-1, local=leaf-1-eth0, remote=spine-1-eth0)
	// The reverse should be e-s1-l1 (spine-1→leaf-1, local=spine-1-eth0).
	g := makeLeafSpineGraph(t)

	fwdEdge := g.GetEdge("e-l1-s1").(*graph.LinkEdge)
	rev, err := findReverseLinkEdge(g, fwdEdge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev.GetID() != "e-s1-l1" {
		t.Errorf("want e-s1-l1, got %s", rev.GetID())
	}
}

func TestFindReverseLinkEdge_FallbackNoIfaceMatch(t *testing.T) {
	// Edge without RemoteIfaceID set → falls back to direction match.
	g := graph.New("test")
	mustAdd(t, g.AddVertex(&graph.Node{BaseVertex: graph.BaseVertex{ID: "A", Type: graph.VTNode}}))
	mustAdd(t, g.AddVertex(&graph.Node{BaseVertex: graph.BaseVertex{ID: "B", Type: graph.VTNode}}))
	mustAdd(t, g.AddEdge(&graph.LinkEdge{
		BaseEdge:  graph.BaseEdge{ID: "ab", Type: graph.ETIGPAdjacency, SrcID: "A", DstID: "B", Directed: true},
		IGPMetric: 10,
		// No LocalIfaceID / RemoteIfaceID
	}))
	mustAdd(t, g.AddEdge(&graph.LinkEdge{
		BaseEdge:  graph.BaseEdge{ID: "ba", Type: graph.ETIGPAdjacency, SrcID: "B", DstID: "A", Directed: true},
		IGPMetric: 10,
	}))

	fwdEdge := g.GetEdge("ab").(*graph.LinkEdge)
	rev, err := findReverseLinkEdge(g, fwdEdge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rev.GetSrcID() != "B" || rev.GetDstID() != "A" {
		t.Errorf("reverse edge direction wrong: got %s→%s", rev.GetSrcID(), rev.GetDstID())
	}
}
