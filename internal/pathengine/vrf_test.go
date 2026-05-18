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
	// detectTenantVRF now returns the VRF Name ("green"), not the vertex ID
	// ("vrf-green"), so that multi-plane topologies with per-endpoint VRF
	// vertices (same name, different IDs) are accepted.
	vrfName, err := detectTenantVRF(g, resolved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vrfName != "green" {
		t.Errorf("want VRF name %q, got %q", "green", vrfName)
	}
}

// TestDetectTenantVRF_MultiVRFSameName verifies that endpoints backed by
// different VRF vertex IDs (multi-plane model) are accepted when all VRF
// vertices share the same name (same tenant).
func TestDetectTenantVRF_MultiVRFSameName(t *testing.T) {
	g := makeLeafSpineGraph(t)

	// Two VRF vertices — different IDs, same name ("green").
	for _, vrfID := range []string{"vrf-green-plane0", "vrf-green-plane1"} {
		mustAdd(t, g.AddVertex(&graph.VRF{
			BaseVertex:  graph.BaseVertex{ID: vrfID, Type: graph.VTVRF},
			Name:        "green",
			OwnerNodeID: "leaf-2",
			SRv6uDTSID:  &srv6.SID{Value: "fc00:0:4:d001::", Behavior: srv6.BehaviorEndDT6},
		}))
	}

	// gpu-1 → vrf-green-plane0; gpu-3 → vrf-green-plane1 (different vertex IDs)
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
				ID: "vrfmem:" + id + ":vrf-green-plane0", Type: graph.ETVRFMembership,
				SrcID: id, DstID: "vrf-green-plane0", Directed: true,
			},
		}))
	}
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
				ID: "vrfmem:" + id + ":vrf-green-plane1", Type: graph.ETVRFMembership,
				SrcID: id, DstID: "vrf-green-plane1", Directed: true,
			},
		}))
	}

	resolved := []ResolvedEndpoint{
		{EndpointID: "gpu-1", NodeID: "leaf-1"},
		{EndpointID: "gpu-3", NodeID: "leaf-2"},
	}
	vrfName, err := detectTenantVRF(g, resolved)
	if err != nil {
		t.Fatalf("unexpected error for same-name VRFs: %v", err)
	}
	if vrfName != "green" {
		t.Errorf("want VRF name %q, got %q", "green", vrfName)
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
	// Verify the full pipeline: endpoint membership → tenant VRF name detection
	// → per-destination VRF vertex resolution → uDT SID appended to segment list.
	//
	// detectTenantVRF now returns the VRF name ("green"). resolveVRFVertex then
	// maps that name to the specific VRF vertex ID for the destination endpoint
	// ("vrf-green"), which BuildSegmentList uses to fetch the uDT SID.
	g := makeLeafSpineGraph(t)
	addVRFFixture(t, g)

	resolved := []ResolvedEndpoint{
		{EndpointID: "gpu-1", NodeID: "leaf-1"},
		{EndpointID: "gpu-3", NodeID: "leaf-2"},
	}

	vrfName, err := detectTenantVRF(g, resolved)
	if err != nil {
		t.Fatalf("detectTenantVRF: %v", err)
	}
	if vrfName != "green" {
		t.Fatalf("want VRF name %q, got %q", "green", vrfName)
	}

	// Resolve the VRF name to the destination endpoint's vertex ID.
	// The destination here is gpu-3 (DstEndpointID in computeOnePairWithID).
	vrfID := resolveVRFVertex(g, "gpu-3", vrfName)
	if vrfID != "vrf-green" {
		t.Fatalf("resolveVRFVertex: want %q, got %q", "vrf-green", vrfID)
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

// TestResolveVRFVertex_ByVertexID verifies that an already-valid vertex ID is
// returned unchanged (backward compatibility with explicit tenant_id usage).
func TestResolveVRFVertex_ByVertexID(t *testing.T) {
	g := makeLeafSpineGraph(t)
	addVRFFixture(t, g)

	got := resolveVRFVertex(g, "gpu-3", "vrf-green")
	if got != "vrf-green" {
		t.Errorf("want %q, got %q", "vrf-green", got)
	}
}

// TestResolveVRFVertex_ByName verifies name-based resolution for multi-plane
// topologies: given a VRF name and a destination endpoint, the function returns
// the vertex ID of the VRF vertex on that endpoint.
func TestResolveVRFVertex_ByName(t *testing.T) {
	g := makeLeafSpineGraph(t)

	// Two VRF vertices with the same name but different IDs.
	for _, vrfID := range []string{"vrf-green-p0", "vrf-green-p1"} {
		mustAdd(t, g.AddVertex(&graph.VRF{
			BaseVertex:  graph.BaseVertex{ID: vrfID, Type: graph.VTVRF},
			Name:        "green",
			OwnerNodeID: "leaf-2",
			SRv6uDTSID:  &srv6.SID{Value: "fc00:0:4:d001::", Behavior: srv6.BehaviorEndDT6},
		}))
	}
	mustAdd(t, g.AddVertex(&graph.Endpoint{
		BaseVertex: graph.BaseVertex{ID: "gpu-dst", Type: graph.VTEndpoint},
		Subtype:    "gpu",
	}))
	mustAdd(t, g.AddEdge(&graph.AttachmentEdge{
		BaseEdge: graph.BaseEdge{
			ID: "attach:gpu-dst:leaf-2", Type: graph.ETAttachment,
			SrcID: "gpu-dst", DstID: "leaf-2", Directed: true,
		},
	}))
	mustAdd(t, g.AddEdge(&graph.VRFMembershipEdge{
		BaseEdge: graph.BaseEdge{
			ID: "vrfmem:gpu-dst:vrf-green-p1", Type: graph.ETVRFMembership,
			SrcID: "gpu-dst", DstID: "vrf-green-p1", Directed: true,
		},
	}))

	got := resolveVRFVertex(g, "gpu-dst", "green")
	if got != "vrf-green-p1" {
		t.Errorf("want %q, got %q", "vrf-green-p1", got)
	}
}

// TestResolveVRFVertex_NotFound verifies that an empty string is returned when
// neither a vertex ID match nor a name match is found.
func TestResolveVRFVertex_NotFound(t *testing.T) {
	g := makeLeafSpineGraph(t)

	got := resolveVRFVertex(g, "leaf-1", "nonexistent-vrf")
	if got != "" {
		t.Errorf("want empty string, got %q", got)
	}
}
