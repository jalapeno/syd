1. On the k8s node — clone and deploy NATS first

```
git clone git@github.com:jalapeno/syd.git
cd syd
```
```
kubectl apply -f deploy/k8s/nats.yaml
```

Wait for it to be ready
```
kubectl -n jalapeno rollout status deployment/nats
```
```
nats-server: /etc/nats/nats-server.conf:15:3: "$G" is a Reserved Account
```

Quick sanity check — JetStream should show up
```
kubectl -n jalapeno port-forward svc/nats 8222:8222 &
curl -s http://localhost:8222/jsz | python3 -m json.tool | grep -E "config|memory"
kill %1
```

2. Redeploy GoBMP with NATS config

```
kubectl apply -f deploy/k8s/gobmp-collector.yaml
```
```
kubectl -n jalapeno rollout status deployment/gobmp
kubectl -n jalapeno logs -f deployment/gobmp
```

In the logs you should see GoBMP connecting to NATS and publishing on gobmp.parsed.* subjects once your BMP sources are pointed at it.

3. Build and deploy syd

You'll need to build the image on the node (or on your Mac and load it):

```
# On the k8s node, from the repo root:
docker build -t syd:latest .

# For k3s:
docker save syd:latest | sudo k3s ctr images import -

# For Kubeadm:
docker save syd:latest -o syd.tar
sudo ctr -n=k8s.io images import syd.tar

# For kind:
kind load docker-image syd:latest
```

Then:


Update the NATS URL in the configmap to point at your jalapeno namespace NATS
It should be: nats://nats.jalapeno:4222
(the default in configmap.yaml is already set to that)
```
kubectl apply -k deploy/k8s/
kubectl -n syd rollout status deployment/syd
kubectl -n syd logs -f deployment/syd
```
You should see:
```
level=INFO msg="bmp collector configured" nats_url=nats://nats.jalapeno:4222
level=INFO msg="syd starting" addr=:8080 bmp=true encap_mode=host
```
Once the containerlab BMP streams are flowing, the topology will start populating and you can hit curl http://<node-ip>:30080/topology from your laptop.

### BMP

Test - get underlay-v6 nodes
```
curl -s http://localhost:30080/topology/underlay-v6/nodes | python3 -m json.tool | grep name
```
```
cisco@jalapeno-host:~/syd$ curl -s http://localhost:30080/topology/underlay-v6/nodes | python3 -m json.tool | grep name
            "name": "xrd01"
            "name": "xrd15"
            "name": "xrd25"
            "name": "xrd08"
            "name": "xrd29"
            "name": "xrd18"
            "name": "xrd07"
            "name": "xrd09"
            "name": "xrd06"
            "name": "xrd31"
            "name": "xrd02"
            "name": "xrd32"
            "name": "xrd28"
            "name": "xrd17"
            "name": "xrd03"
            "name": "xrd16"
            "name": "xrd04"
cisco@jalapeno-host:~/syd$ 
```

Test - path request
```
curl -s -X POST http://localhost:30080/paths/request   -H 'Content-Type: application/json'   -d '{
    "topology_id": "underlay-v6",
    "workload_id": "test-xrd01-xrd28",
    "endpoints": [
      {"id": "0000.0000.0001"},
      {"id": "0000.0000.0028"}
    ]
  }' | python3 -m json.tool
```

```
{
    "workload_id": "test-xrd01-xrd28",
    "topology_id": "underlay-v6",
    "paths": [
        {
            "src_id": "",
            "dst_id": "",
            "segment_list": {
                "encap": "SRv6",
                "flavor": "H.Encaps.Red",
                "sids": [
                    "fc00:0:1:e002::",
                    "fc00:0:4:e004::",
                    "fc00:0:16::",
                    "fc00:0:28::"
                ]
            },
            "metric": {
                "igp_metric": 12,
                "delay_us": 65,
                "hop_count": 3
            },
            "path_id": "test-xrd01-xrd28-1776571627833027192-0"
        },
        {
            "src_id": "",
            "dst_id": "",
            "segment_list": {
                "encap": "SRv6",
                "flavor": "H.Encaps.Red",
                "sids": [
                    "fc00:0:28::",
                    "fc00:0:16::",
                    "fc00:0:4::",
                    "fc00:0:1::"
                ]
            },
            "metric": {
                "igp_metric": 3,
                "delay_us": 70,
                "hop_count": 3
            },
            "path_id": "test-xrd01-xrd28-1776571627833027192-1"
        }
    ],
    "allocation_state": {
        "paths_from_free": 0,
        "paths_from_shared": 0,
        "total_free_after": 0
    }
}
```
```
curl -s http://localhost:30080/paths/test-xrd01-xrd28/flows | python3 -m json.tool
```
```
cisco@jalapeno-host:~/syd$ curl -s http://localhost:30080/paths/test-xrd01-xrd28/flows | python3 -m json.tool
{
    "workload_id": "test-xrd01-xrd28",
    "topology_id": "underlay-v6",
    "flows": [
        {
            "src_node_id": "",
            "dst_node_id": "",
            "path_id": "test-xrd01-xrd28-1776571627833027192-0",
            "segment_list": [
                "fc00:0:1:e002::",
                "fc00:0:4:e004::",
                "fc00:0:16::",
                "fc00:0:28::"
            ],
            "encap_flavor": "H.Encaps.Red",
            "outer_da": "fc00:0:1:e002::",
            "srh_raw": "OwgEAwMAAAD8AAAAACgAAAAAAAAAAAAA/AAAAAAWAAAAAAAAAAAAAPwAAAAABOAEAAAAAAAAAAD8AAAAAAHgAgAAAAAAAAAA"
        },
        {
            "src_node_id": "",
            "dst_node_id": "",
            "path_id": "test-xrd01-xrd28-1776571627833027192-1",
            "segment_list": [
                "fc00:0:28::",
                "fc00:0:16::",
                "fc00:0:4::",
                "fc00:0:1::"
            ],
            "encap_flavor": "H.Encaps.Red",
            "outer_da": "fc00:0:28::",
            "srh_raw": "OwgEAwMAAAD8AAAAAAEAAAAAAAAAAAAA/AAAAAAEAAAAAAAAAAAAAPwAAAAAFgAAAAAAAAAAAAD8AAAAACgAAAAAAAAAAAAA"
        }
    ]
}
```

