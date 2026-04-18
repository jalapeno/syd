package bmpcollector

import (
	"math"
	"testing"

	gobmpmsg "github.com/sbezverk/gobmp/pkg/message"
	gobmpsrv6 "github.com/sbezverk/gobmp/pkg/srv6"
	"github.com/jalapeno/scoville/internal/srv6"
)

// --- ID helper tests ---------------------------------------------------------

func TestNodeID(t *testing.T) {
	if got := nodeID("1.2.3.4"); got != "1.2.3.4" {
		t.Errorf("nodeID = %q, want %q", got, "1.2.3.4")
	}
}

func TestIfaceID_WithIP(t *testing.T) {
	got := ifaceID("r1", "10.0.0.1", 0)
	want := "iface:r1/10.0.0.1"
	if got != want {
		t.Errorf("ifaceID = %q, want %q", got, want)
	}
}

func TestIfaceID_FallbackToNum(t *testing.T) {
	got := ifaceID("r1", "", 42)
	want := "iface:r1/42"
	if got != want {
		t.Errorf("ifaceID = %q, want %q", got, want)
	}
}

func TestLinkEdgeID_WithIP(t *testing.T) {
	got := linkEdgeID("r1", "r2", "10.0.0.1", 0)
	want := "link:r1:r2:10.0.0.1"
	if got != want {
		t.Errorf("linkEdgeID = %q, want %q", got, want)
	}
}

func TestLinkEdgeID_FallbackToNum(t *testing.T) {
	got := linkEdgeID("r1", "r2", "", 7)
	want := "link:r1:r2:7"
	if got != want {
		t.Errorf("linkEdgeID = %q, want %q", got, want)
	}
}

func TestOwnershipEdgeID(t *testing.T) {
	got := ownershipEdgeID("iface:r1/eth0", "r1")
	want := "own:iface:r1/eth0->r1"
	if got != want {
		t.Errorf("ownershipEdgeID = %q, want %q", got, want)
	}
}

func TestPeerEdgeID(t *testing.T) {
	got := peerEdgeID("192.0.2.1", "192.0.2.2")
	want := "bgpsess:192.0.2.1:192.0.2.2"
	if got != want {
		t.Errorf("peerEdgeID = %q, want %q", got, want)
	}
}

// --- behaviorFromCode tests --------------------------------------------------

