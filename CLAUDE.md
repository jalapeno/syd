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
  "topology_id":   "underlay",
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
  - MTID=2 (MT-IPv6/SRv6) → primary graph (e.g. `"underlay"`)
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

## Current status (as of 2026-04-19)

Done:
- Full BMP pipeline (17-node XRd testbed)
- Dijkstra SPF with disjointness, BW, latency, admin-group, Flex-Algo constraints
- uSID container packing (32-bit mixed, 16-bit all-uN)
- Flex-Algo SID selection and SPF edge pruning (`algo_id` in constraints)
- Incremental topology push (only drains workloads on removed elements)
- All-pairs path computation, bidir-paired mode, lease/drain timers
- Metadata-to-algo policy mapping (`POST /topology/{id}/policies`, name→algo_id)
  - `PathRequest.policy` resolves a named policy to an algo_id before compute
  - Operators register mappings once (e.g. "carbon-optimized" → 130); job
    schedulers reference them by name without embedding numeric algo IDs

Roadmap:
- **Executive demo UI** — see `pkg/apitypes` for the full API contract; all
  endpoints are documented above. The UI should show: topology graph
  visualization, active workload list, per-workload path/SID display,
  path-request form.
- BMP peer message integration (BGP session topology layer)
- BMP unicast prefix integration (IPv4/IPv6 prefix→node mapping, "map the internet")
- Fix bmpcollector test failures (BGP session edge/stub vertex tests)
- L3VPN / EVPN handler support