Test - get paths
```
curl -s -X POST http://localhost:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "underlay-v6",
    "workload_id": "test-alltoall-4",
    "endpoints": [
      {"id": "0000.0000.0001"},
      {"id": "0000.0000.0002"},
      {"id": "0000.0000.0028"},
      {"id": "0000.0000.0029"}
    ],
    "disjointness": "link",
    "pairing_mode": "all_directed"
  }' | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(f'workload: {d[\"workload_id\"]}')
print(f'paths: {len(d[\"paths\"])}')
for p in d['paths']:
    sids = p['segment_list']['sids']
    print(f'  {p[\"src_id\"]} -> {p[\"dst_id\"]}  hops={p[\"metric\"][\"hop_count\"]}  sids={len(sids)}')
"
```


### Debugging gobmp-nats

```
curl -s http://localhost:30080/topology/underlay-v6/nodes | python3 -m json.tool | grep name
```

nats cli:
```
 kubectl -n jalapeno port-forward svc/nats 4222:4222 &
```

```
curl -s 'http://localhost:8222/jsz/streams/goBMP/subjects' | python3 -m json.tool
```
```
nats -s nats://localhost:4222 consumer next goBMP   --subject gobmp.parsed.ls_node   --all --count 500 --raw 2>/dev/null   | python3 -c "
import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    try:
        m = json.loads(line)
        print(m)
    except: pass"


---

## Session: uSID container packing + bug fixes

### Bug fixes applied

**1. IOS-XR uSID behavior codes (translator.go)**

IOS-XR advertises non-IANA behavior codes for uN/uA SIDs:
- `0x0030` = uN (micro-node, no function bits)
- `0x0039` = uA (micro-adjacency, 16-bit function)

These were added to `behaviorFromCode()` alongside the IANA codes `0x0041`/`0x0042`.

**2. Allocation table missing on BMP startup (main.go)**

BMP-driven topologies bypass `POST /topology`, so `allocation.NewTable` was never
called. Fixed by pre-creating the table in `main.go` when `--bmp` is enabled:

```go
tables.Put(*bmpTopo, allocation.NewTable(*bmpTopo))
```

**3. Empty topology after pod restart (collector.go)**

Durable JetStream consumers remember their ack position. After a restart the
in-memory store is empty, but the consumer resumes from where it left off and
misses all prior topology messages. Fixed by deleting consumers on startup to
force a full `DeliverAll` replay:

```go
c.deleteConsumers()  // called in Start() before subscribeAll()
```

**4. Empty src_id / dst_id in path responses (resolve.go)**

When an endpoint spec `id` resolved directly to a Node vertex, `EndpointID` was
left as an empty string. Fixed by setting `EndpointID: spec.ID` in that branch.

---

### uSID container packing

SRv6 uSID paths can be compressed into container addresses. The packing rules:

- All SIDs must share the same `LocatorBlockLen` and `LocatorNodeLen`.
- Slot width = `nodeLen + maxFuncLen` across all items.
  - All-uN path (funcLen=0): 16-bit slots, capacity = (128-blockLen) / 16
  - Mixed uA+uN or all-uA path (funcLen=16): 32-bit slots, capacity = (128-blockLen) / 32
- For F3216 (blockLen=32): all-uN capacity=6, mixed/uA capacity=3.
- Each container is packed from bytes `[blockBytes:]` onward.
- If SIDs overflow one container, additional containers are produced (for SRH).

**uA SIDStructure from SubTLVs**: `ls_link` End.X SIDs carry a SID Structure
sub-TLV (`gobmpsrv6.SIDStructure`) inside `EndXSIDTLV.SubTLVs []SubTLV`. The
translator now type-asserts this and populates the `Structure` field, which
`TryPackUSID` needs to determine the correct slot width.

---

### NATS diagnostics

```bash
# Port-forward NATS
kubectl -n jalapeno port-forward svc/nats 4222:4222 &

# Stream overview: message counts per subject
curl -s 'http://localhost:8222/jsz/streams/goBMP/subjects' | python3 -m json.tool

# Tail live ls_node messages (shows protocol_id: 1=IS-IS L1, 2=IS-IS L2)
nats -s nats://localhost:4222 sub gobmp.parsed.ls_node

# Pull all ls_node messages and print protocol_id + router ID
nats -s nats://localhost:4222 consumer next goBMP \
  --subject gobmp.parsed.ls_node --all --count 500 --raw 2>/dev/null \
  | python3 -c "
