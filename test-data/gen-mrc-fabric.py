#!/usr/bin/env python3
"""
gen-mrc-fabric.py — generates syd topology JSON for the srv6-ai-fabric
4p-8x16 MRC/SRv6-spray topology (4 planes × 8 spines × 16 leaves).

Source of truth: srv6-ai-fabric/topologies/4p-8x16/
Reference design: OpenAI/Microsoft/NVIDIA MRC + SRv6-spray multi-plane AI fabric.

Addressing (from srv6-ai-fabric/AGENTS.md and generators/fabric.py):

  Cluster aggregate      fc00:0000::/30              (4 planes)
  Plane P block          fc00:000<P>::/32
  Spine S in plane P     fc00:000<P>:1<S>::/48        uN locator (hex, so spine0=10)
  Leaf L in plane P      fc00:000<P>:2<L>::/48        uN locator (hex, so leaf0=20, leaf15=2f)
  Leaf → spine S uA      fc00:000<P>:f00<S>::/48      f000–f007
  Spine → leaf L uA      fc00:000<P>:e00<L>::/48      e000–e00f
  Leaf → yellow NIC uA   fc00:000<P>:e009::/48        Ethernet36, NIC ordinal 9
  Green uDT6 on leaf     fc00:000<P>:d000::/48        Vrf-green decap at egress leaf
  Yellow uDT6 on NIC     fc00:000<P>:d001::/48        End.DT6 on dest NIC (host-based)

  Fabric P2P /127        2001:db8:fab:<S*NL+L>::/127  reused per plane (L2-isolated)
                          spine side ::0, leaf side ::1

  Green NIC (anycast)    2001:db8:bbbb:<NN>::2/64     same /64 on all 4 plane NICs (nodad)
  Green leaf gateway     2001:db8:bbbb:<NN>::1/64     same on all planes (anycast gw)
  Yellow NIC (anycast)   2001:db8:cccc:<NN>::2/64     same /64 on all 4 plane NICs (nodad)
  Yellow leaf gateway    2001:db8:cccc:<NN>::1/64     same on all planes (anycast gw)

  NIC ordinal rule: Ethernet(N) → uA e<N/4:03x> (not port number)
    Ethernet0  → e000   Ethernet32 → e008 (green access)
    Ethernet36 → e009 ← leaf→host uA for yellow

  SID format: F3216 (locator_block=32, locator_node=16, function=16, argument=0)

  uSID outer dst shape:
    green : fc00:000<P>:f00<S>:e00<L>:d000::   (leaf decap into Vrf-green)
    yellow: fc00:000<P>:f00<S>:e00<L>:e009:d001::  (NIC decap, extra leaf→host hop)

Outputs:
  mrc-p{P}-fabric.json     Day-0: per-plane fabric (spines + leaves + links + SRv6 SIDs)
  mrc-p{P}-compute.json    Day-1: per-plane compute overlay (NICs + attachments)
  mrc-compose.json         Day-0: POST /topology/compose → mrc-cluster
  mrc-tenant-green.json    Day-N: green/hybrid overlay (leaf uDT → Vrf-green)
  mrc-tenant-yellow.json   Day-N: yellow/host-based overlay (NIC uDT → End.DT6)

Usage:
  python3 test-data/gen-mrc-fabric.py                    # full 4×8×16 (matches lab)
  python3 test-data/gen-mrc-fabric.py --planes 2 --spines 4 --leaves 4 --hosts 4
  python3 test-data/gen-mrc-fabric.py --out /tmp/out/
"""

import argparse
import json
import sys
from pathlib import Path

# ---------------------------------------------------------------------------
# Defaults — match srv6-ai-fabric topologies/4p-8x16/topo.yaml exactly
# ---------------------------------------------------------------------------
DEFAULT_PLANES = 4
DEFAULT_SPINES = 8
DEFAULT_LEAVES = 16
DEFAULT_HOSTS  = 16   # hosts per tenant; equals NUM_LEAVES (one host per leaf)

IGP_METRIC = 10

SID_STRUCT = {
    "locator_block_len": 32,
    "locator_node_len":  16,
    "function_len":      16,
    "argument_len":       0,
}

# ---------------------------------------------------------------------------
# Address helpers — match srv6_fabric/topo.py and generators/fabric.py
# ---------------------------------------------------------------------------

def usid_block(P: int) -> str:
    return f"fc00:{P:04x}"

