package topology

import (
	"strings"
	"testing"

	"github.com/jalapeno/syd/internal/graph"
)

// minimalDoc is a small but complete topology document used as the baseline
// for build tests. It contains one node, one interface with an implicit
// ownership edge, one endpoint, one attachment edge, and one link edge.
const minimalDoc = `{
  "topology_id": "test-topo",
  "source": "push",
  "nodes": [
    {"id": "leaf-1", "subtype": "switch"}
  ],
  "interfaces": [
    {"id": "leaf-1-eth0", "owner_node_id": "leaf-1"}
  ],
  "endpoints": [
    {"id": "gpu-0", "subtype": "gpu", "addresses": ["10.0.0.1"]}
  ],
  "edges": [
    {"id": "e-att", "type": "attachment", "src_id": "gpu-0", "dst_id": "leaf-1"},
    {"id": "e-adj", "type": "igp_adjacency", "src_id": "leaf-1", "dst_id": "leaf-1-eth0",
     "igp_metric": 10}
  ]
}`

func TestParse_ValidDocument(t *testing.T) {
	doc, err := Parse(strings.NewReader(minimalDoc))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if doc.TopologyID != "test-topo" {
		t.Errorf("want topology_id=test-topo, got %q", doc.TopologyID)
	}
	if len(doc.Nodes) != 1 {
		t.Errorf("want 1 node, got %d", len(doc.Nodes))
	}
	if len(doc.Interfaces) != 1 {
		t.Errorf("want 1 interface, got %d", len(doc.Interfaces))
	}
	if len(doc.Endpoints) != 1 {
		t.Errorf("want 1 endpoint, got %d", len(doc.Endpoints))
	}
}

func TestParse_MissingTopologyID(t *testing.T) {
	_, err := Parse(strings.NewReader(`{"nodes":[]}`))
	if err == nil {
		t.Fatal("expected error for missing topology_id, got nil")
	}
}