import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    try:
        m = json.loads(line)
        print(m.get('protocol_id'), m.get('igp_router_id'), m.get('name'))
    except: pass"

# Check ls_link for End.X SIDs (uA)
nats -s nats://localhost:4222 consumer next goBMP \
  --subject gobmp.parsed.ls_link --all --count 500 --raw 2>/dev/null \
  | python3 -c "
import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    try:
        m = json.loads(line)
        sids = m.get('srv6_endx_sid') or []
        if sids:
            print(m.get('igp_router_id'), '->', m.get('remote_igp_router_id'), [s.get('srv6_sid') for s in sids])
    except: pass"

# Check ls_srv6_sid for node locator SIDs (uN)
nats -s nats://localhost:4222 consumer next goBMP \
  --subject gobmp.parsed.ls_srv6_sid --all --count 500 --raw 2>/dev/null \
  | python3 -c "
import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    try:
        m = json.loads(line)
        print(m.get('igp_router_id'), m.get('srv6_sid'), 'behavior:', hex(m.get('srv6_endpoint_behavior', {}).get('endpoint_behavior', 0)))
    except: pass"

# Delete stale consumers manually (syd does this on startup, but useful for debugging)
nats -s nats://localhost:4222 consumer rm goBMP syd-gobmp-parsed-ls_node
nats -s nats://localhost:4222 consumer rm goBMP syd-gobmp-parsed-ls_link
nats -s nats://localhost:4222 consumer rm goBMP syd-gobmp-parsed-ls_srv6_sid
nats -s nats://localhost:4222 consumer rm goBMP syd-gobmp-parsed-peer
```

---

## Deploy checklist

```bash
# Pull latest on the k8s node
cd ~/src/syd
git pull

# Rebuild and reload image
docker build -t syd:latest .
docker save syd:latest | sudo k3s ctr images import -

# Rolling restart to pick up new image
kubectl -n syd rollout restart deployment/syd
kubectl -n syd rollout status deployment/syd

# Watch startup — you should now see TWO topology IDs populate
kubectl -n syd logs -f deployment/syd | grep -E "topology|starting|bmp"
```

### Path request with uSID packing — expected output

```bash
curl -s -X POST http://localhost:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "underlay-v6",
    "workload_id": "test-xrd01-xrd28",
    "endpoints": [
      {"id": "0000.0000.0001"},
      {"id": "0000.0000.0028"}
    ]
  }' | python3 -m json.tool
```

Expected (F3216, mixed uA+uN forward, all-uN return):

```
Forward (xrd01→xrd28): mixed uA+uN, 32-bit slots, capacity=3 → 2 containers
  Container 1: fc00:0:1:e002:4:e004:16:0   (xrd01→uA, xrd04→uA, xrd16→uN)
  Container 2: fc00:0:28::                  (xrd28 uN, standalone)

Return (xrd28→xrd01): all-uN, 16-bit slots, capacity=6 → 1 container
  Container 1: fc00:0:28:16:4:1::           (xrd28, xrd16, xrd04, xrd01)
```

Actual output confirmed:
```json
{
    "paths": [
        {
            "src_id": "0000.0000.0001",
            "dst_id": "0000.0000.0028",
            "segment_list": {
                "sids": ["fc00:0:1:e002:4:e004:16:0", "fc00:0:28::"]
            }
        },
        {
            "src_id": "0000.0000.0028",
            "dst_id": "0000.0000.0001",
            "segment_list": {
                "sids": ["fc00:0:28:16:4:1::"]
            }
        }
    ]
}
```

---

## Session: AF graph split, policy mapping, rename to syd (2026-04-19)

---

### 1. Verify AF graph split

After BMP converges, two topology graphs should exist: `underlay-v6` (IPv6/SRv6,
MTID=2 links only) and `underlay-v4` (IPv4/base-topology, MTID=0 links).

```bash
NODE=<your-node-ip>

# Both graphs should appear in the list
curl -s http://$NODE:30080/topology | python3 -m json.tool
# Expected: {"topology_ids": ["underlay-v6", "underlay-v4"]}

# underlay-v6 should have nodes but ONLY IPv6/SRv6 links
curl -s http://$NODE:30080/topology/underlay-v6 | python3 -m json.tool

# underlay-v4 should have the same nodes but only IPv4 links
curl -s http://$NODE:30080/topology/underlay-v4 | python3 -m json.tool
```

Sanity check via NATS — confirm MTID values in raw ls_link messages:

```bash
kubectl -n jalapeno port-forward svc/nats 4222:4222 &

# Show MTID for each link (should see mt_id_tlv.mt_id = 0 for IPv4, 2 for IPv6)
# Note: nats consumer next --raw interleaves message headers with JSON bodies;
# filter to lines starting with '{' to get only the JSON payloads.
nats -s nats://localhost:4222 consumer next goBMP \
  --subject gobmp.parsed.ls_link --count 500 --raw 2>/dev/null \
  | python3 -c "
import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line.startswith('{'):
        continue
    try:
        m = json.loads(line)
        mtid = (m.get('mt_id_tlv') or {}).get('mt_id', 'absent')
        src = m.get('igp_router_id','?')
        dst = m.get('remote_igp_router_id','?')
        lip = m.get('local_link_ip','')
        print(f'MTID={mtid:>6}  {src} -> {dst}  {lip}')
    except: pass" | sort

