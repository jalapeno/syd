package bmpcollector

import (
	"encoding/json"
	"testing"

	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/srv6"
)

// newHandlerEnv returns an Updater, a graph.Store, and the four default
// handlers wired to topology "underlay".
func newHandlerEnv() (*Updater, *graph.Store, []MessageHandler) {
	updater := NewUpdater()
	store := graph.NewStore()
	handlers := DefaultHandlers(updater, "underlay")
	return updater, store, handlers
}

// handlerBySubject returns the handler registered for the given subject.
func handlerBySubject(handlers []MessageHandler, subject string) MessageHandler {
	for _, h := range handlers {
		if h.Subject() == subject {
			return h
		}
	}
	return nil
}

// mustJSON marshals v to JSON, panicking on error (test helper only).
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// --- DefaultHandlers registration --------------------------------------------

func TestDefaultHandlers_Subjects(t *testing.T) {
	_, _, handlers := newHandlerEnv()
	want := map[string]bool{
		SubjectLSNode:    false,
		SubjectLSLink:    false,
		SubjectLSSRv6SID: false,
		SubjectPeer:      false,
		SubjectUnicastV4: false,
		SubjectUnicastV6: false,
	}
	for _, h := range handlers {
		if _, ok := want[h.Subject()]; !ok {
			t.Errorf("unexpected handler subject: %q", h.Subject())
		}
		want[h.Subject()] = true
	}
	for subj, seen := range want {
		if !seen {
			t.Errorf("missing handler for subject %q", subj)
		}
	}
}

// --- lsNodeHandler -----------------------------------------------------------

func TestLSNodeHandler_Add(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSNode)

	payload := mustJSON(map[string]any{
		"action":        "add",
		"igp_router_id": "0000.0000.0001",
		"router_id":     "1.1.1.1",
		"name":          "spine-1",
		"asn":           65001,
		"area_id":       "49.0001",
		"protocol":      "IS-IS Level-2",
	})

	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	g := store.Get("underlay")
	if g == nil {
		t.Fatal("underlay graph not created")
	}
	v := g.GetVertex("0000.0000.0001")
	if v == nil {
		t.Fatal("node 0000.0000.0001 not found after add")
	}
	node := v.(*graph.Node)
	if node.Name != "spine-1" {
		t.Errorf("Name = %q, want spine-1", node.Name)
	}
	if node.RouterID != "1.1.1.1" {
		t.Errorf("RouterID = %q, want 1.1.1.1", node.RouterID)
	}
}

func TestLSNodeHandler_Del(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSNode)

	// Add first.
	add := mustJSON(map[string]any{
		"action":        "add",
		"igp_router_id": "0000.0000.0001",
		"name":          "spine-1",
	})
	_ = h.Handle(add, store)

	// Then delete.
	del := mustJSON(map[string]any{
		"action":        "del",
		"igp_router_id": "0000.0000.0001",
	})
	if err := h.Handle(del, store); err != nil {
		t.Fatalf("del returned error: %v", err)
	}

	g := store.Get("underlay")
	if g.GetVertex("0000.0000.0001") != nil {
		t.Error("expected node to be removed after del")
	}
}

func TestLSNodeHandler_EmptyIGPID_Skipped(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSNode)

	// A message with no igp_router_id should be silently skipped.
	payload := mustJSON(map[string]any{
		"action": "add",
		"name":   "unnamed",
	})
	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("Handle returned unexpected error: %v", err)
	}
}

func TestLSNodeHandler_InvalidJSON(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSNode)

	if err := h.Handle([]byte("not json"), store); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// --- lsSRv6SIDHandler --------------------------------------------------------