func TestBuild_VertexAndEdgeCounts(t *testing.T) {
	doc, err := Parse(strings.NewReader(minimalDoc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, errs := Build(doc)
	if len(errs) != 0 {
		t.Fatalf("unexpected build errors: %v", errs)
	}

	stats := g.Stats()
	if stats.Nodes != 1 {
		t.Errorf("want 1 node, got %d", stats.Nodes)
	}
	if stats.Interfaces != 1 {
		t.Errorf("want 1 interface, got %d", stats.Interfaces)
	}
	if stats.Endpoints != 1 {
		t.Errorf("want 1 endpoint, got %d", stats.Endpoints)
	}
}

func TestBuild_ImplicitOwnershipEdge(t *testing.T) {
	// Build should create an implicit ownership edge from leaf-1-eth0 → leaf-1.
	doc, err := Parse(strings.NewReader(minimalDoc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, errs := Build(doc)
	if len(errs) != 0 {
		t.Fatalf("unexpected build errors: %v", errs)
	}

	// Check that an ownership edge exists from the interface to its node.
	ownEdgeID := "own:leaf-1-eth0->leaf-1"
	e := g.GetEdge(ownEdgeID)
	if e == nil {
		t.Fatalf("implicit ownership edge %q not found", ownEdgeID)
	}
	if e.GetType() != graph.ETOwnership {
		t.Errorf("want ETOwnership, got %s", e.GetType())
	}
}

func TestBuild_UnknownEdgeType(t *testing.T) {
	// An unknown edge type should produce an error but not block the rest of the build.
	const doc = `{
  "topology_id": "t",
  "nodes": [{"id": "A"}, {"id": "B"}],
  "edges": [
    {"id": "bad", "type": "unknown_type", "src_id": "A", "dst_id": "B"},
    {"id": "ok",  "type": "igp_adjacency","src_id": "A", "dst_id": "B", "igp_metric": 10}
  ]
}`
	d, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, errs := Build(d)
	if len(errs) == 0 {
		t.Fatal("expected at least one error for unknown edge type, got none")
	}
	// The valid edge should still have been added.
	if g.GetEdge("ok") == nil {
		t.Error("valid edge 'ok' was not added despite the unknown-type error")
	}
}

func TestBuild_VRFMembershipEdge(t *testing.T) {
	// Verify that a vrf_membership edge type is parsed and built correctly:
	// a VRF vertex, an endpoint, and a directed VRFMembershipEdge between them.
	const doc = `{
  "topology_id": "t",
  "nodes": [{"id": "leaf-1"}],
  "endpoints": [{"id": "gpu-0", "subtype": "gpu"}],
  "vrfs": [{
    "id": "vrf-green",
    "name": "green",
    "owner_node_id": "leaf-1",
    "srv6_udt_sid": {
      "sid": "fc00:0:1:d001::",
      "behavior": "End.DT6"
    }
  }],
  "edges": [
    {"id": "attach:gpu-0:leaf-1", "type": "attachment",
     "src_id": "gpu-0", "dst_id": "leaf-1", "directed": true},
    {"id": "vrfmem:gpu-0:vrf-green", "type": "vrf_membership",
     "src_id": "gpu-0", "dst_id": "vrf-green", "directed": true}
  ]
}`
	d, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, errs := Build(d)
	if len(errs) != 0 {
		t.Fatalf("unexpected build errors: %v", errs)
	}

	// VRF vertex exists.
	v := g.GetVertex("vrf-green")
	if v == nil {
		t.Fatal("vrf-green vertex not found")
	}
	vrf, ok := v.(*graph.VRF)
	if !ok {
		t.Fatalf("vrf-green is not a *graph.VRF")
	}
	if vrf.SRv6uDTSID == nil || vrf.SRv6uDTSID.Value != "fc00:0:1:d001::" {
		t.Errorf("unexpected uDT SID: %+v", vrf.SRv6uDTSID)
	}

	// VRFMembershipEdge exists with the right type.
	e := g.GetEdge("vrfmem:gpu-0:vrf-green")
	if e == nil {
		t.Fatal("vrfmem:gpu-0:vrf-green edge not found")
	}
	if e.GetType() != graph.ETVRFMembership {
		t.Errorf("want ETVRFMembership, got %s", e.GetType())
	}
	if e.GetSrcID() != "gpu-0" || e.GetDstID() != "vrf-green" {
		t.Errorf("edge direction wrong: got %s→%s", e.GetSrcID(), e.GetDstID())
	}

	// Stats reflect the VRF vertex.
	if g.Stats().VRFs != 1 {
		t.Errorf("want 1 VRF in stats, got %d", g.Stats().VRFs)
	}
}

func TestBuild_SRv6SIDPreserved(t *testing.T) {
	// Verify that srv6_locators and srv6_node_sid survive the Parse→Build round-trip.
	const doc = `{
  "topology_id": "t",
  "nodes": [{
    "id": "leaf-1",
    "srv6_locators": [{"prefix": "fc00:0:3::/48", "algo_id": 0,
      "node_sid": {"sid": "fc00:0:3::", "behavior": "End",
        "structure": {"locator_block_len": 32, "locator_node_len": 16,
                      "function_len": 16, "argument_len": 0}}}]
  }]
}`
	d, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, errs := Build(d)
	if len(errs) != 0 {
		t.Fatalf("unexpected build errors: %v", errs)
	}

	v := g.GetVertex("leaf-1")
	if v == nil {
		t.Fatal("leaf-1 vertex not found")
	}
	n, ok := v.(*graph.Node)
	if !ok {
		t.Fatalf("leaf-1 is not a *graph.Node")
	}
	if len(n.SRv6Locators) != 1 {
		t.Fatalf("want 1 locator, got %d", len(n.SRv6Locators))
	}
	loc := n.SRv6Locators[0]
	if loc.NodeSID == nil {
		t.Fatal("locator NodeSID is nil")
	}
	if loc.NodeSID.Value != "fc00:0:3::" {
		t.Errorf("want NodeSID fc00:0:3::, got %s", loc.NodeSID.Value)
	}
	if loc.NodeSID.Structure == nil {
		t.Error("NodeSID Structure should not be nil")
	}
}