func TestBehaviorFromCode(t *testing.T) {
	tests := []struct {
		code uint16
		want srv6.BehaviorType
	}{
		// End family (0x0001–0x0004)
		{0x0001, srv6.BehaviorEnd},
		{0x0002, srv6.BehaviorEnd},
		{0x0003, srv6.BehaviorEnd},
		{0x0004, srv6.BehaviorEnd},
		// End.X family (0x0005–0x0008)
		{0x0005, srv6.BehaviorEndX},
		{0x0006, srv6.BehaviorEndX},
		{0x0007, srv6.BehaviorEndX},
		{0x0008, srv6.BehaviorEndX},
		// Table lookups
		{0x0012, srv6.BehaviorEndDT6},
		{0x0013, srv6.BehaviorEndDT4},
		{0x0014, srv6.BehaviorEndDT46},
		{0x0015, srv6.BehaviorEndDX6},
		{0x0016, srv6.BehaviorEndDX4},
		// B6 encap
		{0x0010, srv6.BehaviorEndB6Encaps},
		{0x0011, srv6.BehaviorEndB6EncapsRed},
		// uSID micro-segment codes
		{0x0041, srv6.BehaviorEnd},
		{0x0042, srv6.BehaviorEndX},
	}
	for _, tc := range tests {
		got := behaviorFromCode(tc.code)
		if got != tc.want {
			t.Errorf("behaviorFromCode(0x%04x) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

func TestBehaviorFromCode_Unknown(t *testing.T) {
	got := behaviorFromCode(0xFFFF)
	want := srv6.BehaviorType("0xffff")
	if got != want {
		t.Errorf("behaviorFromCode(unknown) = %q, want %q", got, want)
	}
}

// --- sidStructure tests ------------------------------------------------------

func TestSidStructure_Nil(t *testing.T) {
	if got := sidStructure(nil); got != nil {
		t.Errorf("sidStructure(nil) = %v, want nil", got)
	}
}

func TestSidStructure_FieldMapping(t *testing.T) {
	in := &gobmpsrv6.SIDStructure{
		LBLength:  48,
		LNLength:  16,
		FunLength: 16,
		ArgLength: 8,
	}
	got := sidStructure(in)
	if got == nil {
		t.Fatal("sidStructure returned nil for non-nil input")
	}
	if got.LocatorBlockLen != 48 {
		t.Errorf("LocatorBlockLen = %d, want 48", got.LocatorBlockLen)
	}
	if got.LocatorNodeLen != 16 {
		t.Errorf("LocatorNodeLen = %d, want 16", got.LocatorNodeLen)
	}
	if got.FunctionLen != 16 {
		t.Errorf("FunctionLen = %d, want 16", got.FunctionLen)
	}
	if got.ArgumentLen != 8 {
		t.Errorf("ArgumentLen = %d, want 8", got.ArgumentLen)
	}
}

// --- translateLSNode tests ---------------------------------------------------

func TestTranslateLSNode(t *testing.T) {
	msg := &gobmpmsg.LSNode{
		IGPRouterID: "0000.0000.0001",
		RouterID:    "1.1.1.1",
		ASN:         65001,
		AreaID:      "49.0001",
		DomainID:    1,
		Protocol:    "IS-IS Level-2",
		Name:        "spine-1",
		RouterHash:  "abc123",
		PeerHash:    "def456",
	}

	node := translateLSNode(msg)

	if node.ID != "0000.0000.0001" {
		t.Errorf("ID = %q, want %q", node.ID, "0000.0000.0001")
	}
	if node.IGPRouterID != "0000.0000.0001" {
		t.Errorf("IGPRouterID = %q, want %q", node.IGPRouterID, "0000.0000.0001")
	}
	if node.RouterID != "1.1.1.1" {
		t.Errorf("RouterID = %q, want %q", node.RouterID, "1.1.1.1")
	}
	if node.ASN != 65001 {
		t.Errorf("ASN = %d, want 65001", node.ASN)
	}
	if node.Name != "spine-1" {
		t.Errorf("Name = %q, want %q", node.Name, "spine-1")
	}
	if node.Protocol != "IS-IS Level-2" {
		t.Errorf("Protocol = %q, want %q", node.Protocol, "IS-IS Level-2")
	}
	if len(node.SRv6Locators) != 0 {
		t.Errorf("SRv6Locators = %v, want empty (locators arrive via LSSRv6SID)", node.SRv6Locators)
	}
}

// --- translateLSSRv6SID tests ------------------------------------------------

func TestTranslateLSSRv6SID_MissingIGPRouterID(t *testing.T) {
	msg := &gobmpmsg.LSSRv6SID{
		IGPRouterID: "",
		SRv6SID:     "fc00:0:1::/48",
	}
	_, _, ok := translateLSSRv6SID(msg)
	if ok {
		t.Error("expected ok=false when IGPRouterID is empty")
	}
}

func TestTranslateLSSRv6SID_MissingSID(t *testing.T) {
	msg := &gobmpmsg.LSSRv6SID{
		IGPRouterID: "0000.0000.0001",
		SRv6SID:     "",
	}
	_, _, ok := translateLSSRv6SID(msg)
	if ok {
		t.Error("expected ok=false when SRv6SID is empty")
	}
}

func TestTranslateLSSRv6SID_NoBehavior(t *testing.T) {
	msg := &gobmpmsg.LSSRv6SID{
		IGPRouterID:          "0000.0000.0001",
		SRv6SID:              "fc00:0:1::",
		Prefix:               "fc00:0:1::",
		PrefixLen:            48,
		SRv6EndpointBehavior: nil,
		SRv6SIDStructure:     nil,
	}
	nID, locator, ok := translateLSSRv6SID(msg)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if nID != "0000.0000.0001" {
		t.Errorf("nodeID = %q, want %q", nID, "0000.0000.0001")
	}
	if locator.Prefix != "fc00:0:1::/48" {
		t.Errorf("locator.Prefix = %q, want %q", locator.Prefix, "fc00:0:1::/48")
	}
	// Default behavior when SRv6EndpointBehavior is nil
	if locator.NodeSID.Behavior != srv6.BehaviorEnd {
		t.Errorf("default Behavior = %q, want %q", locator.NodeSID.Behavior, srv6.BehaviorEnd)
	}
	if locator.NodeSID.Structure != nil {
		t.Errorf("Structure = %v, want nil when SRv6SIDStructure is nil", locator.NodeSID.Structure)
	}
}

func TestTranslateLSSRv6SID_WithBehaviorAndStructure(t *testing.T) {
	msg := &gobmpmsg.LSSRv6SID{
		IGPRouterID: "0000.0000.0002",
		SRv6SID:     "fc00:0:2::",
		Prefix:      "fc00:0:2::",
		PrefixLen:   48,
		SRv6EndpointBehavior: &gobmpsrv6.EndpointBehavior{
			EndpointBehavior: 0x0041, // uN → End
			Algorithm:        128,
		},
		SRv6SIDStructure: &gobmpsrv6.SIDStructure{
			LBLength:  48,
			LNLength:  16,
			FunLength: 16,
			ArgLength: 0,
		},
	}

	nID, locator, ok := translateLSSRv6SID(msg)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if nID != "0000.0000.0002" {
		t.Errorf("nodeID = %q, want %q", nID, "0000.0000.0002")
	}
	if locator.NodeSID.Behavior != srv6.BehaviorEnd {
		t.Errorf("Behavior = %q, want End", locator.NodeSID.Behavior)
	}
	if locator.AlgoID != 128 {
		t.Errorf("AlgoID = %d, want 128", locator.AlgoID)
	}
	if locator.NodeSID.Structure == nil {
		t.Fatal("Structure is nil, want non-nil")
	}
	if locator.NodeSID.Structure.LocatorBlockLen != 48 {
		t.Errorf("LocatorBlockLen = %d, want 48", locator.NodeSID.Structure.LocatorBlockLen)
	}
}

// --- translateLSLink tests ---------------------------------------------------

func TestTranslateLSLink_MissingLocalID(t *testing.T) {
	msg := &gobmpmsg.LSLink{
		IGPRouterID:       "",
		RemoteIGPRouterID: "0000.0000.0002",
	}
	iface, edge, own := translateLSLink(msg)
	if iface != nil || edge != nil || own != nil {
		t.Error("expected all nils when local IGPRouterID is empty")
	}
}

func TestTranslateLSLink_MissingRemoteID(t *testing.T) {
	msg := &gobmpmsg.LSLink{
		IGPRouterID:       "0000.0000.0001",
		RemoteIGPRouterID: "",
	}
	iface, edge, own := translateLSLink(msg)
	if iface != nil || edge != nil || own != nil {
		t.Error("expected all nils when remote IGPRouterID is empty")
	}
}

func TestTranslateLSLink_BasicLink(t *testing.T) {
	msg := &gobmpmsg.LSLink{
		IGPRouterID:       "0000.0000.0001",
		RemoteIGPRouterID: "0000.0000.0002",
		LocalLinkIP:       "10.0.0.1",
		RemoteLinkIP:      "10.0.0.2",
		LocalLinkID:       1,
		RemoteLinkID:      2,
		IGPMetric: 10,
		// 12,500,000 bytes/sec = 100 Mbps; exact in float32 (< 2^24).
		MaxLinkBW: math.Float32bits(12_500_000),
		Protocol:  "IS-IS Level-2",
	}

	iface, edge, own := translateLSLink(msg)
	if iface == nil || edge == nil || own == nil {
		t.Fatal("expected non-nil iface, edge, own")
	}

	// Interface vertex
	wantIfID := "iface:0000.0000.0001/10.0.0.1"
	if iface.ID != wantIfID {
		t.Errorf("iface.ID = %q, want %q", iface.ID, wantIfID)
	}
	if iface.OwnerNodeID != "0000.0000.0001" {
		t.Errorf("iface.OwnerNodeID = %q, want %q", iface.OwnerNodeID, "0000.0000.0001")
	}
	if iface.Bandwidth != 100_000_000 { // 12_500_000 bytes/sec * 8 = 100 Mbps
		t.Errorf("iface.Bandwidth = %d, want 100_000_000", iface.Bandwidth)
	}

	// Link edge
	wantEdgeID := "link:0000.0000.0001:0000.0000.0002:10.0.0.1"
	if edge.ID != wantEdgeID {
		t.Errorf("edge.ID = %q, want %q", edge.ID, wantEdgeID)
	}
	if edge.SrcID != "0000.0000.0001" {
		t.Errorf("edge.SrcID = %q, want 0000.0000.0001", edge.SrcID)
	}
	if edge.DstID != "0000.0000.0002" {
		t.Errorf("edge.DstID = %q, want 0000.0000.0002", edge.DstID)
	}
	if edge.IGPMetric != 10 {
		t.Errorf("edge.IGPMetric = %d, want 10", edge.IGPMetric)
	}
	if edge.MaxBW != 100_000_000 { // 12_500_000 bytes/sec * 8
		t.Errorf("edge.MaxBW = %d, want 100_000_000", edge.MaxBW)
	}

	// RemoteIfaceID computed deterministically
	wantRemoteIfID := "iface:0000.0000.0002/10.0.0.2"
	if edge.RemoteIfaceID != wantRemoteIfID {
		t.Errorf("edge.RemoteIfaceID = %q, want %q", edge.RemoteIfaceID, wantRemoteIfID)
	}

	// Ownership edge
	if own.SrcID != wantIfID {
		t.Errorf("own.SrcID = %q, want %q", own.SrcID, wantIfID)
	}
	if own.DstID != "0000.0000.0001" {
		t.Errorf("own.DstID = %q, want 0000.0000.0001", own.DstID)
	}
}

func TestTranslateLSLink_FallbackLinkID(t *testing.T) {
	// No link IPs — should fall back to numeric link IDs.
	msg := &gobmpmsg.LSLink{
		IGPRouterID:       "0000.0000.0003",
		RemoteIGPRouterID: "0000.0000.0004",
		LocalLinkIP:       "",
		RemoteLinkIP:      "",
		LocalLinkID:       5,
		RemoteLinkID:      6,
	}

	iface, edge, own := translateLSLink(msg)
	if iface == nil || edge == nil || own == nil {
		t.Fatal("expected non-nil results")
	}
	wantIfID := "iface:0000.0000.0003/5"
	if iface.ID != wantIfID {
		t.Errorf("iface.ID = %q, want %q", iface.ID, wantIfID)
	}
	wantEdgeID := "link:0000.0000.0003:0000.0000.0004:5"
	if edge.ID != wantEdgeID {
		t.Errorf("edge.ID = %q, want %q", edge.ID, wantEdgeID)
	}
	wantRemoteIfID := "iface:0000.0000.0004/6"
	if edge.RemoteIfaceID != wantRemoteIfID {
		t.Errorf("edge.RemoteIfaceID = %q, want %q", edge.RemoteIfaceID, wantRemoteIfID)
	}
	_ = own
}

func TestTranslateLSLink_BWConversion(t *testing.T) {
	msg := &gobmpmsg.LSLink{
		IGPRouterID:       "r1",
		RemoteIGPRouterID: "r2",
		LocalLinkIP:       "10.1.1.1",
		// Use small exact float32 values: bytes/sec * 8 = bits/sec.
		// 1,000,000 bytes/sec = 8 Mbps; 500,000 = 4 Mbps; etc. All < 2^24 (exact).
		MaxLinkBW: math.Float32bits(1_000_000),
		MaxResvBW: math.Float32bits(500_000),
		UnResvBW:  []uint32{math.Float32bits(250_000), math.Float32bits(125_000)},
	}

	iface, edge, _ := translateLSLink(msg)
	if iface.Bandwidth != 8_000_000 { // 1_000_000 bytes/sec * 8 = 8 Mbps
		t.Errorf("iface.Bandwidth = %d, want 8_000_000", iface.Bandwidth)
	}
	if edge.MaxBW != 8_000_000 {
		t.Errorf("edge.MaxBW = %d, want 8_000_000", edge.MaxBW)
	}
	if edge.MaxResvBW != 4_000_000 { // 500_000 * 8
		t.Errorf("edge.MaxResvBW = %d, want 4_000_000", edge.MaxResvBW)
	}
	if len(edge.UnresvBW) != 2 || edge.UnresvBW[0] != 2_000_000 { // 250_000 * 8
		t.Errorf("edge.UnresvBW = %v, want [2_000_000 1_000_000]", edge.UnresvBW)
	}
}

// --- translatePeer tests -----------------------------------------------------

func TestTranslatePeer_Up(t *testing.T) {
	msg := &gobmpmsg.PeerStateChange{
		Action:     "add",
		LocalBGPID: "192.0.2.1",
		RemoteIP:   "192.0.2.2",
		LocalASN:   65001,
		RemoteASN:  65002,
	}
	sess := translatePeer(msg)
	if !sess.IsUp {
		t.Error("IsUp = false, want true for action=add")
	}
	if sess.ID != "bgpsess:192.0.2.1:192.0.2.2" {
		t.Errorf("ID = %q, want bgpsess:192.0.2.1:192.0.2.2", sess.ID)
	}
	if sess.LocalASN != 65001 {
		t.Errorf("LocalASN = %d, want 65001", sess.LocalASN)
	}
	if sess.RemoteASN != 65002 {
		t.Errorf("RemoteASN = %d, want 65002", sess.RemoteASN)
	}
}

func TestTranslatePeer_Down(t *testing.T) {
	msg := &gobmpmsg.PeerStateChange{
		Action:     "del",
		LocalBGPID: "192.0.2.1",
		RemoteIP:   "192.0.2.2",
	}
	sess := translatePeer(msg)
	if sess.IsUp {
		t.Error("IsUp = true, want false for action=del")
	}
}

func TestTranslatePeer_CaseInsensitive(t *testing.T) {
	// "ADD" in uppercase should also be treated as up.
	msg := &gobmpmsg.PeerStateChange{
		Action:     "ADD",
		LocalBGPID: "192.0.2.1",
		RemoteIP:   "192.0.2.2",
	}
	sess := translatePeer(msg)
	if !sess.IsUp {
		t.Error("IsUp = false for action=ADD (uppercase), want true")
	}
}