func TestLSSRv6SIDHandler_Add(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSSRv6SID)

	payload := mustJSON(map[string]any{
		"action":        "add",
		"igp_router_id": "0000.0000.0001",
		"srv6_sid":      "fc00:0:1::",
		"prefix":        "fc00:0:1::",
		"prefix_len":    48,
		"srv6_endpoint_behavior": map[string]any{
			"endpoint_behavior": 0x0041,
			"algo":              0,
			"flag":              0,
		},
		"srv6_sid_structure": map[string]any{
			"locator_block_length": 48,
			"locator_node_length":  16,
			"function_length":      16,
			"argument_length":      0,
		},
	})

	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	g := store.Get("underlay")
	v := g.GetVertex("0000.0000.0001")
	if v == nil {
		t.Fatal("stub node not created by LSSRv6SID handler")
	}
	node := v.(*graph.Node)
	if len(node.SRv6Locators) != 1 {
		t.Fatalf("SRv6Locators len = %d, want 1", len(node.SRv6Locators))
	}
	loc := node.SRv6Locators[0]
	if loc.Prefix != "fc00:0:1::/48" {
		t.Errorf("locator prefix = %q, want fc00:0:1::/48", loc.Prefix)
	}
	if loc.NodeSID == nil {
		t.Fatal("NodeSID is nil")
	}
	if loc.NodeSID.Behavior != srv6.BehaviorEnd {
		t.Errorf("Behavior = %q, want End", loc.NodeSID.Behavior)
	}
	if loc.NodeSID.Structure == nil {
		t.Fatal("SIDStructure is nil")
	}
	if loc.NodeSID.Structure.LocatorBlockLen != 48 {
		t.Errorf("LocatorBlockLen = %d, want 48", loc.NodeSID.Structure.LocatorBlockLen)
	}
}

func TestLSSRv6SIDHandler_Del(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSSRv6SID)

	// Add the locator.
	add := mustJSON(map[string]any{
		"action":        "add",
		"igp_router_id": "0000.0000.0001",
		"srv6_sid":      "fc00:0:1::",
		"prefix":        "fc00:0:1::",
		"prefix_len":    48,
	})
	_ = h.Handle(add, store)

	// Remove it.
	del := mustJSON(map[string]any{
		"action":        "del",
		"igp_router_id": "0000.0000.0001",
		"srv6_sid":      "fc00:0:1::",
		"prefix":        "fc00:0:1::",
		"prefix_len":    48,
	})
	if err := h.Handle(del, store); err != nil {
		t.Fatalf("del returned error: %v", err)
	}

	g := store.Get("underlay")
	node := g.GetVertex("0000.0000.0001").(*graph.Node)
	if len(node.SRv6Locators) != 0 {
		t.Errorf("SRv6Locators len = %d after del, want 0", len(node.SRv6Locators))
	}
}

func TestLSSRv6SIDHandler_MissingSID_Skipped(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSSRv6SID)

	payload := mustJSON(map[string]any{
		"action":        "add",
		"igp_router_id": "0000.0000.0001",
		// No srv6_sid field — handler must skip silently.
	})
	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- lsLinkHandler -----------------------------------------------------------

func TestLSLinkHandler_Add(t *testing.T) {
	// No mt_id field → MTID is nil → IPv4 base topology → must land in
	// the companion "underlay-v4" graph, NOT in "underlay".
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSLink)

	payload := mustJSON(map[string]any{
		"action":               "add",
		"igp_router_id":        "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip":        "10.0.0.1",
		"remote_link_ip":       "10.0.0.2",
		"local_link_id":        1,
		"remote_link_id":       2,
		"igp_metric":           10,
		"max_link_bw_kbps":     100000,
		"protocol":             "IS-IS Level-2",
	})

	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// IPv4 link → companion graph.
	g := store.Get("underlay-v4")
	if g == nil {
		t.Fatal("underlay-v4 companion graph not created for IPv4 link")
	}
	// Primary SRv6 graph should not have been created by this link alone.
	if store.Get("underlay") != nil {
		t.Error("underlay (primary) graph should not be created by an IPv4 link")
	}

	// Both stub nodes should exist in the v4 graph.
	if g.GetVertex("0000.0000.0001") == nil {
		t.Error("local node 0000.0000.0001 not found in v4 graph")
	}
	if g.GetVertex("0000.0000.0002") == nil {
		t.Error("remote node 0000.0000.0002 not found in v4 graph")
	}

	// Interface vertex.
	ifID := "iface:0000.0000.0001/10.0.0.1"
	if g.GetVertex(ifID) == nil {
		t.Errorf("interface vertex %q not found in v4 graph", ifID)
	}

	// Link edge.
	edgeID := "link:0000.0000.0001:0000.0000.0002:10.0.0.1"
	e := g.GetEdge(edgeID)
	if e == nil {
		t.Fatalf("link edge %q not found in v4 graph", edgeID)
	}
	link := e.(*graph.LinkEdge)
	if link.IGPMetric != 10 {
		t.Errorf("IGPMetric = %d, want 10", link.IGPMetric)
	}
}