kill %1
```

Expected: IPv4 link IPs (10.x.x.x / 172.x.x.x) have MTID=0, IPv6 link IPs
(fc00::/32 range) have MTID=2.

---

### 2. Test path request — expect uA SIDs on BOTH directions now

The key fix: the reverse path SPF no longer picks IPv4 links (no uA SIDs).
Both forward and reverse should now show packed uA+uN containers.

```bash
# xrd01 ↔ xrd16 (use xrd16 — xrd28 has no Flex-Algo links)
curl -s -X POST http://$NODE:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "underlay-v6",
    "workload_id": "test-af-split",
    "endpoints": [
      {"id": "0000.0000.0001"},
      {"id": "0000.0000.0016"}
    ]
  }' | python3 -c "
import sys, json
d = json.load(sys.stdin)
for p in d['paths']:
    print(f'{p[\"src_id\"]} -> {p[\"dst_id\"]}')
    print(f'  sids: {p[\"segment_list\"][\"sids\"]}')
    print(f'  hops: {p[\"metric\"][\"hop_count\"]}')
"
```

**What to look for:**
- Before fix: reverse path had only uN SIDs (e.g. `fc00:0:16:1::` — no `eXXX` function)
- After fix: both directions should have uA SIDs with non-zero function parts
  (e.g. `fc00:0:1:e002:...` forward, `fc00:0:16:eXXX:...` reverse)

Also check flows for the packed SRH:

```bash
curl -s http://$NODE:30080/paths/test-af-split/flows | python3 -c "
import sys, json
d = json.load(sys.stdin)
for f in d['flows']:
    srh = '+ SRH' if f.get('srh_raw') else ''
    print(f'{f[\"src_node_id\"]} -> {f[\"dst_node_id\"]}')
    print(f'  segment_list: {f[\"segment_list\"]}')
    print(f'  outer_da: {f[\"outer_da\"]} {srh}')
"
```

Cleanup:
```bash
curl -s -X POST http://$NODE:30080/paths/test-af-split/complete \
  -H 'Content-Type: application/json' -d '{"immediate":true}'
```

---

### 3. Flex-Algo path request (regression check)

```bash
curl -s -X POST http://$NODE:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "underlay-v6",
    "workload_id": "test-algo128",
    "endpoints": [
      {"id": "0000.0000.0001"},
      {"id": "0000.0000.0016"}
    ],
    "constraints": {"algo_id": 128}
  }' | python3 -c "
import sys, json
d = json.load(sys.stdin)
for p in d['paths']:
    print(f'{p[\"src_id\"]} -> {p[\"dst_id\"]}  sids={p[\"segment_list\"][\"sids\"]}')
"

curl -s -X POST http://$NODE:30080/paths/test-algo128/complete \
  -H 'Content-Type: application/json' -d '{"immediate":true}'
```

---

### 4. Policy mapping API

Register a human-readable name for algo 128 and use it in a path request.

```bash
# Register policies on the underlay-v6 topology
curl -s -X POST http://$NODE:30080/topology/underlay-v6/policies \
  -H 'Content-Type: application/json' \
  -d '{
    "policies": [
      {"name": "latency-optimized", "algo_id": 128},
      {"name": "carbon-optimized",  "algo_id": 130}
    ]
  }' | python3 -m json.tool

# Verify
curl -s http://$NODE:30080/topology/underlay-v6/policies | python3 -m json.tool

# Request a path using the policy name instead of raw algo_id
curl -s -X POST http://$NODE:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "underlay-v6",
    "workload_id": "test-policy",
    "endpoints": [
      {"id": "0000.0000.0001"},
      {"id": "0000.0000.0016"}
    ],
    "policy": "latency-optimized"
  }' | python3 -c "
import sys, json
d = json.load(sys.stdin)
for p in d['paths']:
    print(f'{p[\"src_id\"]} -> {p[\"dst_id\"]}  sids={p[\"segment_list\"][\"sids\"]}')
"
# Should produce identical results to constraints.algo_id=128

# Test unknown policy → expect 422
curl -s -o /dev/null -w '%{http_code}\n' \
  -X POST http://$NODE:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "underlay-v6",
    "workload_id": "test-bad-policy",
    "endpoints": [{"id":"0000.0000.0001"},{"id":"0000.0000.0016"}],
    "policy": "nonexistent"
  }'
# Expected: 422

curl -s -X POST http://$NODE:30080/paths/test-policy/complete \
  -H 'Content-Type: application/json' -d '{"immediate":true}'

# Remove a policy (algo_id=0 means delete)
curl -s -X POST http://$NODE:30080/topology/underlay-v6/policies \
  -H 'Content-Type: application/json' \
  -d '{"policies": [{"name": "carbon-optimized", "algo_id": 0}]}' \
  | python3 -m json.tool
# carbon-optimized should be gone; latency-optimized remains
```

---

### 5. Incremental topology push (regression check)

Push a minimal topology, allocate a workload, push an updated topology with one
node removed, and verify only the affected workload drains.

**Note:** This test uses a bare A-B-C topology with no SRv6 locators or uA SIDs
— it is specifically testing drain/invalidation behavior. Path responses will
show empty `segment_list.sids` arrays, which is expected. See the
"Push topology with SRv6 data" section below if you want to test actual SID
encoding via the push API.

```bash
# Push v1 — three nodes A, B, C in a line A--B--C with endpoints
curl -s -X POST http://$NODE:30080/topology \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "test-incr",
    "nodes":      [{"id":"A"},{"id":"B"},{"id":"C"}],
    "interfaces": [{"id":"A-eth0","owner_node_id":"A"},{"id":"B-eth0","owner_node_id":"B"},
                   {"id":"B-eth1","owner_node_id":"B"},{"id":"C-eth0","owner_node_id":"C"}],
    "edges": [
      {"id":"AB","type":"igp_adjacency","src_id":"A","dst_id":"B","igp_metric":1},
      {"id":"BA","type":"igp_adjacency","src_id":"B","dst_id":"A","igp_metric":1},
      {"id":"BC","type":"igp_adjacency","src_id":"B","dst_id":"C","igp_metric":1},
      {"id":"CB","type":"igp_adjacency","src_id":"C","dst_id":"B","igp_metric":1}
    ]
  }' | python3 -m json.tool

