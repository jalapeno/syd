# Syd — SR Network Controller

## Project overview

Syd is an SRv6 SDN control plane for AI fabric path pinning and general
traffic engineering. An AI job scheduler (or any caller) POSTs a list of
endpoint pairs; syd returns SRv6 segment lists that pin traffic to
specific paths, bypassing ECMP.

Module path: `github.com/jalapeno/syd`
Local clone:  `~/src/newproject/`

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  Topology sources                                                │
│  ┌─────────────────┐   ┌──────────────────────────────────────┐ │
│  │  POST /topology  │   │  GoBMP → NATS JetStream (BMP/BGP-LS)│ │
│  │  (JSON push)     │   │  bmpcollector package               │ │
│  └────────┬─────────┘   └──────────────┬───────────────────────┘ │
│           │                            │                         │
│           ▼                            ▼                         │
│                    graph.Store                                   │
│              (named *graph.Graph instances)                      │
│                            │                                     │
│            ┌───────────────┼───────────────┐                    │
│            ▼               ▼               ▼                    │
│      pathengine      allocation        southbound               │
│      (Dijkstra,      (state machine:   (gNMI push or            │
│       seg lists,      FREE/EXCL/        no-op host mode)        │
│       uSID pack)      SHARED/DRAIN)                             │
│            │               │                                     │
│            └───────────────┘                                     │
│                     │                                            │
│              HTTP API  :8080                                     │
└──────────────────────────────────────────────────────────────────┘
```

## Package map

| Package | Purpose |
|---------|---------|
| `cmd/syd` | Binary entry point; flag parsing; wires everything together |
| `internal/graph` | Typed property graph: Node, Interface, Endpoint, VRF, LinkEdge, etc. `graph.Store` holds multiple named graphs |
| `internal/srv6` | SID types, `TryPackUSID` (uSID container packing), segment list types |
| `internal/topology` | JSON topology document parser/builder for the push API |
| `internal/pathengine` | Dijkstra SPF, endpoint resolution, segment list builder, all-pairs computation |
| `internal/allocation` | Path state machine (FREE/EXCLUSIVE/SHARED/DRAINING), workload lifecycle, lease timers |
| `internal/bmpcollector` | GoBMP NATS JetStream subscriber; translates BGP-LS messages into graph mutations |
| `internal/api` | HTTP handlers; routes wired in `server.go` |
| `internal/southbound` | Driver interface; `noop` (host mode) and `gnmi` (ToR push) implementations |
| `pkg/apitypes` | Public HTTP request/response types; importable by external callers |

## HTTP API

All endpoints are served on `:8080` (configurable via `--addr`).

### Topology

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/topology` | Push a JSON topology document; creates or incrementally updates |
| `POST` | `/topology/compose` | Merge source topologies into a composite graph (snapshot) |
| `GET` | `/topology` | List topology IDs |
| `GET` | `/topology/{id}` | Get topology stats |
| `GET` | `/topology/{id}/nodes` | List nodes |
| `DELETE` | `/topology/{id}` | Delete topology |
| `POST` | `/topology/{id}/policies` | Set/merge name→algo_id policy mappings |
| `GET` | `/topology/{id}/policies` | List current policy mappings |
| `DELETE` | `/topology/{id}/policies` | Clear all policy mappings |

### Paths / workloads

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/paths/request` | Request SRv6 paths for a workload |
| `GET` | `/paths/{workload_id}` | Get workload status |
| `GET` | `/paths/{workload_id}/flows` | Get encoded SRv6 segment lists (host programming) |
| `GET` | `/paths/{workload_id}/events` | SSE stream of workload state changes |
| `POST` | `/paths/{workload_id}/complete` | Signal workload done; start drain timer |
| `POST` | `/paths/{workload_id}/heartbeat` | Extend lease |
| `GET` | `/paths/state` | Dump full allocation table |

### Path request body (`POST /paths/request`)

```json
{
  "topology_id":   "underlay-v6",
  "workload_id":   "my-job-123",
  "endpoints": [
    {"id": "0000.0000.0001"},
    {"id": "0000.0000.0028"}
  ],
  "pairing_mode":  "all_directed",
  "disjointness":  "link",
  "constraints": {
    "algo_id":          128,
    "min_bandwidth_bps": 0,
    "max_latency_us":    0
  },
  "tenant_id":     "",
  "lease_duration_seconds": 300
}
```

`pairing_mode`: `"all_directed"` (default, N×(N-1) flows) or `"bidir_paired"`
(N×(N-1)/2 physical pairs, reverse derived automatically; best for AI all-reduce).

`disjointness`: `"none"` | `"link"` | `"node"` | `"srlg"`

`algo_id`: Flex-Algo ID. When non-zero:
  - SPF only traverses edges where **both endpoints have a locator for that algo**
  - Segment list uses algo-specific SIDs (uN/uA with matching AlgoID)

### Flows response (`GET /paths/{workload_id}/flows`)

```json
{
  "flows": [{
    "src_node_id":   "0000.0000.0001",
    "dst_node_id":   "0000.0000.0028",
    "segment_list":  ["fc00:0:1:e002:4:e004:16:0", "fc00:0:28::"],
    "encap_flavor":  "H.Encaps.Red",
    "outer_da":      "fc00:0:1:e002:4:e004:16:0",
    "srh_raw":       "<base64 Type-4 SRH, present only when len(segment_list)>1>"
  }]
}
```

## BMP / NATS topology ingestion

```
containerlab XRd routers
    └─► gobmp-nats (port 5001, NodePort 30512)
           └─► NATS JetStream stream "goBMP"
                  └─► bmpcollector → graph.Store