func TestLSLinkHandler_Del(t *testing.T) {
	// IPv4 link (no mt_id) → lives in underlay-v4; del must target the same graph.
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSLink)

	// Add.
	add := mustJSON(map[string]any{
		"action":               "add",
		"igp_router_id":        "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip":        "10.0.0.1",
		"remote_link_ip":       "10.0.0.2",
	})
	_ = h.Handle(add, store)

	// Delete.
	del := mustJSON(map[string]any{
		"action":               "del",
		"igp_router_id":        "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip":        "10.0.0.1",
		"remote_link_ip":       "10.0.0.2",
	})
	if err := h.Handle(del, store); err != nil {
		t.Fatalf("del returned error: %v", err)
	}

	g := store.Get("underlay-v4")
	edgeID := "link:0000.0000.0001:0000.0000.0002:10.0.0.1"
	if g.GetEdge(edgeID) != nil {
		t.Error("expected link edge to be removed after del")
	}
	ifID := "iface:0000.0000.0001/10.0.0.1"
	if g.GetVertex(ifID) != nil {
		t.Error("expected interface vertex to be removed after del")
	}
}

func TestLSLinkHandler_MissingNodeIDs_Skipped(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSLink)

	// Missing remote_igp_router_id.
	payload := mustJSON(map[string]any{
		"action":        "add",
		"igp_router_id": "0000.0000.0001",
		// remote_igp_router_id absent → translateLSLink returns nils
	})
	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Graph should not have been created (no EnsureGraph call).
	if g := store.Get("underlay"); g != nil {
		if g.GetVertex("0000.0000.0001") != nil {
			t.Error("node should not exist when link was skipped")
		}
	}
}

// --- peerHandler -------------------------------------------------------------

func TestPeerHandler_Up(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectPeer)

	payload := mustJSON(map[string]any{
		"action":       "add",
		"local_bgp_id": "192.0.2.1",
		"remote_ip":    "192.0.2.2",
		"local_asn":    65001,
		"remote_asn":   65002,
	})

	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// Peer messages go to the dedicated "-peers" companion graph.
	g := store.Get("underlay-peers")
	if g == nil {
		t.Fatal("underlay-peers graph not created")
	}
	// Stub node vertices must exist for both BGP endpoints.
	if g.GetVertex("192.0.2.1") == nil {
		t.Error("local BGP-ID vertex 192.0.2.1 not found in peers graph")
	}
	if g.GetVertex("192.0.2.2") == nil {
		t.Error("remote IP vertex 192.0.2.2 not found in peers graph")
	}
	e := g.GetEdge("bgpsess:192.0.2.1:192.0.2.2")
	if e == nil {
		t.Fatal("BGP session edge not found")
	}
	sess := e.(*graph.BGPSessionEdge)
	if !sess.IsUp {
		t.Error("IsUp = false, want true for action=add")
	}
	if sess.LocalASN != 65001 {
		t.Errorf("LocalASN = %d, want 65001", sess.LocalASN)
	}
	if sess.RemoteASN != 65002 {
		t.Errorf("RemoteASN = %d, want 65002", sess.RemoteASN)
	}
}