# Allocate a workload that traverses C
curl -s -X POST http://$NODE:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id":"test-incr","workload_id":"wl-through-c",
    "endpoints":[{"id":"A"},{"id":"C"}]
  }' | python3 -m json.tool

# Push v2 — node C removed
curl -s -X POST http://$NODE:30080/topology \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "test-incr",
    "nodes":      [{"id":"A"},{"id":"B"}],
    "interfaces": [{"id":"A-eth0","owner_node_id":"A"},{"id":"B-eth0","owner_node_id":"B"}],
    "edges": [
      {"id":"AB","type":"igp_adjacency","src_id":"A","dst_id":"B","igp_metric":1},
      {"id":"BA","type":"igp_adjacency","src_id":"B","dst_id":"A","igp_metric":1}
    ]
  }' | python3 -m json.tool

# wl-through-c should now be DRAINING with reason=topology_change
curl -s http://$NODE:30080/paths/wl-through-c | python3 -m json.tool

# Cleanup
curl -s -X DELETE http://$NODE:30080/topology/test-incr
```

---

### 6. Push topology with SRv6 data

To test the full SRv6 SID encoding path via the push API (not BMP), you need
to include `srv6_locators` on nodes and `srv6_ua_sids` on interfaces.
This example mirrors a minimal two-node topology with uN + uA SIDs:

```bash
curl -s -X POST http://$NODE:30080/topology \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "test-srv6",
    "nodes": [
      {
        "id": "0000.0000.0001",
        "name": "r1",
        "srv6_locators": [
          {"prefix": "fc00:0:1::/48", "block_len": 32, "node_len": 16,
           "func_len": 0, "arg_len": 0, "algo_id": 0}
        ]
      },
      {
        "id": "0000.0000.0002",
        "name": "r2",
        "srv6_locators": [
          {"prefix": "fc00:0:2::/48", "block_len": 32, "node_len": 16,
           "func_len": 0, "arg_len": 0, "algo_id": 0}
        ]
      }
    ],
    "interfaces": [
      {
        "id": "r1-eth0", "owner_node_id": "0000.0000.0001",
        "srv6_ua_sids": [{
          "sid": "fc00:0:1:e001::",
          "behavior": "End.X",
          "algo_id": 0,
          "structure": {"locator_block_len":32,"locator_node_len":16,"function_len":16,"argument_len":0}
        }]
      },
      {
        "id": "r2-eth0", "owner_node_id": "0000.0000.0002",
        "srv6_ua_sids": [{
          "sid": "fc00:0:2:e001::",
          "behavior": "End.X",
          "algo_id": 0,
          "structure": {"locator_block_len":32,"locator_node_len":16,"function_len":16,"argument_len":0}
        }]
      }
    ],
    "edges": [
      {"id":"r1r2","type":"igp_adjacency","src_id":"0000.0000.0001",
       "dst_id":"0000.0000.0002","igp_metric":1,
       "local_iface_id":"r1-eth0","remote_iface_id":"r2-eth0"},
      {"id":"r2r1","type":"igp_adjacency","src_id":"0000.0000.0002",
       "dst_id":"0000.0000.0001","igp_metric":1,
       "local_iface_id":"r2-eth0","remote_iface_id":"r1-eth0"}
    ]
  }' | python3 -m json.tool

# Request paths — should produce uA SIDs on both directions
curl -s -X POST http://$NODE:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "test-srv6",
    "workload_id": "wl-srv6-push",
    "endpoints": [
      {"id": "0000.0000.0001"},
      {"id": "0000.0000.0002"}
    ]
  }' | python3 -c "
import sys, json
d = json.load(sys.stdin)
for p in d['paths']:
    print(f'{p[\"src_id\"]} -> {p[\"dst_id\"]}  sids={p[\"segment_list\"][\"sids\"]}')
"
# Expected:
# 0000.0000.0001 -> 0000.0000.0002  sids=['fc00:0:1:e001:2::']   (uA packed)
# 0000.0000.0002 -> 0000.0000.0001  sids=['fc00:0:2:e001:1::']

# Cleanup
curl -s -X POST http://$NODE:30080/paths/wl-srv6-push/complete \
  -H 'Content-Type: application/json' -d '{"immediate":true}'
curl -s -X DELETE http://$NODE:30080/topology/test-srv6
```

---

## Session: BMP peer and unicast prefix graphs (2026-04-19)

### Deploy checklist

```bash
cd ~/src/syd && git pull
docker build -t syd:latest .
docker save syd:latest | sudo k3s ctr images import -
kubectl -n syd rollout restart deployment/syd
kubectl -n syd rollout status deployment/syd
```

After BMP converges, five topology graphs should exist.

---

### 1. Verify all five topology graphs

```bash
NODE=<your-node-ip>