```

Handled NATS subjects:
- `gobmp.parsed.ls_node` → `graph.Node` (primary graph + mirrored to v4 companion)
- `gobmp.parsed.ls_link` → `graph.Interface` + `graph.LinkEdge` + uA SIDs
  - MTID=2 (MT-IPv6/SRv6) → primary graph (e.g. `"underlay-v6"`)
  - MTID=0/absent (base/IPv4) → companion graph (e.g. `"underlay-v4"`)
- `gobmp.parsed.ls_srv6_sid` → locators / uN SIDs merged onto Node (primary only)
- `gobmp.parsed.peer` → `graph.BGPSessionEdge` (primary graph)

Address-family graph split: IS-IS advertises IPv4 (MTID=0) and IPv6/SRv6 (MTID=2)
links as separate BGP-LS TLVs. syd routes them to separate graphs so the SRv6 SPF
never accidentally traverses an IPv4-only link (which has no uA SIDs). Path requests
always target the primary graph; the `-v4` companion is topology-visible only.

IOS-XR uSID behavior codes (non-standard, added to `behaviorFromCode`):
- `0x0030` = uN (micro-node, `BehaviorEnd`, `FunctionLen=0`)
- `0x0039` = uA (micro-adjacency, `BehaviorEndX`, `FunctionLen=16`)

On startup the collector **deletes** its durable consumers to force a
`DeliverAll` replay into the empty in-memory store.

## uSID container packing

`srv6.TryPackUSID([]SIDItem)` compresses a segment list into uSID containers:

- Slot width = `LocatorNodeLen + max(FunctionLen)` across all items
- F3216 all-uN (funcLen=0): 16-bit slots, capacity=6 per container
- F3216 mixed/all-uA (funcLen=16): 32-bit slots, capacity=3 per container
- Falls back to raw SIDs if structures are missing or blocks differ

## Key data model notes

- `graph.Node.SRv6Locators []srv6.Locator` — one entry per algo; `AlgoID=0`
  is the base SPF locator; `AlgoID=128/129/130` are Flex-Algo locators.
- `graph.Interface.SRv6uASIDs []srv6.UASID` — End.X SIDs per egress interface,
  one per algo. Structure sub-TLV is extracted from `EndXSIDTLV.SubTLVs`.
- `graph.Path.VertexIDs` / `.EdgeIDs` — used by `allocation.InvalidateElement`
  to determine which workloads to drain when topology elements are withdrawn.

## Vertex / edge keying scheme

Keys are scoped per `graph.Graph` instance (identified by `topoID`). Within a
graph, the rules are:

| Vertex type | Key format | Notes |
|-------------|-----------|-------|
| Node (IGP) | `<IGPRouterID>` | plain when `DomainID==0` (single domain) |
| Node (IGP, multi-domain) | `<DomainID>:<IGPRouterID>` | e.g. `65536:0000.0000.0006` |
| Node (external BGP peer) | `peer:<RemoteBGPID>_<RemoteIP>` | Jalapeno convention |
| Node (nexthop stub) | `nh:<ip>` | internal to prefix graphs |
| Interface | `iface:<nodeID>/<linkIP>` or `iface:<nodeID>/<linkNum>` | |
| Prefix (default VRF) | `pfx:<ip>/<len>` | e.g. `pfx:10.0.0.0/8` |
| Prefix (VRF-scoped) | `pfx:<vrfID>:<ip>/<len>` | reserved for L3VPN |
| LinkEdge | `link:<srcNodeID>:<dstNodeID>:<localLinkIP>` | |
| BGPSessionEdge | `bgpsess:<LocalBGPID>:<RemoteIP>` | |
| BGPReachabilityEdge | `bgpreach:<peerVertexID>:<pfxVertexID>` | |
| OwnershipEdge | `own:<ifaceID>-><nodeID>` or `pfxown:<pfxID>:<nhID>` | |

**Cross-graph stitching (composite graph):** BGP peer sessions use `LocalBGPID`
(a BGP router ID, e.g. `10.0.0.6`) as `SrcID`, while IGP nodes are keyed by
IS-IS system ID (e.g. `0000.0000.0006`). The stitching key is `graph.Node.RouterID`
which stores the BGP router ID on every IGP node vertex. When composing graphs,
scan `underlay-v6` nodes to build a `RouterID → nodeID` map and rewrite BGP
session edge `SrcID` values before insertion.

**XRd testbed:** all nodes have `DomainID=0` — node IDs are plain IS-IS system
IDs and match existing curl examples and API references unchanged.

## Composite graph (roadmap — next)

The goal is a `POST /topology/compose` endpoint that merges source graphs into a
unified named graph (e.g. `"ipv6-graph"`), enabling end-to-end shortest-path
queries from GPU endpoints through the IGP fabric to external BGP prefixes.

**Compose steps:**
1. Copy all vertices and `ETIGPAdjacency`/`ETPhysical` edges from `underlay-v6`
2. Build `RouterID → nodeID` map from the copied nodes
3. From `underlay-peers`: copy eBGP peer vertices; rewrite session `SrcID` via
   the map (drop if local end unresolvable)
4. From `underlay-prefixes-v4/v6`: copy prefix vertices and `ETBGPReachability`
   edges

**Path computation on composite graph:**
- Extend `POST /paths/request` with a `dst_prefix` field; internally resolves
  the target prefix to an egress BGP node, computes an SRv6 path to that node,
  and returns both the segment list (for underlay steering) and the BGP nexthop
  (for the final hop to the external destination).
- The path engine SPF needs a `"reachability"` mode that traverses
  `ETIGPAdjacency` for transit hops and `ETBGPSession`/`ETBGPReachability`
  for the final BGP-to-prefix hop.

## Allocation state machine

```
FREE ──alloc──► EXCLUSIVE ──complete/expire──► DRAINING ──timer──► FREE/COMPLETE
     ──alloc──► SHARED    ──complete/expire──► DRAINING
                                    ▲
                       topology_change (element withdrawn)
