#!/usr/bin/env python3
"""
gen-multiplane.py — generates syd topology JSON files for a multi-plane
SRv6 AI fabric matching the addressing conventions in test-topology/.

Each plane P uses its own uSID block fc00:000<P>::/32:

  Spine locator       fc00:000P:1S::/48   uN SID
  Leaf  locator       fc00:000P:2L::/48   uN SID  (L in hex)
  Leaf → spine uA     fc00:000P:f00S::/48
  Spine → leaf uA     fc00:000P:e00L::/48
  Leaf → NIC uA       fc00:000P:e009::/48  (yellow NIC port, port-index 9 = Ethernet36)
  Leaf uDT6 green     fc00:000P:d000::/48  Vrf-green  (hybrid decap at egress leaf)
  NIC  uDT6 yellow    fc00:000P:d001::/48  End.DT6    (host-based decap at destination NIC)

Cluster aggregate:    fc00:0000::/30  (covers 4 planes)
Fabric P2P links:     2001:db8:fab:<S*LEAVES+L>::/127  (reused per plane, L2-isolated)
Green NIC uplinks:    2001:db8:bbbb:<P*256+H>::/64
Yellow NIC uplinks:   2001:db8:cccc:<P*256+H>::/64

Outputs (in --out directory, default test-data/):

  multiplane-p{P}-fabric.json    Day-0 : per-plane fabric (spines + leaves + links)
  multiplane-p{P}-compute.json   Day-1 : per-plane compute overlay (NICs + attachments)
  multiplane-compose.json        Day-0 : POST /topology/compose request body
  multiplane-tenant-green.json   Day-N : green/hybrid tenant (leaf uDT6 Vrf-green)
  multiplane-tenant-yellow.json  Day-N : yellow/host-based tenant (NIC uDT6 End.DT6)

Usage:
  # Full scale matching test-topology/ (96 SONiC nodes, 32 hosts per color)
  python3 test-data/gen-multiplane.py

  # Small scale for quick testing
  python3 test-data/gen-multiplane.py --planes 2 --spines 4 --leaves 4 --hosts 4

  # Single plane for CI/unit tests
  python3 test-data/gen-multiplane.py --planes 1 --spines 2 --leaves 4 --hosts 4

NOTE — path engine gap for host-based (yellow) tenant:
  The full host-based segment list requires a leaf→NIC uA SID as the penultimate
  entry, e.g.: [leaf-uA, spine-uA, leaf→NIC-uA, NIC-uDT].
  The leaf→NIC uA SID is present on the yellow access interface in the compute
  overlay (access_iface_id on the attachment edge). The current path engine does
  not yet insert this final leaf→NIC hop; that enhancement tracks as a roadmap
  item. The topology data is architecturally complete.
"""

import argparse
import json
import sys
from pathlib import Path

# ---------------------------------------------------------------------------
# Scale defaults — matches test-topology/ at full scale
# ---------------------------------------------------------------------------
DEFAULT_PLANES = 4
DEFAULT_SPINES = 8
DEFAULT_LEAVES = 16
DEFAULT_HOSTS  = 16   # hosts per color per plane (one host per leaf)

# IGP metric for all fabric links
IGP_METRIC = 10

# uSID F3216 structure
SID_STRUCT = {
    "locator_block_len": 32,
    "locator_node_len":  16,
    "function_len":      16,
    "argument_len":       0
}

# ---------------------------------------------------------------------------
# Address helpers
# ---------------------------------------------------------------------------

def usid_block(P: int) -> str:
    """32-bit uSID block prefix for plane P: fc00:0000, fc00:0001, …"""
    return f"fc00:{P:04x}"

def spine_locator_prefix(P, S):  return f"{usid_block(P)}:{0x10+S:x}::/48"
def spine_locator_sid(P, S):     return f"{usid_block(P)}:{0x10+S:x}::"
def leaf_locator_prefix(P, L):   return f"{usid_block(P)}:{0x20+L:x}::/48"
def leaf_locator_sid(P, L):      return f"{usid_block(P)}:{0x20+L:x}::"

def leaf_to_spine_ua_sid(P, S):  return f"{usid_block(P)}:f{S:03x}::"   # leaf going up → spine S
def spine_to_leaf_ua_sid(P, L):  return f"{usid_block(P)}:e{L:03x}::"   # spine going down → leaf L
def leaf_to_nic_ua_sid(P):       return f"{usid_block(P)}:e009::"        # leaf going down → NIC (Ethernet36, port-idx 9)

def green_udt_sid(P):            return f"{usid_block(P)}:d000::"        # Vrf-green uDT6 on leaf
def yellow_udt_sid(P):           return f"{usid_block(P)}:d001::"        # End.DT6 uDT6 on NIC

