package pathengine

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/pkg/apitypes"
)

// ResolvedEndpoint pairs an EndpointSpec with the Node vertex ID that the path
// engine will use as the SPF source or destination. For endpoints attached to
// the network via AttachmentEdges, this is the ID of the attached Node, not
// the Endpoint vertex itself.
type ResolvedEndpoint struct {
	Spec       apitypes.EndpointSpec
	EndpointID string // Endpoint vertex ID (may be empty if endpoint is a Node)
	NodeID     string // Node vertex ID to use as SPF src/dst
}

// ResolveEndpoints resolves a slice of EndpointSpecs to their attached Node
// vertices in g. Resolution order:
//
//  1. If Spec.ID is set and matches a Node vertex directly → use it.
//  2. If Spec.ID is set and matches an Endpoint vertex → follow its
//     AttachmentEdge to the connected Node.
//  3. If Spec.Address is set → find Endpoint vertices whose Addresses slice
//     contains a matching IP, then follow AttachmentEdge.
//
// All errors are collected so the caller can report them in aggregate.
func ResolveEndpoints(g *graph.Graph, specs []apitypes.EndpointSpec) ([]ResolvedEndpoint, []error) {
	var errs []error
	result := make([]ResolvedEndpoint, 0, len(specs))

	for _, spec := range specs {
		re, err := resolveOne(g, spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("endpoint %q: %w", endpointLabel(spec), err))
			continue
		}
		result = append(result, re)
	}
	return result, errs
}

func resolveOne(g *graph.Graph, spec apitypes.EndpointSpec) (ResolvedEndpoint, error) {
	if spec.ID != "" {
		v := g.GetVertex(spec.ID)
		if v == nil {
			return ResolvedEndpoint{}, fmt.Errorf("vertex %q not found in topology", spec.ID)
		}
		switch v.GetType() {
		case graph.VTNode:
			// When the spec ID is a Node directly, EndpointID == NodeID.
			return ResolvedEndpoint{Spec: spec, EndpointID: spec.ID, NodeID: spec.ID}, nil
		case graph.VTEndpoint:
			nodeID, err := attachedNode(g, spec.ID)
			if err != nil {
				return ResolvedEndpoint{}, err
			}
			return ResolvedEndpoint{Spec: spec, EndpointID: spec.ID, NodeID: nodeID}, nil
		case graph.VTPrefix:
			// Resolve the prefix to its advertising border node so that prefix
			// vertices can be used as path endpoints directly (e.g. from the UI).
			// The SPF runs to/from the border node; EndpointID retains the prefix
			// vertex ID so path results label flows by prefix.
			p, ok := v.(*graph.Prefix)
			if !ok {
				return ResolvedEndpoint{}, fmt.Errorf("vertex %q is not a *graph.Prefix", spec.ID)
			}
			res, err := ResolvePrefix(g, p.Prefix)
			if err != nil {
				return ResolvedEndpoint{}, fmt.Errorf("prefix %q: %w", spec.ID, err)
			}
			return ResolvedEndpoint{Spec: spec, EndpointID: spec.ID, NodeID: res.NodeID}, nil
		default:
			return ResolvedEndpoint{}, fmt.Errorf("vertex %q has type %q; expected node, endpoint, or prefix", spec.ID, v.GetType())
		}
	}

	if spec.Address != "" {
		ep, err := findEndpointByAddress(g, spec.Address)
		if err != nil {
			return ResolvedEndpoint{}, err
		}
		nodeID, err := attachedNode(g, ep.GetID())
		if err != nil {
			return ResolvedEndpoint{}, err
		}
		return ResolvedEndpoint{Spec: spec, EndpointID: ep.GetID(), NodeID: nodeID}, nil
	}

	return ResolvedEndpoint{}, fmt.Errorf("endpoint must have either id or address set")
}

// attachedNode follows outgoing AttachmentEdges from an Endpoint vertex and
// returns the ID of the first Node found. Most endpoints have exactly one
// attachment; for multi-homed endpoints the first edge is used (the caller
// can specify spec.ID pointing directly to a specific interface or node if
// more control is needed).
func attachedNode(g *graph.Graph, endpointID string) (string, error) {
	for _, e := range g.OutEdges(endpointID) {
		if e.GetType() != graph.ETAttachment {
			continue
		}
		dstID := e.GetDstID()
		v := g.GetVertex(dstID)
		if v == nil {
			continue
		}
		if v.GetType() == graph.VTNode {
			return dstID, nil
		}
	}
	return "", fmt.Errorf("no attachment edge from endpoint %q to a node vertex", endpointID)
}

// findEndpointByAddress scans all Endpoint vertices for one whose Addresses
// slice contains addr (as an exact IP match, ignoring prefix length).
func findEndpointByAddress(g *graph.Graph, addr string) (graph.Vertex, error) {
	needle, err := parseIP(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}

	for _, v := range g.VerticesByType(graph.VTEndpoint) {
		ep, ok := v.(*graph.Endpoint)
		if !ok {
			continue
		}
		for _, a := range ep.Addresses {
			candidate, err := parseIP(a)
			if err != nil {
				continue
			}
			if candidate == needle {
				return v, nil
			}
		}
	}
	return nil, fmt.Errorf("no endpoint with address %q found in topology", addr)
}

// parseIP parses an address string that may be a bare IP ("192.0.2.1") or a
// CIDR prefix ("192.0.2.1/32") and returns the host address.
func parseIP(s string) (netip.Addr, error) {
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		pfx, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Addr{}, err
		}
		return pfx.Addr(), nil
	}
	return netip.ParseAddr(s)
}

func endpointLabel(spec apitypes.EndpointSpec) string {
	if spec.ID != "" {
		return spec.ID
	}
	return spec.Address
}