curl -s http://$NODE:30080/topology | python3 -m json.tool
```

Expected (order may vary):
```json
{"topology_ids": ["underlay-v6", "underlay-v4", "underlay-peers", "underlay-prefixes-v4", "underlay-prefixes-v6"]}
```

Check each graph's stats — vertex/edge counts confirm data is flowing:

```bash
for topo in underlay-v6 underlay-v4 underlay-peers underlay-prefixes-v4 underlay-prefixes-v6; do
  echo "=== $topo ==="
  curl -s http://$NODE:30080/topology/$topo | python3 -c "
import sys, json
d = json.load(sys.stdin)
s = d['stats']
print(f'  nodes={s[\"nodes\"]}  edges={s[\"total_edges\"]}  prefixes={s.get(\"prefixes\",0)}')
"
done
```

Rough expected counts (17-node XRd testbed):
- `underlay-v6`: ~17 nodes, ~100+ edges (IPv6/SRv6 links + interfaces + ownership)
- `underlay-v4`: ~17 nodes, ~100+ edges (IPv4 links)
- `underlay-peers`: nodes = unique BGP endpoint IPs, edges = BGP sessions
- `underlay-prefixes-v4`: prefix vertices + nexthop nodes
- `underlay-prefixes-v6`: prefix vertices + nexthop nodes

---

### 2. Inspect peer topology

```bash
# List node IDs (BGP endpoint IPs) in the peers graph
curl -s http://$NODE:30080/topology/underlay-peers/nodes | python3 -m json.tool | head -30

# Spot-check: show BGP session edges (raw graph endpoint)
curl -s "http://$NODE:30080/topology/underlay-peers/graph" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(f'Peer nodes: {len(d[\"nodes\"])}')
for lnk in d.get('links', []):
    print(f'  BGP session: {lnk[\"source\"]} -> {lnk[\"target\"]}')
" | head -30
```

You should see BGP peer IP pairs (e.g. 10.0.0.x ↔ 10.x.x.x loopbacks).

---

### 3. Inspect prefix topologies

```bash
# IPv4 prefix stats
curl -s http://$NODE:30080/topology/underlay-prefixes-v4 | python3 -m json.tool

# IPv6 prefix stats
curl -s http://$NODE:30080/topology/underlay-prefixes-v6 | python3 -m json.tool

# Sample IPv4 prefix vertices and their nexthop nodes
curl -s "http://$NODE:30080/topology/underlay-prefixes-v4/graph" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(f'Prefix nodes: {len(d[\"nodes\"])}  Links (prefix→nexthop): {len(d.get(\"links\",[]))}')
# Show first 10 prefix vertices
pfx_nodes = [n for n in d['nodes'] if n['id'].startswith('pfx:')]
for n in pfx_nodes[:10]:
    print(f'  {n[\"id\"]}')
" 

# Same for IPv6
curl -s "http://$NODE:30080/topology/underlay-prefixes-v6/graph" | python3 -c "
import sys, json
d = json.load(sys.stdin)
pfx_nodes = [n for n in d['nodes'] if n['id'].startswith('pfx:')]
print(f'IPv6 prefixes: {len(pfx_nodes)}')
for n in pfx_nodes[:5]:
    print(f'  {n[\"id\"]}')
"
```

---

### 4. Check vertex_ids and edge_ids in path responses

Path responses now include the ordered list of node vertex IDs and link edge
IDs traversed — used by the UI for path highlighting.

```bash
curl -s -X POST http://$NODE:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "underlay-v6",
    "workload_id": "test-vertexids",
    "endpoints": [
      {"id": "0000.0000.0001"},
      {"id": "0000.0000.0016"}
    ]
  }' | python3 -c "
import sys, json
d = json.load(sys.stdin)
for p in d['paths']:
    print(f'{p[\"src_id\"]} -> {p[\"dst_id\"]}')
    print(f'  sids:       {p[\"segment_list\"][\"sids\"]}')
    print(f'  vertex_ids: {p.get(\"vertex_ids\",[])}')
    print(f'  edge_ids:   {p.get(\"edge_ids\",[])}')
"

curl -s -X POST http://$NODE:30080/paths/test-vertexids/complete \
  -H 'Content-Type: application/json' -d '{"immediate":true}'
```

Expected: `vertex_ids` = IS-IS system IDs in hop order; `edge_ids` = `link:*` IDs.


---

## Session: Clos fabric push topology (UI development)

The file `test-data/clos-fabric.json` contains a synthetic 4-spine 8-leaf
Clos fabric with 64 GPU endpoints. Push it as a separate topology alongside
the BMP underlay-v6 — they are completely isolated by `topology_id`.

Topology structure:
- 4 spine nodes (`spine-1..4`), labels: `tier=spine`
- 8 leaf nodes (`leaf-01..08`), labels: `tier=leaf pod=1..4` (2 leaves/pod)
- 64 GPU endpoints (`gpu-001..064`), labels: `tier=gpu leaf=leaf-XX slot=1..8`
- 64 directed spine-leaf `igp_adjacency` edge pairs (400 Gbps, 1 µs, metric 1)
- 64 directed `attachment` edges (GPU → leaf)

### Push the fabric topology

```bash
NODE=<your-node-ip>

# From the repo root (where test-data/ lives):
curl -s -X POST http://$NODE:30080/topology \
  -H 'Content-Type: application/json' \
  -d @test-data/clos-fabric.json | python3 -m json.tool