def fab_p2p_addr(S, L, NL, side):
    """Fabric P2P /127 for spine S ↔ leaf L (spine=::0, leaf=::1). NL=num_leaves."""
    idx = S * NL + L
    suffix = "0" if side == "spine" else "1"
    return f"2001:db8:fab:{idx:04x}::{suffix}/127"

def green_gateway_addr(P, H): return f"2001:db8:bbbb:{P*256+H:04x}::1/64"
def green_nic_addr(P, H):     return f"2001:db8:bbbb:{P*256+H:04x}::2"
def yellow_gateway_addr(P, H): return f"2001:db8:cccc:{P*256+H:04x}::1/64"
def yellow_nic_addr(P, H):     return f"2001:db8:cccc:{P*256+H:04x}::2"

# ---------------------------------------------------------------------------
# Vertex / edge ID helpers
# ---------------------------------------------------------------------------

def spine_id(P, S):          return f"p{P}-spine{S:02d}"
def leaf_id(P, L):           return f"p{P}-leaf{L:02d}"
def green_nic_id(P, H):      return f"green-host{H:02d}-p{P}-nic"
def yellow_nic_id(P, H):     return f"yellow-host{H:02d}-p{P}-nic"

def spine_iface_id(P, S, L): return f"iface:{spine_id(P,S)}/Eth{L*4}"   # spine's port toward leaf L
def leaf_iface_id(P, L, S):  return f"iface:{leaf_id(P,L)}/Eth{S*4}"    # leaf's port toward spine S
def leaf_green_iface_id(P, L): return f"iface:{leaf_id(P,L)}/Eth32-green"
def leaf_yellow_iface_id(P, L): return f"iface:{leaf_id(P,L)}/Eth36-yellow"

def green_vrf_id(P, L):      return f"vrf:green:p{P}-leaf{L:02d}"
def yellow_vrf_id(P, H):     return f"vrf:yellow:yellow-host{H:02d}-p{P}"

# ---------------------------------------------------------------------------
# SID constructors
# ---------------------------------------------------------------------------

def node_sid(value, behavior="End"):
    return {"sid": value, "behavior": behavior, "structure": SID_STRUCT}

def ua_sid(value):
    return {"sid": value, "behavior": "End.X", "structure": SID_STRUCT}

def udt_sid(value, behavior="End.DT6"):
    return {"sid": value, "behavior": behavior, "structure": SID_STRUCT}

def locator(prefix, sid_value):
    return {"prefix": prefix, "algo_id": 0, "node_sid": node_sid(sid_value)}

# ---------------------------------------------------------------------------
# Day-0: per-plane fabric document
# Spines, leaves, fabric P2P interfaces, igp_adjacency edges, SRv6 SIDs.
# ---------------------------------------------------------------------------

def gen_fabric(P: int, NS: int, NL: int) -> dict:
    nodes, interfaces, edges = [], [], []

    for S in range(NS):
        nodes.append({
            "id":        spine_id(P, S),
            "subtype":   "switch",
            "name":      spine_id(P, S),
            "srv6_locators": [locator(spine_locator_prefix(P, S), spine_locator_sid(P, S))]
        })
        for L in range(NL):
            interfaces.append({
                "id":           spine_iface_id(P, S, L),
                "owner_node_id": spine_id(P, S),
                "name":         f"Ethernet{L*4}",
                "addresses":    [fab_p2p_addr(S, L, NL, "spine")],
                "srv6_ua_sids": [ua_sid(spine_to_leaf_ua_sid(P, L))]
            })

    for L in range(NL):
        nodes.append({
            "id":        leaf_id(P, L),
            "subtype":   "switch",
            "name":      leaf_id(P, L),
            "srv6_locators": [locator(leaf_locator_prefix(P, L), leaf_locator_sid(P, L))]
        })
        for S in range(NS):
            interfaces.append({
                "id":           leaf_iface_id(P, L, S),
                "owner_node_id": leaf_id(P, L),
                "name":         f"Ethernet{S*4}",
                "addresses":    [fab_p2p_addr(S, L, NL, "leaf")],
                "srv6_ua_sids": [ua_sid(leaf_to_spine_ua_sid(P, S))]
            })

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
                "igp_metric":      IGP_METRIC
            })

    return {
        "topology_id":  f"fabric-p{P}",
        "description":  (
            f"Plane {P} fabric — {NS} spines × {NL} leaves, "
            f"SRv6 uSID block {usid_block(P)}::/32"
        ),
        "source": "push",
        "nodes":      nodes,
        "interfaces": interfaces,
        "edges":      edges
    }

# ---------------------------------------------------------------------------
# Day-1: per-plane compute overlay  (merge onto fabric-pP)
# Green NIC endpoints (hybrid: leaf decaps) and yellow NIC endpoints
# (host-based: NIC decaps).  Leaf access interfaces carry the leaf→NIC uA
# SID for yellow, which the path engine will use for the penultimate hop.
# ---------------------------------------------------------------------------