```

Workloads are drained (not immediately released) so in-flight packets clear
before capacity is reused.

## Build & test

```bash
go build ./...
go test ./...
```

Pre-existing failures in `internal/bmpcollector` (peer handler and BGP session
stub vertex tests) — tracked as a roadmap item, unrelated to recent changes.

Kubernetes deployment:
```bash
docker build -t syd:latest .
docker save syd:latest | sudo k3s ctr images import -
kubectl apply -k deploy/k8s/
kubectl -n syd rollout status deployment/syd
```

NodePort: `http://<node-ip>:30080`

## Scale analysis — 32-spine 64-leaf 8192-GPU cluster

### What runs where

Dijkstra runs on the **fabric topology only** (spine + leaf nodes = 96 vertices for
32-spine 64-leaf), not on the full graph including GPU endpoint vertices. GPU endpoint
vertices exist in the graph but have no transit uA SIDs — the path engine resolves them
to their attached leaf and computes leaf→leaf paths. So graph traversal cost stays low
even at 8192 GPUs.

### Today's bottleneck: N² pair enumeration

`ComputeAllPairs` iterates every directed endpoint pair and runs Dijkstra for each.
For a job with K GPUs:
- `all_directed`: K×(K-1) Dijkstra runs
- `bidir_paired`: K×(K-1)/2 Dijkstra runs

At K=256 GPUs/job: 65,280 Dijkstra runs × ~1 ms each ≈ 65 s (too slow at scale).

With K=64 GPUs/job (realistic AI job size today): ~4,000 runs, feasible.

### Path to 8192-GPU scale: leaf-pair pre-computation + ECMP-group output

**Observation**: in a fixed Clos topology the set of distinct leaf→leaf paths is small
and stable. A 64-leaf fabric has 64×63 = 4,032 directed leaf pairs (2,016 unordered).
Pre-compute and cache all leaf-pair segment lists at topology load time.

**ECMP-group output** (roadmap): instead of returning one segment list per GPU-pair,
return one group per leaf-pair. A job with 8192 GPUs spread across 64 leaves emits a
64×63 = 4,032-entry response (vs. 8192×8191 ≈ 67M entries today). The host-side agent
picks the right segment list based on its own leaf attachment.

**Result**: per-request work drops from O(K² × Dijkstra) to O(K² × hash lookup), and
response payload shrinks from O(GPU²) to O(leaf²) regardless of GPU count per leaf.

### Short-term ceiling