# Expected: {"topology_id": "clos-fabric", "nodes": 12, "endpoints": 64, ...}

# Verify it appears alongside underlay-v6 in the list
curl -s http://$NODE:30080/topology | python3 -m json.tool

# Check graph stats
curl -s http://$NODE:30080/topology/clos-fabric | python3 -m json.tool

# Fetch graph JSON for UI visualization
curl -s http://$NODE:30080/topology/clos-fabric/graph | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(f'nodes={len(d[\"nodes\"])}  links={len(d[\"links\"])}')
# Show a sample of node IDs grouped by tier
from collections import Counter
tiers = Counter(n['name'].split('-')[0] for n in d['nodes'])
print('tiers:', dict(tiers))
"
```

### Request a path across the fabric

The fabric nodes have SRv6 node SIDs (spines fc00:0:1000::-fc00:0:1003::,
leafs fc00:0:2000::-fc00:0:2007::) so segment_list will contain packed uN SIDs.

```bash
# gpu-001 (on leaf-01) → gpu-017 (on leaf-05) — crosses spine layer
curl -s -X POST http://$NODE:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "clos-fabric",
    "workload_id": "clos-test",
    "endpoints": [
      {"id": "gpu-001"},
      {"id": "gpu-017"}
    ],
    "pairing_mode": "all_directed"
  }' | python3 -c "
import sys, json
d = json.load(sys.stdin)
for p in d['paths'][:4]:
    print(f'{p[\"src_id\"]} -> {p[\"dst_id\"]}')
    print(f'  hops: {p[\"metric\"][\"hop_count\"]}')
    print(f'  path: {\" -> \".join(p.get(\"vertex_ids\",[]))}')
    print(f'  sids: {p[\"segment_list\"][\"sids\"]}')
"

curl -s -X POST http://$NODE:30080/paths/clos-test/complete \
  -H 'Content-Type: application/json' -d '{"immediate":true}'
```

Expected: 2-hop paths (leaf-01 → spine-N → leaf-05), with ECMP across all 4 spines.
After trimming to 4 GPUs/leaf: gpu-001..004 on leaf-01, gpu-005..008 on leaf-02,
gpu-009..012 on leaf-03, gpu-013..016 on leaf-04, gpu-017..020 on leaf-05, etc.

### 8-GPU all-reduce workload (one GPU per leaf)

Representative AI all-reduce workload: one GPU per leaf, `bidir_paired` mode so
forward and reverse flows for each pair share the same physical links.

**Note on disjointness:** do NOT use `disjointness: link` here. A 4-spine 8-leaf
Clos has only 32 spine-leaf links; each 2-hop path consumes 2, so the hard ceiling
on strictly link-disjoint paths is 16. The greedy SPF will produce only ~22 paths
before the topology runs dry. For all-to-all AI workloads you want all 56 paths —
let ECMP across the spines provide the redundancy, not the disjointness exclusion.

```bash
curl -s -X POST http://$NODE:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "clos-fabric",
    "workload_id": "clos-allreduce-8",
    "endpoints": [
      {"id": "gpu-001"},
      {"id": "gpu-005"},
      {"id": "gpu-009"},
      {"id": "gpu-013"},
      {"id": "gpu-017"},
      {"id": "gpu-021"},
      {"id": "gpu-025"},
      {"id": "gpu-029"}
    ],
    "pairing_mode": "bidir_paired"
  }' | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(f'workload:  {d[\"workload_id\"]}')
print(f'paths:     {len(d[\"paths\"])}  (expect 56 = 8*(8-1)/2 pairs x2 directions)')
print(f'free used: {d[\"allocation_state\"][\"paths_from_free\"]}')
for p in d['paths'][:4]:
    print(f'  {p[\"src_id\"]} -> {p[\"dst_id\"]}  hops={p[\"metric\"][\"hop_count\"]}  sids={p[\"segment_list\"][\"sids\"]}')
print('  ...')
"

# Check flows (56 entries, one per directed flow)
curl -s http://$NODE:30080/paths/clos-allreduce-8/flows | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(f'flows: {len(d[\"flows\"])}')
for f in d['flows'][:2]:
    srh = '+ SRH' if f.get('srh_raw') else ''
    print(f'  {f[\"src_node_id\"]} -> {f[\"dst_node_id\"]}  outer_da={f[\"outer_da\"]} {srh}')
"

curl -s -X POST http://$NODE:30080/paths/clos-allreduce-8/complete \
  -H 'Content-Type: application/json' -d '{"immediate":true}'
```

### Cleanup

```bash
curl -s -X DELETE http://$NODE:30080/topology/clos-fabric
```

# Build ipv6 and ipv6 graphs

ipv6
```bash
curl -s -X POST http://localhost:30080/topology/compose \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "ipv6-graph",
    "sources": ["underlay-v6", "underlay-peers", "underlay-prefixes-v6"]
  }' | python3 -m json.tool
```

ipv4
```bash
curl -s -X POST http://localhost:30080/topology/compose \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "ipv4-graph",
    "sources": ["underlay-v6", "underlay-peers", "underlay-prefixes-v4"]
  }' | python3 -m json.tool