func TestPeerHandler_Down(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectPeer)

	// Bring up.
	up := mustJSON(map[string]any{
		"action":       "add",
		"local_bgp_id": "192.0.2.1",
		"remote_ip":    "192.0.2.2",
	})
	_ = h.Handle(up, store)

	// Bring down.
	down := mustJSON(map[string]any{
		"action":       "del",
		"local_bgp_id": "192.0.2.1",
		"remote_ip":    "192.0.2.2",
	})
	if err := h.Handle(down, store); err != nil {
		t.Fatalf("down returned error: %v", err)
	}

	g := store.Get("underlay-peers")
	e := g.GetEdge("bgpsess:192.0.2.1:192.0.2.2")
	if e == nil {
		t.Fatal("BGP session edge should still exist after peer down (state updated in place)")
	}
	sess := e.(*graph.BGPSessionEdge)
	if sess.IsUp {
		t.Error("IsUp = true after del, want false")
	}
}

func TestPeerHandler_EmptyLocalBGPID_Skipped(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectPeer)

	payload := mustJSON(map[string]any{
		"action":    "add",
		"remote_ip": "192.0.2.2",
		// local_bgp_id absent
	})
	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Out-of-order arrival: SID before Node -----------------------------------

func TestOutOfOrder_SIDBeforeNode(t *testing.T) {
	// LSSRv6SID arrives before the LSNode message — handler must create a stub
	// node and attach the locator. When LSNode arrives it must preserve the
	// locator that was already accumulated.
	_, store, handlers := newHandlerEnv()
	sidH := handlerBySubject(handlers, SubjectLSSRv6SID)
	nodeH := handlerBySubject(handlers, SubjectLSNode)

	// SID arrives first.
	sidPayload := mustJSON(map[string]any{
		"action":        "add",
		"igp_router_id": "0000.0000.0001",
		"srv6_sid":      "fc00:0:1::",
		"prefix":        "fc00:0:1::",
		"prefix_len":    48,
	})
	_ = sidH.Handle(sidPayload, store)

	// LSNode arrives after.
	nodePayload := mustJSON(map[string]any{
		"action":        "add",
		"igp_router_id": "0000.0000.0001",
		"name":          "spine-1",
	})
	_ = nodeH.Handle(nodePayload, store)

	g := store.Get("underlay")
	v := g.GetVertex("0000.0000.0001")
	if v == nil {
		t.Fatal("node not found")
	}
	node := v.(*graph.Node)
	if node.Name != "spine-1" {
		t.Errorf("Name = %q, want spine-1 (LSNode update must apply)", node.Name)
	}
	if len(node.SRv6Locators) != 1 {
		t.Errorf("SRv6Locators len = %d, want 1 (locator preserved through LSNode upsert)", len(node.SRv6Locators))
	}
}

// --- Multiple locators on the same node --------------------------------------

func TestMultipleLocators_TwoAlgos(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSSRv6SID)

	for _, tc := range []struct {
		sid    string
		prefix string
		algo   int
	}{
		{"fc00:0:1::", "fc00:0:1::", 0},
		{"fc00:0:1::128", "fc00:0:1::", 128},
	} {
		payload := mustJSON(map[string]any{
			"action":        "add",
			"igp_router_id": "0000.0000.0001",
			"srv6_sid":      tc.sid,
			"prefix":        tc.prefix,
			"prefix_len":    48,
			"srv6_endpoint_behavior": map[string]any{
				"endpoint_behavior": 0x0041,
				"algo":              tc.algo,
			},
		})
		_ = h.Handle(payload, store)
	}

	node := store.Get("underlay").GetVertex("0000.0000.0001").(*graph.Node)
	if len(node.SRv6Locators) != 2 {
		t.Errorf("SRv6Locators len = %d, want 2 (different algos → separate locators)", len(node.SRv6Locators))
	}
}

// --- AF graph split (MTID routing) -------------------------------------------