def spine_locator_prefix(P, S): return f"{usid_block(P)}:{0x10+S:x}::/48"
def spine_locator_sid(P, S):    return f"{usid_block(P)}:{0x10+S:x}::"
def leaf_locator_prefix(P, L):  return f"{usid_block(P)}:{0x20+L:x}::/48"
def leaf_locator_sid(P, L):     return f"{usid_block(P)}:{0x20+L:x}::"

# uA SIDs
def leaf_to_spine_ua_sid(P, S): return f"{usid_block(P)}:f{S:03x}::"   # f000–f007
def spine_to_leaf_ua_sid(P, L): return f"{usid_block(P)}:e{L:03x}::"   # e000–e00f
def leaf_to_nic_ua_sid(P):      return f"{usid_block(P)}:e009::"        # NIC ordinal 9

# Tenant SIDs
def green_udt_sid(P):           return f"{usid_block(P)}:d000::"        # leaf Vrf-green
def yellow_udt_sid(P):          return f"{usid_block(P)}:d001::"        # NIC End.DT6

# Fabric P2P /127 — index = S*NL+L, reused identically per plane (L2-isolated)
def fab_p2p_prefix(S, L, NL):   return f"2001:db8:fab:{S*NL+L:04x}::/127"
def fab_p2p_addr(S, L, NL, side):
    idx = S * NL + L
    suffix = "0" if side == "spine" else "1"
    return f"2001:db8:fab:{idx:04x}::{suffix}/127"

# Host addresses — ANYCAST (no plane component); same /64 on all plane NICs
# Green: 2001:db8:bbbb:<NN>::{1,2}/64  — leaf gw ::1, NIC ::2
# Yellow: 2001:db8:cccc:<NN>::{1,2}/64 — leaf gw ::1, NIC ::2
def green_gw_addr(H):           return f"2001:db8:bbbb:{H:02x}::1/64"
def green_nic_addr(H):          return f"2001:db8:bbbb:{H:02x}::2"
def yellow_gw_addr(H):          return f"2001:db8:cccc:{H:02x}::1/64"
def yellow_nic_addr(H):         return f"2001:db8:cccc:{H:02x}::2"

# ---------------------------------------------------------------------------
# Vertex / edge ID helpers
# ---------------------------------------------------------------------------

def spine_id(P, S):             return f"p{P}-spine{S:02d}"
def leaf_id(P, L):              return f"p{P}-leaf{L:02d}"
def green_nic_id(H, P):         return f"green-host{H:02d}-p{P}-nic"
def yellow_nic_id(H, P):        return f"yellow-host{H:02d}-p{P}-nic"

# Spine port toward leaf L: Ethernet{L*4}
def spine_iface_id(P, S, L):    return f"iface:{spine_id(P,S)}/Eth{L*4}"
# Leaf port toward spine S: Ethernet{S*4}
def leaf_iface_id(P, L, S):     return f"iface:{leaf_id(P,L)}/Eth{S*4}"
# Leaf green access: Ethernet32 (NIC ordinal 8)
def leaf_green_iface_id(P, L):  return f"iface:{leaf_id(P,L)}/Eth32-green"
# Leaf yellow access: Ethernet36 (NIC ordinal 9)
def leaf_yellow_iface_id(P, L): return f"iface:{leaf_id(P,L)}/Eth36-yellow"

def green_vrf_id(P, L):         return f"vrf:green:p{P}-leaf{L:02d}"
def yellow_vrf_id(H, P):        return f"vrf:yellow:yellow-host{H:02d}-p{P}"

# ---------------------------------------------------------------------------
# SID constructors
# ---------------------------------------------------------------------------

def node_sid(value):
    return {"sid": value, "behavior": "End", "structure": SID_STRUCT}

def ua_sid(value):
    return {"sid": value, "behavior": "End.X", "structure": SID_STRUCT}

def udt_sid(value):
    return {"sid": value, "behavior": "End.DT6", "structure": SID_STRUCT}

def locator(prefix, sid_value):
    return {"prefix": prefix, "algo_id": 0, "node_sid": node_sid(sid_value)}

# ---------------------------------------------------------------------------
# Day-0: per-plane fabric
# Spines + leaves, fabric P2P interfaces, igp_adjacency edges, SRv6 SIDs.
# ---------------------------------------------------------------------------

