// Package topology handles topology ingestion from various sources.
// Currently only push-via-JSON is implemented. The BMP ingestion adapter
// (using GoBMP) is planned as a future addition.
package topology

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/srv6"
)

// Document is the top-level JSON structure accepted by POST /topology.
// It contains the full set of vertices and edges for a topology. Fields that
// are not relevant to a given deployment (e.g. Prefixes, VRFs for a static
// AI fabric) can be omitted.
type Document struct {
	TopologyID  string            `json:"topology_id"`
	Description string            `json:"description,omitempty"`
	Source      graph.TopologySource `json:"source,omitempty"`

	Nodes      []NodeDoc      `json:"nodes,omitempty"`
	Interfaces []IfaceDoc     `json:"interfaces,omitempty"`
	Endpoints  []EndpointDoc  `json:"endpoints,omitempty"`
	Prefixes   []PrefixDoc    `json:"prefixes,omitempty"`
	VRFs       []VRFDoc       `json:"vrfs,omitempty"`
	Edges      []EdgeDoc      `json:"edges,omitempty"`
}

// --- Vertex document types -----------------------------------------------
// These mirror the graph types but are deliberately kept separate so that the
// JSON surface area is stable and independent of internal model evolution.

type NodeDoc struct {
	ID          string            `json:"id"`
	Subtype     string            `json:"subtype,omitempty"`
	Name        string            `json:"name,omitempty"`
	RouterID    string            `json:"router_id,omitempty"`
	IGPRouterID string            `json:"igp_router_id,omitempty"`
	ASN         uint32            `json:"asn,omitempty"`
	AreaID      string            `json:"area_id,omitempty"`
	DomainID    int64             `json:"domain_id,omitempty"`
	Protocol    string            `json:"protocol,omitempty"`
	SRv6Locators []srv6.Locator  `json:"srv6_locators,omitempty"`
	SRv6NodeSID  *srv6.SID       `json:"srv6_node_sid,omitempty"`
	SRCapability *graph.SRCapability `json:"sr_capability,omitempty"`
	SRAlgorithms []int           `json:"sr_algorithms,omitempty"`
	FlexAlgos    []graph.FlexAlgo `json:"flex_algos,omitempty"`
	NodeMSD      []graph.MSDEntry `json:"node_msd,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

type IfaceDoc struct {
	ID          string            `json:"id"`
	OwnerNodeID string            `json:"owner_node_id"`
	Name        string            `json:"name,omitempty"`
	Addresses   []string          `json:"addresses,omitempty"`
	LinkLocalID uint32            `json:"link_local_id,omitempty"`
	Bandwidth   uint64            `json:"bandwidth_bps,omitempty"`
	SRv6uASIDs  []srv6.UASID     `json:"srv6_ua_sids,omitempty"`
	AdjSIDs     []srv6.AdjSID    `json:"adj_sids,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type EndpointDoc struct {
	ID          string            `json:"id"`
	Subtype     string            `json:"subtype,omitempty"`
	Name        string            `json:"name,omitempty"`
	Addresses   []string          `json:"addresses,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type PrefixDoc struct {
	ID           string            `json:"id"`
	Prefix       string            `json:"prefix"`
	PrefixLen    int32             `json:"prefix_len"`
	PrefixType   string            `json:"prefix_type,omitempty"`
	IGPMetric    uint32            `json:"igp_metric,omitempty"`
	PrefixMetric uint32            `json:"prefix_metric,omitempty"`
	SRv6Locator  *srv6.Locator    `json:"srv6_locator,omitempty"`
	OwnerNodeIDs []string          `json:"owner_node_ids,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

type VRFDoc struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	OwnerNodeID string            `json:"owner_node_id"`
	RD          string            `json:"rd,omitempty"`
	RTImport    []string          `json:"rt_import,omitempty"`
	RTExport    []string          `json:"rt_export,omitempty"`
	SRv6uDTSID  *srv6.SID        `json:"srv6_udt_sid,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// EdgeDoc covers all edge types. The Type field determines which additional
// fields are meaningful.
type EdgeDoc struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	SrcID    string            `json:"src_id"`
	DstID    string            `json:"dst_id"`
	Directed bool              `json:"directed"`
	Labels   map[string]string `json:"labels,omitempty"`

	// LinkEdge / IGPAdjacency fields
	LocalIfaceID  string   `json:"local_iface_id,omitempty"`
	RemoteIfaceID string   `json:"remote_iface_id,omitempty"`
	Protocol      string   `json:"protocol,omitempty"`
	AreaID        string   `json:"area_id,omitempty"`
	DomainID      int64    `json:"domain_id,omitempty"`
	IGPMetric     uint32   `json:"igp_metric,omitempty"`
	TEMetric      uint32   `json:"te_metric,omitempty"`
	AdminGroup    uint32   `json:"admin_group,omitempty"`
	MaxBW         uint64   `json:"max_bw_bps,omitempty"`
	MaxResvBW     uint64   `json:"max_resv_bw_bps,omitempty"`
	UnresvBW      []uint64 `json:"unresv_bw_bps,omitempty"`
	SRLG          []uint32 `json:"srlg,omitempty"`
	UnidirDelay   uint32   `json:"unidir_delay_us,omitempty"`
	UnidirAvailBW uint64   `json:"unidir_avail_bw_bps,omitempty"`

	// BGPSessionEdge fields
	LocalASN  uint32 `json:"local_asn,omitempty"`
	RemoteASN uint32 `json:"remote_asn,omitempty"`
	LocalIP   string `json:"local_ip,omitempty"`
	RemoteIP  string `json:"remote_ip,omitempty"`
	IsUp      bool   `json:"is_up,omitempty"`

	// AttachmentEdge fields
	AccessIfaceID string `json:"access_iface_id,omitempty"`
}

// --- Ingestion -----------------------------------------------------------

// Parse decodes a topology Document from r and returns it.
func Parse(r io.Reader) (*Document, error) {
	var doc Document
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("topology parse: %w", err)
	}
	if doc.TopologyID == "" {
		return nil, fmt.Errorf("topology parse: topology_id is required")
	}
	return &doc, nil
}

// Build converts a Document into a Graph and returns it. Validation errors
// accumulate and are returned together so the caller can report them all at
// once.
func Build(doc *Document) (*graph.Graph, []error) {
	src := doc.Source
	if src == "" {
		src = graph.SourcePush
	}

	g := graph.New(doc.TopologyID)
	var errs []error

	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	// Vertices must be added before edges so that edge validation can check
	// that src/dst exist. Add them in dependency order: nodes first, then
	// interfaces (which reference nodes), then the rest.

	for _, n := range doc.Nodes {
		if n.ID == "" {
			collect(fmt.Errorf("node missing id"))
			continue
		}
		v := &graph.Node{
			BaseVertex: graph.BaseVertex{
				ID:          n.ID,
				Type:        graph.VTNode,
				Labels:      n.Labels,
				Annotations: n.Annotations,
				Source:      src,
			},
			Subtype:      graph.NodeSubtype(n.Subtype),
			Name:         n.Name,
			RouterID:     n.RouterID,
			IGPRouterID:  n.IGPRouterID,
			ASN:          n.ASN,
			AreaID:       n.AreaID,
			DomainID:     n.DomainID,
			Protocol:     n.Protocol,
			SRv6Locators: n.SRv6Locators,
			SRv6NodeSID:  n.SRv6NodeSID,
			SRCapability: n.SRCapability,
			SRAlgorithms: n.SRAlgorithms,
			FlexAlgos:    n.FlexAlgos,
			NodeMSD:      n.NodeMSD,
		}
		collect(g.AddVertex(v))
	}

	for _, ifc := range doc.Interfaces {
		if ifc.ID == "" {
			collect(fmt.Errorf("interface missing id"))
			continue
		}
		if ifc.OwnerNodeID == "" {
			collect(fmt.Errorf("interface %q missing owner_node_id", ifc.ID))
			continue
		}
		v := &graph.Interface{
			BaseVertex: graph.BaseVertex{
				ID:          ifc.ID,
				Type:        graph.VTInterface,
				Labels:      ifc.Labels,
				Annotations: ifc.Annotations,
				Source:      src,
			},
			OwnerNodeID: ifc.OwnerNodeID,
			Name:        ifc.Name,
			Addresses:   ifc.Addresses,
			LinkLocalID: ifc.LinkLocalID,
			Bandwidth:   ifc.Bandwidth,
			SRv6uASIDs:  ifc.SRv6uASIDs,
			AdjSIDs:     ifc.AdjSIDs,
		}
		collect(g.AddVertex(v))
		// Implicit ownership edge: interface → node
		oe := &graph.OwnershipEdge{
			BaseEdge: graph.BaseEdge{
				ID:       fmt.Sprintf("own:%s->%s", ifc.ID, ifc.OwnerNodeID),
				Type:     graph.ETOwnership,
				SrcID:    ifc.ID,
				DstID:    ifc.OwnerNodeID,
				Directed: true,
				Source:   src,
			},
		}
		collect(g.AddEdge(oe))
	}

	for _, ep := range doc.Endpoints {
		if ep.ID == "" {
			collect(fmt.Errorf("endpoint missing id"))
			continue
		}
		v := &graph.Endpoint{
			BaseVertex: graph.BaseVertex{
				ID:          ep.ID,
				Type:        graph.VTEndpoint,
				Labels:      ep.Labels,
				Annotations: ep.Annotations,
				Source:      src,
			},
			Subtype:   graph.EndpointSubtype(ep.Subtype),
			Name:      ep.Name,
			Addresses: ep.Addresses,
			Metadata:  ep.Metadata,
		}
		collect(g.AddVertex(v))
	}

	for _, p := range doc.Prefixes {
		if p.ID == "" {
			collect(fmt.Errorf("prefix missing id"))
			continue
		}
		v := &graph.Prefix{
			BaseVertex: graph.BaseVertex{
				ID:          p.ID,
				Type:        graph.VTPrefix,
				Labels:      p.Labels,
				Annotations: p.Annotations,
				Source:      src,
			},
			Prefix:       p.Prefix,
			PrefixLen:    p.PrefixLen,
			PrefixType:   graph.PrefixType(p.PrefixType),
			IGPMetric:    p.IGPMetric,
			PrefixMetric: p.PrefixMetric,
			SRv6Locator:  p.SRv6Locator,
			OwnerNodeIDs: p.OwnerNodeIDs,
		}
		collect(g.AddVertex(v))
		// Implicit ownership edges: prefix → each owning node
		for _, ownerID := range p.OwnerNodeIDs {
			oe := &graph.OwnershipEdge{
				BaseEdge: graph.BaseEdge{
					ID:       fmt.Sprintf("own:%s->%s", p.ID, ownerID),
					Type:     graph.ETOwnership,
					SrcID:    p.ID,
					DstID:    ownerID,
					Directed: true,
					Source:   src,
				},
			}
			collect(g.AddEdge(oe))
		}
	}

	for _, vrf := range doc.VRFs {
		if vrf.ID == "" {
			collect(fmt.Errorf("vrf missing id"))
			continue
		}
		v := &graph.VRF{
			BaseVertex: graph.BaseVertex{
				ID:          vrf.ID,
				Type:        graph.VTVRF,
				Labels:      vrf.Labels,
				Annotations: vrf.Annotations,
				Source:      src,
			},
			Name:        vrf.Name,
			OwnerNodeID: vrf.OwnerNodeID,
			RD:          vrf.RD,
			RTImport:    vrf.RTImport,
			RTExport:    vrf.RTExport,
			SRv6uDTSID:  vrf.SRv6uDTSID,
		}
		collect(g.AddVertex(v))
		if vrf.OwnerNodeID != "" {
			oe := &graph.OwnershipEdge{
				BaseEdge: graph.BaseEdge{
					ID:       fmt.Sprintf("own:%s->%s", vrf.ID, vrf.OwnerNodeID),
					Type:     graph.ETOwnership,
					SrcID:    vrf.ID,
					DstID:    vrf.OwnerNodeID,
					Directed: true,
					Source:   src,
				},
			}
			collect(g.AddEdge(oe))
		}
	}

	// Edges — after all vertices are loaded.
	for _, ed := range doc.Edges {
		if ed.ID == "" {
			collect(fmt.Errorf("edge missing id"))
			continue
		}
		collect(buildEdge(g, ed, src))
	}

	return g, errs
}

func buildEdge(g *graph.Graph, ed EdgeDoc, src graph.TopologySource) error {
	base := graph.BaseEdge{
		ID:       ed.ID,
		SrcID:    ed.SrcID,
		DstID:    ed.DstID,
		Directed: ed.Directed,
		Labels:   ed.Labels,
		Source:   src,
	}

	switch graph.EdgeType(ed.Type) {
	case graph.ETPhysical, graph.ETIGPAdjacency:
		base.Type = graph.EdgeType(ed.Type)
		e := &graph.LinkEdge{
			BaseEdge:      base,
			LocalIfaceID:  ed.LocalIfaceID,
			RemoteIfaceID: ed.RemoteIfaceID,
			Protocol:      ed.Protocol,
			AreaID:        ed.AreaID,
			DomainID:      ed.DomainID,
			IGPMetric:     ed.IGPMetric,
			TEMetric:      ed.TEMetric,
			AdminGroup:    ed.AdminGroup,
			MaxBW:         ed.MaxBW,
			MaxResvBW:     ed.MaxResvBW,
			UnresvBW:      ed.UnresvBW,
			SRLG:          ed.SRLG,
			UnidirDelay:   ed.UnidirDelay,
			UnidirAvailBW: ed.UnidirAvailBW,
		}
		return g.AddEdge(e)

	case graph.ETBGPSession:
		base.Type = graph.ETBGPSession
		e := &graph.BGPSessionEdge{
			BaseEdge:  base,
			LocalASN:  ed.LocalASN,
			RemoteASN: ed.RemoteASN,
			LocalIP:   ed.LocalIP,
			RemoteIP:  ed.RemoteIP,
			IsUp:      ed.IsUp,
		}
		return g.AddEdge(e)

	case graph.ETAttachment:
		base.Type = graph.ETAttachment
		e := &graph.AttachmentEdge{
			BaseEdge:      base,
			AccessIfaceID: ed.AccessIfaceID,
		}
		return g.AddEdge(e)

	case graph.ETOwnership:
		base.Type = graph.ETOwnership
		return g.AddEdge(&graph.OwnershipEdge{BaseEdge: base})

	case graph.ETVRFMembership:
		base.Type = graph.ETVRFMembership
		return g.AddEdge(&graph.VRFMembershipEdge{BaseEdge: base})

	default:
		return fmt.Errorf("edge %q: unknown type %q", ed.ID, ed.Type)
	}
}