func TestLSLinkHandler_MTID2_GoesToPrimary(t *testing.T) {
	// mt_id=2 (MT-IPv6/SRv6) must land in the primary "underlay" graph.
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSLink)

	payload := mustJSON(map[string]any{
		"action":               "add",
		"igp_router_id":        "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip":        "fc00::1",
		"remote_link_ip":       "fc00::2",
		"igp_metric":           10,
		"mt_id_tlv":            map[string]any{"mt_id": 2},
		"protocol":             "IS-IS Level-2",
	})

	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if store.Get("underlay") == nil {
		t.Fatal("underlay (primary) graph not created for MTID=2 link")
	}
	if store.Get("underlay-v4") != nil {
		t.Error("underlay-v4 companion graph should not be created by an MTID=2 link")
	}

	g := store.Get("underlay")
	edgeID := "link:0000.0000.0001:0000.0000.0002:fc00::1"
	if g.GetEdge(edgeID) == nil {
		t.Errorf("link edge %q not found in primary graph", edgeID)
	}
}

func TestLSLinkHandler_MTID0_GoesToV4(t *testing.T) {
	// Explicit mt_id=0 (IS-IS base topology) must go to "underlay-v4".
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSLink)

	payload := mustJSON(map[string]any{
		"action":               "add",
		"igp_router_id":        "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip":        "10.1.1.1",
		"remote_link_ip":       "10.1.1.2",
		"igp_metric":           5,
		"mt_id_tlv":            map[string]any{"mt_id": 0},
	})

	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if store.Get("underlay-v4") == nil {
		t.Fatal("underlay-v4 not created for explicit MTID=0 link")
	}
	if store.Get("underlay") != nil {
		t.Error("primary underlay should not be created by an MTID=0 link")
	}
}

func TestLSLinkHandler_BothMTIDs_SeparateGraphs(t *testing.T) {
	// Same node pair with MTID=0 and MTID=2 links both present: each lands in
	// its own graph and neither pollutes the other.
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectLSLink)

	v4Link := mustJSON(map[string]any{
		"action":               "add",
		"igp_router_id":        "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip":        "10.0.0.1",
		"mt_id_tlv":            map[string]any{"mt_id": 0},
	})
	v6Link := mustJSON(map[string]any{
		"action":               "add",
		"igp_router_id":        "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip":        "fc00::1",
		"mt_id_tlv":            map[string]any{"mt_id": 2},
	})

	_ = h.Handle(v4Link, store)
	_ = h.Handle(v6Link, store)

	gV4 := store.Get("underlay-v4")
	gV6 := store.Get("underlay")
	if gV4 == nil || gV6 == nil {
		t.Fatal("expected both underlay and underlay-v4 graphs to exist")
	}

	v4EdgeID := "link:0000.0000.0001:0000.0000.0002:10.0.0.1"
	v6EdgeID := "link:0000.0000.0001:0000.0000.0002:fc00::1"

	if gV4.GetEdge(v4EdgeID) == nil {
		t.Error("IPv4 edge not found in underlay-v4")
	}
	if gV4.GetEdge(v6EdgeID) != nil {
		t.Error("IPv6 edge must not appear in underlay-v4")
	}
	if gV6.GetEdge(v6EdgeID) == nil {
		t.Error("IPv6 edge not found in underlay (primary)")
	}
	if gV6.GetEdge(v4EdgeID) != nil {
		t.Error("IPv4 edge must not appear in underlay (primary)")
	}
}

func TestLSNodeHandler_MirroredToV4Graph(t *testing.T) {
	// When a node arrives after an IPv4 link has created the v4 companion graph,
	// the node must be mirrored into the v4 graph with full data.
	_, store, handlers := newHandlerEnv()
	linkH := handlerBySubject(handlers, SubjectLSLink)
	nodeH := handlerBySubject(handlers, SubjectLSNode)

	// IPv4 link arrives first → creates underlay-v4 with stub nodes.
	_ = linkH.Handle(mustJSON(map[string]any{
		"action":               "add",
		"igp_router_id":        "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip":        "10.0.0.1",
		"mt_id_tlv":            map[string]any{"mt_id": 0},
	}), store)

	// Node arrives later with full data.
	_ = nodeH.Handle(mustJSON(map[string]any{
		"action":        "add",
		"igp_router_id": "0000.0000.0001",
		"name":          "spine-1",
		"router_id":     "1.1.1.1",
	}), store)

	// Must be in both primary (created lazily by node handler) and v4 graph.
	for _, topoID := range []string{"underlay", "underlay-v4"} {
		g := store.Get(topoID)
		if g == nil {
			t.Fatalf("%s graph not found", topoID)
		}
		v := g.GetVertex("0000.0000.0001")
		if v == nil {
			t.Fatalf("node not found in %s", topoID)
		}
		n := v.(*graph.Node)
		if n.Name != "spine-1" {
			t.Errorf("%s: Name = %q, want spine-1", topoID, n.Name)
		}
	}
}