def gen_compute(P: int, NL: int, NH: int) -> dict:
    """NH = hosts per color; must be <= NL (one host per leaf)."""
    endpoints, interfaces, edges = [], [], []

    for H in range(NH):
        L = H   # host H is attached to leaf H in each plane

        # ---- green NIC (hybrid: leaf Ethernet32, Vrf-green) ----
        g_nic = green_nic_id(P, H)
        g_iface = leaf_green_iface_id(P, L)

        endpoints.append({
            "id":       g_nic,
            "subtype":  "gpu",
            "name":     f"green-host{H:02d}-plane{P}",
            "addresses": [green_nic_addr(P, H)],
            "metadata": {
                "host":   f"green-host{H:02d}",
                "plane":  str(P),
                "tenant": "green",
                "model":  "hybrid"
            }
        })
        interfaces.append({
            "id":           g_iface,
            "owner_node_id": leaf_id(P, L),
            "name":         "Ethernet32",
            "addresses":    [green_gateway_addr(P, H)],
            # No uA SID: in hybrid mode the leaf decaps into Vrf-green via uDT6;
            # normal VRF routing delivers the inner packet to the NIC.
        })
        edges.append({
            "id":             f"att:{g_nic}:{leaf_id(P,L)}",
            "type":           "attachment",
            "src_id":         g_nic,
            "dst_id":         leaf_id(P, L),
            "directed":       True,
            "access_iface_id": g_iface
        })

        # ---- yellow NIC (host-based: leaf Ethernet36, default VRF) ----
        y_nic = yellow_nic_id(P, H)
        y_iface = leaf_yellow_iface_id(P, L)

        endpoints.append({
            "id":       y_nic,
            "subtype":  "gpu",
            "name":     f"yellow-host{H:02d}-plane{P}",
            "addresses": [yellow_nic_addr(P, H)],
            "metadata": {
                "host":   f"yellow-host{H:02d}",
                "plane":  str(P),
                "tenant": "yellow",
                "model":  "host-based"
            }
        })
        interfaces.append({
            "id":           y_iface,
            "owner_node_id": leaf_id(P, L),
            "name":         "Ethernet36",
            "addresses":    [yellow_gateway_addr(P, H)],
            # uA SID: leaf→NIC uA for the host-based penultimate hop.
            # The path engine needs to insert this when building the segment
            # list for host-based endpoints (roadmap item).
            "srv6_ua_sids": [ua_sid(leaf_to_nic_ua_sid(P))]
        })
        edges.append({
            "id":             f"att:{y_nic}:{leaf_id(P,L)}",
            "type":           "attachment",
            "src_id":         y_nic,
            "dst_id":         leaf_id(P, L),
            "directed":       True,
            "access_iface_id": y_iface
        })

    return {
        "topology_id": f"fabric-p{P}",
        "description": (
            f"Plane {P} compute overlay — "
            f"{NH} green NICs (hybrid) + {NH} yellow NICs (host-based)"
        ),
        "source": "push",
        "merge":  True,
        "endpoints":  endpoints,
        "interfaces": interfaces,
        "edges":      edges
    }

# ---------------------------------------------------------------------------
# Day-0: compose request body  (POST /topology/compose)
# ---------------------------------------------------------------------------

def gen_compose(NP: int) -> dict:
    return {
        "topology_id": "cluster",
        "sources":     [f"fabric-p{P}" for P in range(NP)],
        "description": (
            f"{NP}-plane cluster graph — composed from "
            + ", ".join(f"fabric-p{P}" for P in range(NP))
        )
    }

# ---------------------------------------------------------------------------
# Day-N: green/hybrid tenant overlay  (merge onto cluster)
# One VRF vertex per leaf per plane, owned by the leaf.
# Tenant boundary: egress leaf decapsulates and routes via Vrf-green.
# ---------------------------------------------------------------------------

def gen_tenant_green(NP: int, NL: int, NH: int) -> dict:
    vrfs, edges = [], []

    for P in range(NP):
        for L in range(NL):
            vrf_id = green_vrf_id(P, L)
            vrfs.append({
                "id":          vrf_id,
                "name":        "Vrf-green",
                "owner_node_id": leaf_id(P, L),
                "srv6_udt_sid": udt_sid(green_udt_sid(P))
            })
            # VRFMembership for the green NIC attached to this leaf
            H = L
            if H < NH:
                g_nic = green_nic_id(P, H)
                edges.append({
                    "id":       f"vrfmem:{g_nic}:{vrf_id}",
                    "type":     "vrf_membership",
                    "src_id":   g_nic,
                    "dst_id":   vrf_id,
                    "directed": True
                })

    return {
        "topology_id": "cluster",
        "description": (
            "Green/hybrid tenant Day-N overlay — "
            "uDT6 Vrf-green on egress leaves; source NIC encapsulates"
        ),
        "source": "push",
        "merge":  True,
        "vrfs":   vrfs,
        "edges":  edges
    }

