package api

import (
	"fmt"
	"net/http"

	"github.com/jalapeno/syd/internal/graph"
)

// --- Topology graph API for UI visualization ---

type uiGraphNode struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"`
	Subtype string `json:"subtype,omitempty"`
}

type uiGraphLink struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	Type      string `json:"type,omitempty"`
	Metric    uint32 `json:"metric,omitempty"`
	Bandwidth uint64 `json:"bandwidth,omitempty"`
	Delay     uint32 `json:"delay,omitempty"`
}

type uiTopologyGraph struct {
	Nodes []uiGraphNode `json:"nodes"`
	Links []uiGraphLink `json:"links"`
}

func (s *Server) handleTopologyGraph(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g := s.store.Get(id)
	if g == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("topology %q not found", id))
		return
	}

	// Collect all vertices except Interfaces (which are internal to link
	// modeling and clutter the visualization).
	allVerts := g.AllVertices()
	nodeIDs := make(map[string]struct{}, len(allVerts))
	nodes := make([]uiGraphNode, 0, len(allVerts))

	for _, v := range allVerts {
		vt := v.GetType()
		if vt == graph.VTInterface {
			continue // skip interfaces — they're internal to link modeling
		}
		n := uiGraphNode{
			ID:   v.GetID(),
			Type: string(vt),
		}
		// Resolve display name and subtype per vertex type.
		switch tv := v.(type) {
		case *graph.Node:
			n.Name = tv.Name
			n.Subtype = string(tv.Subtype)
		case *graph.Endpoint:
			n.Name = tv.Name
		case *graph.Prefix:
			n.Name = tv.Prefix // CIDR string as display name
		}
		nodes = append(nodes, n)
		nodeIDs[v.GetID()] = struct{}{}
	}

	// Collect all edges where both endpoints are in our visible node set.
	// Initialize as non-nil so the JSON field is always an array, never null.
	seen := make(map[string]struct{})
	links := make([]uiGraphLink, 0)
	for _, e := range g.AllEdges() {
		src, dst := e.GetSrcID(), e.GetDstID()
		if _, ok := nodeIDs[src]; !ok {
			continue
		}
		if _, ok := nodeIDs[dst]; !ok {
			continue
		}

		// Deduplicate bidirectional edges (keep one per pair)
		pairKey := src + "|" + dst
		reversePairKey := dst + "|" + src
		if _, ok := seen[pairKey]; ok {
			continue
		}
		if _, ok := seen[reversePairKey]; ok {
			continue
		}
		seen[pairKey] = struct{}{}

		link := uiGraphLink{
			ID:     e.GetID(),
			Source: src,
			Target: dst,
			Type:   string(e.GetType()),
		}

		switch le := e.(type) {
		case *graph.LinkEdge:
			link.Metric = le.IGPMetric
			link.Bandwidth = le.MaxBW
			link.Delay = le.UnidirDelay
		}

		links = append(links, link)
	}

	writeJSON(w, http.StatusOK, uiTopologyGraph{
		Nodes: nodes,
		Links: links,
	})
}
