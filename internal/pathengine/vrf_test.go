package pathengine

import (
	"testing"

	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/srv6"
)

// addVRFFixture adds a VRF vertex and two GPU endpoints to g.
// gpu-1 and gpu-2 attach to leaf-1; gpu-3 and gpu-4 attach to leaf-2.
// All four endpoints are members of vrf-green via VRFMembershipEdges.
// vrf-green is owned by leaf-2 and has a uDT SID fc00:0:4:d001::.
func addVRFFixture(t testing.TB, g *graph.Graph) {
	t.Helper()

	mustAdd(t, g.AddVertex(&graph.VRF{
		BaseVertex:  graph.BaseVertex{ID: "vrf-green", Type: graph.VTVRF},
		Name:        "green",
		OwnerNodeID: "leaf-2",
		SRv6uDTSID: &srv6.SID{
			Value:     "fc00:0:4:d001::",
			Behavior:  srv6.BehaviorEndDT6,
			Structure: f3216,
		},
	}))

	// GPU endpoints on leaf-1
	for _, id := range []string{"gpu-1", "gpu-2"} {
		mustAdd(t, g.AddVertex(&graph.Endpoint{
			BaseVertex: graph.BaseVertex{ID: id, Type: graph.VTEndpoint},
			Subtype:    "gpu",
		}))
		mustAdd(t, g.AddEdge(&graph.AttachmentEdge{
			BaseEdge: graph.BaseEdge{
				ID: "attach:" + id + ":leaf-1", Type: graph.ETAttachment,
				SrcID: id, DstID: "leaf-1", Directed: true,
			},
		}))
		mustAdd(t, g.AddEdge(&graph.VRFMembershipEdge{
			BaseEdge: graph.BaseEdge{
				ID: "vrfmem:" + id + ":vrf-green", Type: graph.ETVRFMembership,
				SrcID: id, DstID: "vrf-green", Directed: true,
			},
		}))
	}

	// GPU endpoints on leaf-2
	for _, id := range []string{"gpu-3", "gpu-4"} {
		mustAdd(t, g.AddVertex(&graph.Endpoint{
			BaseVertex: graph.BaseVertex{ID: id, Type: graph.VTEndpoint},
			Subtype:    "gpu",
		}))
		mustAdd(t, g.AddEdge(&graph.AttachmentEdge{
			BaseEdge: graph.BaseEdge{
				ID: "attach:" + id + ":leaf-2", Type: graph.ETAttachment,
				SrcID: id, DstID: "leaf-2", Directed: true,
			},
		}))
		mustAdd(t, g.AddEdge(&graph.VRFMembershipEdge{
			BaseEdge: graph.BaseEdge{
				ID: "vrfmem:" + id + ":vrf-green", Type: graph.ETVRFMembership,
				SrcID: id, DstID: "vrf-green", Directed: true,
			},
		}))
	}
}

// --- detectTenantVRF ---------------------------------------------------------

func TestDetectTenantVRF_AutoDetect(t *testing.T) {
	g := makeLeafSpineGraph(t)
	addVRFFixture(t, g)

	resolved := []ResolvedEndpoint{
		{EndpointID: "gpu-1", NodeID: "leaf-1"},
		{EndpointID: "gpu-3", NodeID: "leaf-2"},
	}
	vrfID, err := detectTenantVRF(g, resolved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vrfID != "vrf-green" {
		t.Errorf("want vrf-green, got %q", vrfID)
	}
}

func TestDetectTenantVRF_NoMembership(t *testing.T) {
	// Endpoints with no VRFMembershipEdges → returns "" (no VRF / default table).
	g := makeLeafSpineGraph(t)
	resolved := []ResolvedEndpoint{
		{EndpointID: "leaf-1", NodeID: "leaf-1"},
		{EndpointID: "leaf-2", NodeID: "leaf-2"},
	}
	vrfID, err := detectTenantVRF(g, resolved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vrfID != "" {
		t.Errorf("want empty string (no VRF), got %q", vrfID)
	}
}

func TestDetectTenantVRF_CrossVRFError(t *testing.T) {
	g := makeLeafSpineGraph(t)
	addVRFFixture(t, g)

	// Add a second VRF and attach gpu-3 to it instead of vrf-green.
	mustAdd(t, g.AddVertex(&graph.VRF{
		BaseVertex:  graph.BaseVertex{ID: "vrf-red", Type: graph.VTVRF},
		Name:        "red",
		OwnerNodeID: "leaf-2",
		SRv6uDTSID: &srv6.SID{Value: "fc00:0:4:d002::", Behavior: srv6.BehaviorEndDT6},
	}))
	mustAdd(t, g.AddEdge(&graph.VRFMembershipEdge{
		BaseEdge: graph.BaseEdge{
			ID: "vrfmem:gpu-3:vrf-red", Type: graph.ETVRFMembership,
			SrcID: "gpu-3", DstID: "vrf-red", Directed: true,
		},
	}))

	// gpu-1 → vrf-green, gpu-3 → vrf-green AND vrf-red.
	// Because gpu-3 has two membership edges, the second one differs from
	// vrf-green → should trigger cross-VRF error.
	resolved := []ResolvedEndpoint{
		{EndpointID: "gpu-1", NodeID: "leaf-1"},
		{EndpointID: "gpu-3", NodeID: "leaf-2"},
	}
	_, err := detectTenantVRF(g, resolved)
	if err == nil {
		t.Fatal("expected cross-VRF error, got nil")
	}
}

// --- Segment list with auto-detected VRF ------------------------------------

func TestBuildSegmentList_VRFAutoDetectedFromEndpoint(t *testing.T) {
	// Verify the full pipeline: endpoint membership → tenant VRF → uDT SID
	// appended to segment list. This test exercises detectTenantVRF and
	// BuildSegmentList together using the same VRF fixture.
	g := makeLeafSpineGraph(t)
	addVRFFixture(t, g)

	resolved := []ResolvedEndpoint{
		{EndpointID: "gpu-1", NodeID: "leaf-1"},
		{EndpointID: "gpu-3", NodeID: "leaf-2"},
	}

	vrfID, err := detectTenantVRF(g, resolved)
	if err != nil {
		t.Fatalf("detectTenantVRF: %v", err)
	}
	if vrfID != "vrf-green" {
		t.Fatalf("want vrf-green, got %q", vrfID)
	}

	spf := runDijkstra(t, g, "leaf-1", "leaf-2")
	sl, err := BuildSegmentList(g, spf, 0, vrfID, modeUAFull)
	if err != nil {
		t.Fatalf("BuildSegmentList: %v", err)
	}

	// 4 items (uA+uA+uN+uDT) → 2 containers at 32-bit slot width (capacity=3).
	//   Container 0: fc00:0:3:e001:2:e002:4:0   (uA leaf-1-eth0 + uA spine-1-eth1 + uN leaf-2)
	//   Container 1: fc00:0:4:d001::             (uDT vrf-green)
	if len(sl.SIDs) != 2 {
		t.Fatalf("want 2 SID containers, got %d: %v", len(sl.SIDs), sl.SIDs)
	}
	const wantUDT = "fc00:0:4:d001::"
	if sl.SIDs[1] != wantUDT {
		t.Errorf("uDT SID: want %s, got %s", wantUDT, sl.SIDs[1])
	}
}