# ---------------------------------------------------------------------------
# Day-N: yellow/host-based tenant overlay  (merge onto cluster)
# One VRF vertex per yellow NIC per plane, owned by the NIC endpoint.
# Tenant boundary: destination NIC decapsulates via seg6local End.DT6.
# VRF owner is the NIC (not the leaf) — leaf carries no VRF state.
# ---------------------------------------------------------------------------

def gen_tenant_yellow(NP: int, NH: int) -> dict:
    vrfs, edges = [], []

    for P in range(NP):
        for H in range(NH):
            y_nic = yellow_nic_id(P, H)
            vrf_id = yellow_vrf_id(P, H)
            vrfs.append({
                "id":          vrf_id,
                "name":        "yellow-default",
                # Owner is the NIC endpoint — this VRF lives on the host, not the leaf
                "owner_node_id": y_nic,
                "srv6_udt_sid": udt_sid(yellow_udt_sid(P))
            })
            edges.append({
                "id":       f"vrfmem:{y_nic}:{vrf_id}",
                "type":     "vrf_membership",
                "src_id":   y_nic,
                "dst_id":   vrf_id,
                "directed": True
            })

    return {
        "topology_id": "cluster",
        "description": (
            "Yellow/host-based tenant Day-N overlay — "
            "uDT6 End.DT6 on destination NICs; leaf is pure transit"
        ),
        "source": "push",
        "merge":  True,
        "vrfs":   vrfs,
        "edges":  edges
    }

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    p = argparse.ArgumentParser(
        description="Generate syd multi-plane topology JSON files",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__
    )
    p.add_argument("--planes", type=int, default=DEFAULT_PLANES,
                   help=f"Number of planes (default {DEFAULT_PLANES})")
    p.add_argument("--spines", type=int, default=DEFAULT_SPINES,
                   help=f"Spines per plane (default {DEFAULT_SPINES})")
    p.add_argument("--leaves", type=int, default=DEFAULT_LEAVES,
                   help=f"Leaves per plane (default {DEFAULT_LEAVES})")
    p.add_argument("--hosts",  type=int, default=DEFAULT_HOSTS,
                   help=f"Hosts per color per plane, must be <= leaves (default {DEFAULT_HOSTS})")
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
        path = out / f"multiplane-p{plane}-fabric.json"
        path.write_text(json.dumps(gen_fabric(plane, NS, NL), indent=2))
        files.append(path)

    for plane in range(NP):
        path = out / f"multiplane-p{plane}-compute.json"
        path.write_text(json.dumps(gen_compute(plane, NL, NH), indent=2))
        files.append(path)

    path = out / "multiplane-compose.json"
    path.write_text(json.dumps(gen_compose(NP), indent=2))
    files.append(path)

    path = out / "multiplane-tenant-green.json"
    path.write_text(json.dumps(gen_tenant_green(NP, NL, NH), indent=2))
    files.append(path)

    path = out / "multiplane-tenant-yellow.json"
    path.write_text(json.dumps(gen_tenant_yellow(NP, NH), indent=2))
    files.append(path)

    print(
        f"Generated {len(files)} files "
        f"({NP} planes, {NS} spines, {NL} leaves, {NH} hosts/color/plane):",
        file=sys.stderr
    )
    for f in files:
        print(f"  {f}", file=sys.stderr)

    # Print the curl sequence for convenience
    node_ip = "<node-ip>"
    base = f"http://{node_ip}:30080"
    print("\n# Deployment sequence:", file=sys.stderr)
    for plane in range(NP):
        print(f"curl -s -X POST {base}/topology -H 'Content-Type: application/json' -d @{out}/multiplane-p{plane}-fabric.json | python3 -m json.tool", file=sys.stderr)
    for plane in range(NP):
        print(f"curl -s -X POST {base}/topology -H 'Content-Type: application/json' -d @{out}/multiplane-p{plane}-compute.json | python3 -m json.tool", file=sys.stderr)
    print(f"curl -s -X POST {base}/topology/compose -H 'Content-Type: application/json' -d @{out}/multiplane-compose.json | python3 -m json.tool", file=sys.stderr)
    print(f"curl -s -X POST {base}/topology -H 'Content-Type: application/json' -d @{out}/multiplane-tenant-green.json | python3 -m json.tool", file=sys.stderr)
    print(f"curl -s -X POST {base}/topology -H 'Content-Type: application/json' -d @{out}/multiplane-tenant-yellow.json | python3 -m json.tool", file=sys.stderr)

if __name__ == "__main__":
    main()