def gen_fabric(P: int, NS: int, NL: int) -> dict:
    nodes, interfaces, edges = [], [], []

    # Spines
    for S in range(NS):
        nodes.append({
            "id":            spine_id(P, S),
            "subtype":       "switch",
            "name":          spine_id(P, S),
            "srv6_locators": [locator(spine_locator_prefix(P, S),
                                      spine_locator_sid(P, S))],
        })
        # One port per leaf — spine uA toward each leaf
        for L in range(NL):
            interfaces.append({
                "id":            spine_iface_id(P, S, L),
                "owner_node_id": spine_id(P, S),
                "name":          f"Ethernet{L*4}",
                "addresses":     [fab_p2p_addr(S, L, NL, "spine")],
                "srv6_ua_sids":  [ua_sid(spine_to_leaf_ua_sid(P, L))],
            })

    # Leaves
    for L in range(NL):
        nodes.append({
            "id":            leaf_id(P, L),
            "subtype":       "switch",
            "name":          leaf_id(P, L),
            "srv6_locators": [locator(leaf_locator_prefix(P, L),
                                      leaf_locator_sid(P, L))],
        })
        # Fabric ports — leaf uA toward each spine
        for S in range(NS):
            interfaces.append({
                "id":            leaf_iface_id(P, L, S),
                "owner_node_id": leaf_id(P, L),
                "name":          f"Ethernet{S*4}",
                "addresses":     [fab_p2p_addr(S, L, NL, "leaf")],
                "srv6_ua_sids":  [ua_sid(leaf_to_spine_ua_sid(P, S))],
            })

    # igp_adjacency edges (bidirectional, one per spine×leaf pair)
    for S in range(NS):
        for L in range(NL):
            edges.append({
                "id":              f"adj:{spine_id(P,S)}:{leaf_id(P,L)}",
                "type":            "igp_adjacency",
                "src_id":          spine_id(P, S),
                "dst_id":          leaf_id(P, L),
                "directed":        False,
                "local_iface_id":  spine_iface_id(P, S, L),
                "remote_iface_id": leaf_iface_id(P, L, S),
                "igp_metric":      IGP_METRIC,
            })

    return {
        "topology_id": f"mrc-p{P}",
        "metadata":    [{"topology_type": "clos"}],
        "description": (
            f"MRC/SRv6-spray plane {P} fabric — "
            f"{NS} spines × {NL} leaves, block {usid_block(P)}::/32"
        ),
        "source":     "push",
        "nodes":      nodes,
        "interfaces": interfaces,
        "edges":      edges,
    }

# ---------------------------------------------------------------------------
# Day-1: per-plane compute overlay  (merge onto mrc-p{P})
# NIC endpoints + leaf access interfaces + attachment edges.
# Green: Ethernet32, no uA (leaf decaps via uDT6/Vrf-green).
# Yellow: Ethernet36, leaf carries e009 uA toward host NIC.
# Host addresses are anycast — identical /64 on every plane NIC (nodad).
# ---------------------------------------------------------------------------