Without leaf-pair caching, the practical limit is ~256 GPUs/job at <5 s response time.
For the current demo/testbed use case (32 GPUs, 8 GPUs/job) there is no issue.

---

## Current status (as of 2026-04-20)

Done:
- Full BMP pipeline (17-node XRd testbed)
- Dijkstra SPF with disjointness, BW, latency, admin-group, Flex-Algo constraints
- uSID container packing: 32-bit full-uA (default), 16-bit `ua` mode, 16-bit `un` mode
- Flex-Algo SID selection and SPF edge pruning (`algo_id` in constraints)
- Incremental topology push (only drains workloads on removed elements)
- All-pairs path computation, bidir-paired mode, lease/drain timers
- Metadata-to-algo policy mapping (`POST /topology/{id}/policies`, name→algo_id)
- Address-family graph split: `underlay-v6` (SRv6/IPv6) + `underlay-v4` companion
- BMP peer message integration (BGP session topology layer, `underlay-peers` graph)
- BMP unicast prefix integration (IPv4/IPv6 prefix→node mapping, `underlay-prefixes-v4/v6`)
- External BGP peer vertices (`NSExternalBGP`) with `BGPReachabilityEdge` to prefix vertices;
  iBGP sessions skipped; eBGP peers keyed as `peer:<RemoteBGPID>_<RemoteIP>` (Jalapeno convention)
- Multi-domain node keying: `nodeID` incorporates `DomainID` when non-zero;
  VRF-scoped prefix keys reserved for L3VPN ingestion
- `graph.Compose()` + `POST /topology/compose` — merges IGP + peer + prefix source graphs
  into a unified snapshot with BGP session stitching (RouterID join); both `ipv4-graph`
  and `ipv6-graph` variants supported by choosing the appropriate prefix source
- Executive demo UI (topology graph, workload list, path/SID display, path-request form)
- All bmpcollector tests passing
- `scripts/test-local.sh` — local integration test suite (no NATS/BMP required)
- `test-data/clos-fabric.json` — 4-spine 8-leaf Clos, 32 GPU endpoints (4/leaf)

Roadmap:
- **End-to-end path with `dst_prefix`** — extend `POST /paths/request` with a
  `dst_prefix` field; resolves target prefix → egress BGP node → SRv6 path to that
  node; returns segment list + BGP nexthop. Requires the composite graph to exist.
  See "Composite graph" section above for full design.
- **L3VPN / EVPN handler support** — VRF/VPN topology ingestion via BMP;
  VRF-scoped prefix keys already in place (`prefixVertexID` with vrfID)
- **gNMI ToR southbound** — stub exists; needs `openconfig/gnmi` dependency wired up
  and real `DialFunc` implementation for SONiC switch programming
- **OpenAPI self-documentation** — `GET /openapi.json` so external agents can
  discover endpoints and request/response schemas without reading source
- **uDT multi-tenant paths** — `TenantID` field and `tenantUDTSIDItem` are wired;
  needs end-to-end testing with real VRF vertices pushed via the topology API

## UI agent work needed — composite graph rendering

The executive demo UI renders composite graphs (`ipv4-graph`, `ipv6-graph`) automatically
in the topology list and stats panel. Basic node/edge visualization also works. The
following improvements require UI agent changes:

### Server-side change (already done — `internal/api/ui.go`)

`GET /topology/{id}/graph` now returns `"subtype"` on node objects. External BGP peer
nodes have `subtype: "external_bgp"`; fabric/IGP nodes have no subtype. The `type`
field on edges was already present: `"igp_adjacency"`, `"bgp_session"`, `"bgp_reachability"`.

### Client-side changes needed (`ui/src/components/TopologyCanvas.tsx`)

1. **Node coloring by subtype** — the current color scheme is tier-based (degree analysis).
   Add subtype awareness:
   - `subtype === "external_bgp"` → distinct color (e.g. amber/orange), visually separate
     from fabric spine/leaf nodes
   - `type === "prefix"` already gets tier-2 handling in `getNodeTier()` — no change needed

2. **Edge styling by type** — all edge types currently render identically. Differentiate:
   - `type === "igp_adjacency"` → solid gray (current default, keep as-is)
   - `type === "bgp_session"` → dashed blue (inter-domain peering)
   - `type === "bgp_reachability"` → dotted green (prefix reachability)

3. **Layout for composite graphs** — the Clos spine/leaf/endpoint tier layout is
   inappropriate for composite graphs that include external peer and prefix nodes.
   Auto-detect when `type === "prefix"` or `subtype === "external_bgp"` nodes are
   present and default to force-directed (`"auto"`) layout for those topologies.
   Alternatively, expose a layout toggle in the UI.