func TestLSNodeHandler_Del_RemovedFromBothGraphs(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	linkH := handlerBySubject(handlers, SubjectLSLink)
	nodeH := handlerBySubject(handlers, SubjectLSNode)

	// Create both graphs.
	_ = linkH.Handle(mustJSON(map[string]any{
		"action": "add", "igp_router_id": "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip": "10.0.0.1", "mt_id_tlv": map[string]any{"mt_id": 0},
	}), store)
	_ = linkH.Handle(mustJSON(map[string]any{
		"action": "add", "igp_router_id": "0000.0000.0001",
		"remote_igp_router_id": "0000.0000.0002",
		"local_link_ip": "fc00::1", "mt_id_tlv": map[string]any{"mt_id": 2},
	}), store)
	_ = nodeH.Handle(mustJSON(map[string]any{
		"action": "add", "igp_router_id": "0000.0000.0001", "name": "spine-1",
	}), store)

	// Delete the node.
	_ = nodeH.Handle(mustJSON(map[string]any{
		"action": "del", "igp_router_id": "0000.0000.0001",
	}), store)

	for _, topoID := range []string{"underlay", "underlay-v4"} {
		g := store.Get(topoID)
		if g == nil {
			continue // graph may not exist if del happened before any link
		}
		if g.GetVertex("0000.0000.0001") != nil {
			t.Errorf("node still present in %s after del", topoID)
		}
	}
}

// --- unicastPrefixHandler ----------------------------------------------------

func TestUnicastPrefixV4Handler_Add(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectUnicastV4)

	payload := mustJSON(map[string]any{
		"action":     "add",
		"prefix":     "192.0.2.0",
		"prefix_len": 24,
		"nexthop":    "10.0.0.1",
		"peer_asn":   65001,
		"origin_as":  65002,
		"is_ipv4":    true,
	})

	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	g := store.Get("underlay-prefixes-v4")
	if g == nil {
		t.Fatal("underlay-prefixes-v4 graph not created")
	}

	// Prefix vertex must exist.
	pfxID := "pfx:192.0.2.0/24"
	v := g.GetVertex(pfxID)
	if v == nil {
		t.Fatalf("prefix vertex %q not found", pfxID)
	}
	pfx := v.(*graph.Prefix)
	if pfx.Prefix != "192.0.2.0/24" {
		t.Errorf("Prefix = %q, want 192.0.2.0/24", pfx.Prefix)
	}
	if pfx.PrefixLen != 24 {
		t.Errorf("PrefixLen = %d, want 24", pfx.PrefixLen)
	}

	// Nexthop stub node must exist.
	nhID := "nh:10.0.0.1"
	if g.GetVertex(nhID) == nil {
		t.Errorf("nexthop node %q not found", nhID)
	}

	// OwnershipEdge linking prefix → nexthop must exist.
	ownID := "pfxown:" + pfxID + ":" + nhID
	if g.GetEdge(ownID) == nil {
		t.Errorf("ownership edge %q not found", ownID)
	}
}