def gen_compute(P: int, NL: int, NH: int) -> dict:
    endpoints, interfaces, edges = [], [], []

    for H in range(NH):
        L = H  # host H attached to leaf H in every plane

        # ---- green NIC (hybrid: leaf Ethernet32 → Vrf-green) ----
        g_nic   = green_nic_id(H, P)
        g_iface = leaf_green_iface_id(P, L)
        endpoints.append({
            "id":       g_nic,
            "subtype":  "gpu",
            "name":     f"green-host{H:02d}-plane{P}",
            "addresses": [green_nic_addr(H)],
            "metadata": {
                "host":   f"green-host{H:02d}",
                "plane":  str(P),
                "tenant": "green",
                "model":  "hybrid",
            },
        })
        interfaces.append({
            "id":            g_iface,
            "owner_node_id": leaf_id(P, L),
            "name":          "Ethernet32",
            "addresses":     [green_gw_addr(H)],
            # No uA SID: leaf uses uDT6/d000 to decap into Vrf-green;
            # normal VRF routing delivers the inner packet to the NIC.
        })
        edges.append({
            "id":              f"att:{g_nic}:{leaf_id(P,L)}",
            "type":            "attachment",
            "src_id":          g_nic,
            "dst_id":          leaf_id(P, L),
            "directed":        True,
            "access_iface_id": g_iface,
        })

        # ---- yellow NIC (host-based: leaf Ethernet36, no leaf VRF state) ----
        y_nic   = yellow_nic_id(H, P)
        y_iface = leaf_yellow_iface_id(P, L)
        endpoints.append({
            "id":       y_nic,
            "subtype":  "gpu",
            "name":     f"yellow-host{H:02d}-plane{P}",
            "addresses": [yellow_nic_addr(H)],
            "metadata": {
                "host":   f"yellow-host{H:02d}",
                "plane":  str(P),
                "tenant": "yellow",
                "model":  "host-based",
            },
        })
        interfaces.append({
            "id":            y_iface,
            "owner_node_id": leaf_id(P, L),
            "name":          "Ethernet36",
            "addresses":     [yellow_gw_addr(H)],
            # e009 uA: leaf→NIC penultimate hop for host-based yellow segment lists.
            # Invariant (AGENTS.md): Ethernet36 = NIC ordinal 9 = e009.
            # Path engine roadmap: insert this as penultimate hop for yellow endpoints.
            "srv6_ua_sids":  [ua_sid(leaf_to_nic_ua_sid(P))],
        })
        edges.append({
            "id":              f"att:{y_nic}:{leaf_id(P,L)}",
            "type":            "attachment",
            "src_id":          y_nic,
            "dst_id":          leaf_id(P, L),
            "directed":        True,
            "access_iface_id": y_iface,
        })

    return {
        "topology_id": f"mrc-p{P}",
        "metadata":    [{"topology_type": "clos"}],
        "description": (
            f"MRC plane {P} compute overlay — "
            f"{NH} green NICs (hybrid) + {NH} yellow NICs (host-based)"
        ),
        "source":     "push",
        "merge":      True,
        "endpoints":  endpoints,
        "interfaces": interfaces,
        "edges":      edges,
    }

# ---------------------------------------------------------------------------
# Day-0: compose request (POST /topology/compose)
# Merges mrc-p{0..NP-1} into mrc-cluster.
# ---------------------------------------------------------------------------

def gen_compose(NP: int) -> dict:
    return {
        "topology_id": "mrc-cluster",
        "metadata":    [{"topology_type": "clos"}],
        "sources":     [f"mrc-p{P}" for P in range(NP)],
        "description": (
            f"MRC {NP}-plane cluster — composed from "
            + ", ".join(f"mrc-p{P}" for P in range(NP))
        ),
    }

# ---------------------------------------------------------------------------
# Day-N: green/hybrid tenant overlay  (merge onto mrc-cluster)
# VRF vertex per leaf per plane; owned by the leaf (hybrid decap).
# Tenant boundary: egress leaf applies uDT6 d000 → Vrf-green.
# VRFMembership connects green NIC → leaf VRF.
# ---------------------------------------------------------------------------

def gen_tenant_green(NP: int, NL: int, NH: int) -> dict:
    vrfs, edges = [], []
    for P in range(NP):
        for L in range(NL):
            vrf_id = green_vrf_id(P, L)
            vrfs.append({
                "id":            vrf_id,
                "name":          "Vrf-green",
                "owner_node_id": leaf_id(P, L),
                "srv6_udt_sid":  udt_sid(green_udt_sid(P)),
            })
            H = L  # host H is attached to leaf H
            if H < NH:
                g_nic = green_nic_id(H, P)
                edges.append({
                    "id":       f"vrfmem:{g_nic}:{vrf_id}",
                    "type":     "vrf_membership",
                    "src_id":   g_nic,
                    "dst_id":   vrf_id,
                    "directed": True,
                })
    return {
        "topology_id": "mrc-cluster",
        "metadata":    [{"topology_type": "clos"}],
        "description": (
            "MRC green/hybrid tenant — "
            "uDT6 d000 Vrf-green on egress leaves; NIC encapsulates"
        ),
        "source": "push",
        "merge":  True,
        "vrfs":   vrfs,
        "edges":  edges,
    }

# ---------------------------------------------------------------------------
# Day-N: yellow/host-based tenant overlay  (merge onto mrc-cluster)
# VRF vertex per yellow NIC per plane; owned by the NIC (host-based decap).
# Tenant boundary: destination NIC applies End.DT6 d001.
# Leaf is pure transit — no Vrf-yellow on any leaf.
# VRFMembership connects yellow NIC → its own NIC-owned VRF.
# ---------------------------------------------------------------------------