```

# Ingress and Egress
Egress (GPU → external prefix):

```bash
curl -s -X POST http://localhost:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "ipv6-graph",
    "workload_id": "gpu-egress-test",
    "endpoints": [{"id": "0000.0000.0001"}, {"id": "0000.0000.0002"}],
    "dst_prefix": "200.0.0.0/8"
  }' | jq '{paths: [.paths[] | {src: .src_id, dst: .dst_id, bgp_nexthop, prefix_id, segs: .segment_list}]}'
```

Ingress (external prefix → GPU/service):

```bash
curl -s -X POST http://localhost:30080/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "ipv6-graph",
    "workload_id": "ingress-test",
    "endpoints": [{"id": "0000.0000.0001"}],
    "src_prefix": "200.0.0.0/8"
  }' | jq .
```

Each PathResult carries bgp_nexthop (the BGP next-hop the border router uses after SRv6 decap) and prefix_id (the resolved prefix vertex). The segment list terminates at the SRv6 domain edge — the border router.

### Leaf-Pairs and ECMP Group
Step 1 — Push the small Clos topology
```bash
curl -s -X POST http://localhost:30080/topology \
  -H "Content-Type: application/json" \
  -d @/Users/brucemcdougall/src/syd/test-data/clos-fabric.json | jq .
```
This gives you clos-32gpu (4 spines, 8 leaves, 4 GPUs/leaf). The leaf_pair_flows grouping effect is easier to read at this scale.

Step 2 — Request paths for GPUs spanning exactly 2 leaves
Pick gpu-001..gpu-008 — those are the 4 GPUs on leaf-01 and the 4 GPUs on leaf-02:

```bash
curl -s -X POST http://localhost:30080/paths/request \
  -H "Content-Type: application/json" \
  -d '{
    "topology_id": "clos-32gpu",
    "workload_id": "validate-leaf-pairs",
    "endpoints": [
      {"id": "gpu-001"}, {"id": "gpu-002"}, {"id": "gpu-003"}, {"id": "gpu-004"},
      {"id": "gpu-005"}, {"id": "gpu-006"}, {"id": "gpu-007"}, {"id": "gpu-008"}
    ],
    "pairing_mode": "all_directed",
    "disjointness": "none"
  }' | jq '{paths_count: (.paths | length)}'
```
8 GPUs → 56 directed pairs total.

Step 3 — Fetch flows and inspect the grouping
```bash
curl -s http://localhost:30080/paths/validate-leaf-pairs/flows | jq '{
  flows_count:      (.flows | length),
  leaf_pair_count:  (.leaf_pair_flows | length),
  leaf_pairs:       [.leaf_pair_flows[] | {
    pair:       "\(.src_node_id) → \(.dst_node_id)",
    sid:        .segment_list,
    flow_count: .flow_count
  }]
}'
```
What to expect
Thing	Value	Why
flows_count	56	8×7 directed GPU-pairs
leaf_pair_count	4	(leaf-01→leaf-01), (leaf-01→leaf-02), (leaf-02→leaf-01), (leaf-02→leaf-02)
flow_count on cross-leaf entries	16	4 GPUs on src-leaf × 4 GPUs on dst-leaf
flow_count on same-leaf entries	12	4×3 same-leaf pairs (zero-hop paths)
SID list	identical across all 16 flows in a leaf-pair group	the whole point — same physical path
Step 4 — Verify SID consistency within a group
Pick a cross-leaf pair and verify all 16 GPU-pair flows in flows that share that leaf-pair really do have the same segment list:


FLOWS=$(curl -s http://localhost:8080/paths/validate-leaf-pairs/flows)

# Grab the leaf-01→leaf-02 segment list from the grouped view
LEAF_SID=$(echo "$FLOWS" | jq -r '
  .leaf_pair_flows[]
  | select(.src_node_id == "leaf-01" and .dst_node_id == "leaf-02")
  | .segment_list[0]')

echo "Leaf-pair SID: $LEAF_SID"

# Count how many individual GPU flows match it
echo "$FLOWS" | jq --arg sid "$LEAF_SID" '
  [.flows[] | select(.segment_list[0] == $sid)] | length'
That count should match the flow_count from the grouped entry.

Step 5 — Scale up to 256 GPUs

curl -s -X POST http://localhost:8080/topology \
  -H "Content-Type: application/json" \
  -d @/Users/brucemcdougall/src/syd/test-data/clos-256gpu.json | jq .

curl -s -X POST http://localhost:8080/paths/request \
  -H "Content-Type: application/json" \
  -d '{
    "topology_id": "clos-256gpu",
    "workload_id": "validate-256",
    "endpoints": [
      {"id": "gpu-001"}, {"id": "gpu-002"}, {"id": "gpu-003"}, {"id": "gpu-004"},
      {"id": "gpu-005"}, {"id": "gpu-006"}, {"id": "gpu-007"}, {"id": "gpu-008"}
    ],
    "pairing_mode": "all_directed",
    "disjointness": "none"
  }' | jq .

curl -s http://localhost:8080/paths/validate-256/flows | jq '{
  flows_count: (.flows | length),
  leaf_pair_count: (.leaf_pair_flows | length)
}'
With 256 GPUs total in the topology but only 8 in the job (2 leaves), the reduction numbers are the same as Step 3. To see the full leaf² vs GPU² contrast, request all 256 GPUs — flows_count will be 65,280, leaf_pair_count will be 4,032 (64×63 directed leaf-pairs).


### Polarfly

Post polarfly topology data
```
curl -s -X POST http://localhost:30080/topology   -H 'Content-Type: application/json'   -d @q7/q7-fabric.json | python3 -m json.tool
```