func TestUnicastPrefixV4Handler_Del(t *testing.T) {
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectUnicastV4)

	// Add the prefix first.
	add := mustJSON(map[string]any{
		"action":     "add",
		"prefix":     "192.0.2.0",
		"prefix_len": 24,
		"nexthop":    "10.0.0.1",
		"is_ipv4":    true,
	})
	_ = h.Handle(add, store)

	// Withdraw it.
	del := mustJSON(map[string]any{
		"action":     "del",
		"prefix":     "192.0.2.0",
		"prefix_len": 24,
		"nexthop":    "10.0.0.1",
		"is_ipv4":    true,
	})
	if err := h.Handle(del, store); err != nil {
		t.Fatalf("del returned error: %v", err)
	}

	g := store.Get("underlay-prefixes-v4")
	// OwnershipEdge must be removed.
	pfxID := "pfx:192.0.2.0/24"
	nhID := "nh:10.0.0.1"
	ownID := "pfxown:" + pfxID + ":" + nhID
	if g.GetEdge(ownID) != nil {
		t.Error("ownership edge should be removed after prefix withdrawal")
	}
	// Prefix vertex is intentionally left in place (other nexthops may still advertise it).
	if g.GetVertex(pfxID) == nil {
		t.Error("prefix vertex should remain after withdrawal (other paths may exist)")
	}
}

func TestUnicastPrefixV4Handler_MultipleNexthops(t *testing.T) {
	// The same prefix announced by two different nexthops → two OwnershipEdges,
	// same Prefix vertex.
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectUnicastV4)

	for _, nh := range []string{"10.0.0.1", "10.0.0.2"} {
		_ = h.Handle(mustJSON(map[string]any{
			"action":     "add",
			"prefix":     "203.0.113.0",
			"prefix_len": 24,
			"nexthop":    nh,
			"is_ipv4":    true,
		}), store)
	}

	g := store.Get("underlay-prefixes-v4")
	pfxID := "pfx:203.0.113.0/24"
	// Prefix vertex shared.
	if g.GetVertex(pfxID) == nil {
		t.Fatal("prefix vertex not found")
	}
	// Two distinct ownership edges.
	for _, nh := range []string{"nh:10.0.0.1", "nh:10.0.0.2"} {
		ownID := "pfxown:" + pfxID + ":" + nh
		if g.GetEdge(ownID) == nil {
			t.Errorf("ownership edge %q not found", ownID)
		}
	}
}

func TestUnicastPrefixV6Handler_Add(t *testing.T) {
	// IPv6 nexthops may be "primary,link-local" — only the primary is used.
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectUnicastV6)

	payload := mustJSON(map[string]any{
		"action":     "add",
		"prefix":     "2001:db8::",
		"prefix_len": 32,
		"nexthop":    "2001:db8:1::1,fe80::1",
		"is_ipv4":    false,
	})

	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	g := store.Get("underlay-prefixes-v6")
	if g == nil {
		t.Fatal("underlay-prefixes-v6 graph not created")
	}

	pfxID := "pfx:2001:db8::/32"
	nhID := "nh:2001:db8:1::1" // link-local part stripped
	if g.GetVertex(pfxID) == nil {
		t.Errorf("prefix vertex %q not found", pfxID)
	}
	if g.GetVertex(nhID) == nil {
		t.Errorf("nexthop node %q not found (link-local part must be stripped)", nhID)
	}
	ownID := "pfxown:" + pfxID + ":" + nhID
	if g.GetEdge(ownID) == nil {
		t.Errorf("ownership edge %q not found", ownID)
	}
}

func TestUnicastPrefixHandler_EOR_Skipped(t *testing.T) {
	// End-of-RIB marker has an empty prefix field — must be silently skipped.
	_, store, handlers := newHandlerEnv()
	h := handlerBySubject(handlers, SubjectUnicastV4)

	payload := mustJSON(map[string]any{
		"action":  "add",
		"is_eor":  true,
		"is_ipv4": true,
		// prefix absent
	})
	if err := h.Handle(payload, store); err != nil {
		t.Fatalf("EoR must not return an error: %v", err)
	}
	// No graph should have been created.
	if store.Get("underlay-prefixes-v4") != nil {
		t.Error("graph must not be created for EoR message")
	}
}
