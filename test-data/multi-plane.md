# Multi-plane fabric test data

Test data for a 4-plane 8-spine×16-leaf SONiC fabric with green (hybrid) and yellow
(host-based) SRv6 multi-tenancy. Mirrors the containerlab topology in `../test-topology/`.

## Files

| File | Day | Purpose |
|---|---|---|
| `multiplane-p{0-3}-fabric.json` | Day-0 | Per-plane fabric: spines, leaves, interfaces, igp_adjacency edges, SRv6 uA SIDs |
| `multiplane-p{0-3}-compute.json` | Day-1 | Per-plane compute overlay: NIC endpoints + leaf access interfaces + attachment edges |
| `multiplane-compose.json` | Day-0 | Compose request: merges `fabric-p{0-3}` → `cluster` graph |
| `multiplane-tenant-green.json` | Day-N | Green/hybrid tenant: uDT6 `Vrf-green` VRF vertices on egress leaves, VRFMembership from green NICs |
| `multiplane-tenant-yellow.json` | Day-N | Yellow/host-based tenant: uDT6 `End.DT6` VRF vertices on destination NICs, VRFMembership from yellow NICs |

Regenerate with:

```bash
python3 test-data/gen-multiplane.py                                   # full scale (4×8×16×16)
python3 test-data/gen-multiplane.py --planes 2 --spines 4 --leaves 4 --hosts 4  # reduced
```

## Topology at a glance

```
 cluster graph (composed)
 ├── fabric-p0  (fc00:0000::/32)   8 spines × 16 leaves
 ├── fabric-p1  (fc00:0001::/32)   8 spines × 16 leaves
 ├── fabric-p2  (fc00:0002::/32)   8 spines × 16 leaves
 └── fabric-p3  (fc00:0003::/32)   8 spines × 16 leaves

Each plane:
  Spine locator  fc00:000<P>:1<S>::/48    (S = 0–7)
  Leaf locator   fc00:000<P>:2<L>::/48    (L = 0–f)
  Leaf→spine uA  fc00:000<P>:f00<S>::/48  ("f" = going up)
  Spine→leaf uA  fc00:000<P>:e00<L>::/48  ("e" = going down)
  Leaf→NIC uA    fc00:000<P>:e009::/48    (access port toward yellow host)
  Green uDT6     fc00:000<P>:d000::/48    (per leaf, decap into Vrf-green)
  Yellow uDT6    fc00:000<P>:d001::/48    (per yellow NIC, End.DT6 table 0)

Hosts (16 green + 16 yellow, each with one NIC per plane):
  Green NIC      green-host<NN>-p<P>-nic  attached to p<P>-leaf<NN>
  Yellow NIC     yellow-host<NN>-p<P>-nic attached to p<P>-leaf<NN>
```

## Deployment sequence

Set `NODE_IP` to your cluster node before running:

```bash
NODE_IP=<node-ip>
SYD=http://${NODE_IP}:30080
```

### Day-0 — fabric topology (one POST per plane)

```bash
for P in 0 1 2 3; do
  echo "--- fabric-p${P} ---"
  curl -s -X POST ${SYD}/topology \
    -H 'Content-Type: application/json' \
    -d @test-data/multiplane-p${P}-fabric.json | python3 -m json.tool
done
```

### Day-1 — compute overlay (one POST per plane, merges onto fabric-p{P})

```bash
for P in 0 1 2 3; do
  echo "--- compute-p${P} ---"
  curl -s -X POST ${SYD}/topology \
    -H 'Content-Type: application/json' \
    -d @test-data/multiplane-p${P}-compute.json | python3 -m json.tool
done
```

### Day-0 compose — build cluster graph

```bash
curl -s -X POST ${SYD}/topology/compose \
  -H 'Content-Type: application/json' \
  -d @test-data/multiplane-compose.json | python3 -m json.tool
```

This creates the `cluster` topology by merging all four `fabric-p{P}` graphs.
Run this after all four fabric + compute pushes are complete.

### Day-N — tenant overlays (merge onto cluster)

```bash
# Green (hybrid) — VRF on egress leaf
curl -s -X POST ${SYD}/topology \
  -H 'Content-Type: application/json' \
  -d @test-data/multiplane-tenant-green.json | python3 -m json.tool

# Yellow (host-based) — VRF on destination NIC
curl -s -X POST ${SYD}/topology \
  -H 'Content-Type: application/json' \
  -d @test-data/multiplane-tenant-yellow.json | python3 -m json.tool
```

### All steps as a single sequence

```bash
NODE_IP=<node-ip>
SYD=http://${NODE_IP}:30080

# Day-0: fabric
for P in 0 1 2 3; do
  curl -s -X POST ${SYD}/topology -H 'Content-Type: application/json' \
    -d @test-data/multiplane-p${P}-fabric.json | python3 -m json.tool
done

# Day-1: compute overlay
for P in 0 1 2 3; do
  curl -s -X POST ${SYD}/topology -H 'Content-Type: application/json' \
    -d @test-data/multiplane-p${P}-compute.json | python3 -m json.tool
done

# Day-0 compose: build cluster graph
curl -s -X POST ${SYD}/topology/compose -H 'Content-Type: application/json' \
  -d @test-data/multiplane-compose.json | python3 -m json.tool

# Day-N: tenant overlays
curl -s -X POST ${SYD}/topology -H 'Content-Type: application/json' \
  -d @test-data/multiplane-tenant-green.json | python3 -m json.tool
curl -s -X POST ${SYD}/topology -H 'Content-Type: application/json' \
  -d @test-data/multiplane-tenant-yellow.json | python3 -m json.tool
```

## Verify

```bash
# List topologies
curl -s ${SYD}/topology | python3 -m json.tool

# Check cluster graph node count
curl -s ${SYD}/topology/cluster | python3 -m json.tool

# Request a green path (hybrid: VRF on leaf)
curl -s -X POST ${SYD}/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "cluster",
    "workload_id": "green-test-01",
    "endpoints": [
      {"id": "green-host00-p0-nic"},
      {"id": "green-host15-p0-nic"}
    ],
    "pairing_mode": "bidir_paired"
  }' | python3 -m json.tool

# Request a yellow path (host-based: VRF on NIC)
curl -s -X POST ${SYD}/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "cluster",
    "workload_id": "yellow-test-01",
    "endpoints": [
      {"id": "yellow-host00-p0-nic"},
      {"id": "yellow-host15-p0-nic"}
    ],
    "pairing_mode": "bidir_paired"
  }' | python3 -m json.tool

# Retrieve segment lists
curl -s ${SYD}/paths/green-test-01/flows | python3 -m json.tool
curl -s ${SYD}/paths/yellow-test-01/flows | python3 -m json.tool
```

## Expected segment list shape

A green path from `green-host00-p0-nic` to `green-host15-p0-nic` (plane 0) resolves
via the attached leaves and produces a uSID container encoding the leaf→spine→leaf
crossing plus the uDT6 green SID at the egress leaf:

```
fc00:0000:20:f00<S>:2f:d000::
          └┬┘ └──┬──┘ └┬┘ └┬┘
           │     │     │   └─ d000   green uDT6 — decap into Vrf-green
           │     │     └───── 2f     leaf15 locator (0x2f = leaf 15)
           │     └─────────── f00<S> leaf00 uA toward chosen spine S
           └───────────────── 20     leaf00 locator (0x20 = leaf 0)
```

Yellow paths end with `d001::` (yellow uDT6) targeting the destination NIC rather
than the egress leaf. The leaf is pure transit; no Vrf-yellow state exists on it.