def gen_tenant_yellow(NP: int, NH: int) -> dict:
    vrfs, edges = [], []
    for P in range(NP):
        for H in range(NH):
            y_nic  = yellow_nic_id(H, P)
            vrf_id = yellow_vrf_id(H, P)
            vrfs.append({
                "id":            vrf_id,
                "name":          "yellow-default",
                # Owner is the NIC endpoint — VRF lives on the host, not the leaf
                "owner_node_id": y_nic,
                "srv6_udt_sid":  udt_sid(yellow_udt_sid(P)),
            })
            edges.append({
                "id":       f"vrfmem:{y_nic}:{vrf_id}",
                "type":     "vrf_membership",
                "src_id":   y_nic,
                "dst_id":   vrf_id,
                "directed": True,
            })
    return {
        "topology_id": "mrc-cluster",
        "metadata":    [{"topology_type": "clos"}],
        "description": (
            "MRC yellow/host-based tenant — "
            "uDT6 d001 End.DT6 on destination NICs; leaf is pure transit"
        ),
        "source": "push",
        "merge":  True,
        "vrfs":   vrfs,
        "edges":  edges,
    }

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    p = argparse.ArgumentParser(
        description="Generate syd topology JSON for the srv6-ai-fabric 4p-8x16 MRC topology",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    p.add_argument("--planes", type=int, default=DEFAULT_PLANES,
                   help=f"Number of planes (default {DEFAULT_PLANES})")
    p.add_argument("--spines", type=int, default=DEFAULT_SPINES,
                   help=f"Spines per plane (default {DEFAULT_SPINES})")
    p.add_argument("--leaves", type=int, default=DEFAULT_LEAVES,
                   help=f"Leaves per plane (default {DEFAULT_LEAVES})")
    p.add_argument("--hosts",  type=int, default=DEFAULT_HOSTS,
                   help=f"Hosts per tenant, must be <= leaves (default {DEFAULT_HOSTS})")
    p.add_argument("--out", default="test-data",
                   help="Output directory (default: test-data/)")
    args = p.parse_args()

    NP, NS, NL, NH = args.planes, args.spines, args.leaves, args.hosts
    if NH > NL:
        p.error(f"--hosts ({NH}) must be <= --leaves ({NL})")

    out = Path(args.out)
    out.mkdir(exist_ok=True)

    files = []

    for plane in range(NP):
        path = out / f"mrc-p{plane}-fabric.json"
        path.write_text(json.dumps(gen_fabric(plane, NS, NL), indent=2))
        files.append(path)

    for plane in range(NP):
        path = out / f"mrc-p{plane}-compute.json"
        path.write_text(json.dumps(gen_compute(plane, NL, NH), indent=2))
        files.append(path)

    path = out / "mrc-compose.json"
    path.write_text(json.dumps(gen_compose(NP), indent=2))
    files.append(path)

    path = out / "mrc-tenant-green.json"
    path.write_text(json.dumps(gen_tenant_green(NP, NL, NH), indent=2))
    files.append(path)

    path = out / "mrc-tenant-yellow.json"
    path.write_text(json.dumps(gen_tenant_yellow(NP, NH), indent=2))
    files.append(path)

    print(
        f"Generated {len(files)} files "
        f"({NP} planes, {NS} spines, {NL} leaves, {NH} hosts/tenant):",
        file=sys.stderr,
    )
    for f in files:
        print(f"  {f}", file=sys.stderr)

    node_ip = "<node-ip>"
    base = f"http://{node_ip}:30080"
    print("\n# Deployment sequence:", file=sys.stderr)
    for plane in range(NP):
        print(f"curl -s -X POST {base}/topology -H 'Content-Type: application/json'"
              f" -d @{out}/mrc-p{plane}-fabric.json | python3 -m json.tool", file=sys.stderr)
    for plane in range(NP):
        print(f"curl -s -X POST {base}/topology -H 'Content-Type: application/json'"
              f" -d @{out}/mrc-p{plane}-compute.json | python3 -m json.tool", file=sys.stderr)
    print(f"curl -s -X POST {base}/topology/compose -H 'Content-Type: application/json'"
          f" -d @{out}/mrc-compose.json | python3 -m json.tool", file=sys.stderr)
    print(f"curl -s -X POST {base}/topology -H 'Content-Type: application/json'"
          f" -d @{out}/mrc-tenant-green.json | python3 -m json.tool", file=sys.stderr)
    print(f"curl -s -X POST {base}/topology -H 'Content-Type: application/json'"
          f" -d @{out}/mrc-tenant-yellow.json | python3 -m json.tool", file=sys.stderr)

if __name__ == "__main__":
    main()
