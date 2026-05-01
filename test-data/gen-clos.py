#!/usr/bin/env python3
"""
Generate a synthetic Clos fabric topology JSON for syd.

Default (no args):    4 spines, 8 leaves, 4 GPUs/leaf  =  32 GPUs
--scale medium:      16 spines, 32 leaves, 4 GPUs/leaf = 128 GPUs
--scale large:       32 spines, 64 leaves, 4 GPUs/leaf = 256 GPUs
--scale xl:          64 spines, 128 leaves, 4 GPUs/leaf = 512 GPUs

Usage:
  python3 gen-clos.py                        > clos-fabric.json
  python3 gen-clos.py --scale large          > clos-256gpu.json
  python3 gen-clos.py --spines 8 --leaves 16 --gpus-per-leaf 8 > custom.json
"""

import argparse
import json
import sys

BLOCK = "fc00:0:"   # SRv6 locator block prefix
IGP_METRIC   = 1
LINK_BW_BPS  = 400_000_000_000   # 400 Gbps
LINK_DELAY_US = 1

SCALES = {
    "small":  dict(spines=4,  leaves=8,   gpus_per_leaf=4),
    "medium": dict(spines=16, leaves=32,  gpus_per_leaf=4),
    "large":  dict(spines=32, leaves=64,  gpus_per_leaf=4),
    "xl":     dict(spines=64, leaves=128, gpus_per_leaf=4),
}


def f3216():
    return {"locator_block_len": 32, "locator_node_len": 16,
            "function_len": 0, "argument_len": 0}


def f3216_ua():
    return {"locator_block_len": 32, "locator_node_len": 16,
            "function_len": 16, "argument_len": 0}


def node_sid(node_idx: int) -> str:
    """fc00:0:<hex-idx>:: — unique uN SID per node."""
    return f"{BLOCK}{node_idx:x}::"


def ua_sid(node_idx: int, iface_idx: int) -> str:
    """fc00:0:<hex-node>:<hex-iface>:: — unique uA SID per interface."""
    return f"{BLOCK}{node_idx:x}:{iface_idx:x}::"


def generate(n_spines: int, n_leaves: int, gpus_per_leaf: int) -> dict:
    nodes = []
    interfaces = []
    endpoints = []
    edges = []

    # Node index counter — every node and interface gets a unique index for
    # deterministic SID generation.
    nidx = 1

    # --- spine nodes ---
    spine_ids = []
    for s in range(1, n_spines + 1):
        sid = f"spine-{s:02d}"
        spine_ids.append(sid)
        nodes.append({
            "id": sid,
            "name": sid,
            "subtype": "switch",
            "labels": {"tier": "spine", "pod": "fabric"},
            "srv6_node_sid": {
                "sid": node_sid(nidx),
                "behavior": "End",
                "structure": f3216(),
            },
        })
        nidx += 1

    # --- leaf nodes and their GPU endpoints ---
    leaf_ids = []
    gpu_counter = 1

    for lf in range(1, n_leaves + 1):
        lid = f"leaf-{lf:02d}"
        leaf_ids.append(lid)
        nodes.append({
            "id": lid,
            "name": lid,
            "subtype": "switch",
            "labels": {"tier": "leaf", "pod": f"pod-{lf}"},
            "srv6_node_sid": {
                "sid": node_sid(nidx),
                "behavior": "End",
                "structure": f3216(),
            },
        })
        leaf_nidx = nidx
        nidx += 1

        # GPU endpoints attached to this leaf
        for g in range(1, gpus_per_leaf + 1):
            gid = f"gpu-{gpu_counter:03d}"
            gpu_counter += 1
            endpoints.append({
                "id": gid,
                "name": gid,
                "subtype": "gpu",
                "labels": {"tier": "gpu", "leaf": lid, "slot": str(g)},
            })
            edges.append({
                "id": f"attach:{gid}:{lid}",
                "type": "attachment",
                "src_id": gid,
                "dst_id": lid,
                "directed": True,
            })

    # --- interfaces and links: full-mesh spine↔leaf (both directions) ---
    # Each spine-leaf pair gets two directed edges.
    iface_idx = 1
    for s_idx, sid in enumerate(spine_ids, 1):
        spine_node_idx = s_idx   # spine nidx starts at 1
        for l_idx, lid in enumerate(leaf_ids, 1):
            leaf_node_idx = n_spines + l_idx  # leaf nidx follows spines

            s_iface = f"iface:{sid}/{lid}"
            l_iface = f"iface:{lid}/{sid}"

            interfaces.append({
                "id": s_iface,
                "owner_node_id": sid,
                "srv6_ua_sids": [{
                    "algo_id": 0,
                    "sid": ua_sid(spine_node_idx, iface_idx),
                    "behavior": "End.X",
                    "structure": f3216_ua(),
                }],
            })
            iface_idx += 1

            interfaces.append({
                "id": l_iface,
                "owner_node_id": lid,
                "srv6_ua_sids": [{
                    "algo_id": 0,
                    "sid": ua_sid(leaf_node_idx, iface_idx),
                    "behavior": "End.X",
                    "structure": f3216_ua(),
                }],
            })
            iface_idx += 1

            # spine → leaf
            edges.append({
                "id": f"link:{sid}:{lid}",
                "type": "igp_adjacency",
                "src_id": sid,
                "dst_id": lid,
                "directed": True,
                "local_iface_id": s_iface,
                "remote_iface_id": l_iface,
                "igp_metric": IGP_METRIC,
                "max_bw_bps": LINK_BW_BPS,
                "unidir_delay_us": LINK_DELAY_US,
            })
            # leaf → spine
            edges.append({
                "id": f"link:{lid}:{sid}",
                "type": "igp_adjacency",
                "src_id": lid,
                "dst_id": sid,
                "directed": True,
                "local_iface_id": l_iface,
                "remote_iface_id": s_iface,
                "igp_metric": IGP_METRIC,
                "max_bw_bps": LINK_BW_BPS,
                "unidir_delay_us": LINK_DELAY_US,
            })

    total_gpus = n_leaves * gpus_per_leaf
    desc = (f"{n_spines}-spine {n_leaves}-leaf Clos fabric, "
            f"{total_gpus} GPU endpoints ({gpus_per_leaf}/leaf). "
            f"Generated by gen-clos.py.")

    return {
        "topology_id": f"clos-{total_gpus}gpu",
        "description": desc,
        "nodes": nodes,
        "interfaces": interfaces,
        "endpoints": endpoints,
        "edges": edges,
    }


def main():
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--scale", choices=list(SCALES), default=None,
                   help="Named scale preset")
    p.add_argument("--spines", type=int, default=None)
    p.add_argument("--leaves", type=int, default=None)
    p.add_argument("--gpus-per-leaf", type=int, default=None)
    args = p.parse_args()

    if args.scale:
        cfg = SCALES[args.scale]
    else:
        cfg = SCALES["small"]

    if args.spines is not None:
        cfg["spines"] = args.spines
    if args.leaves is not None:
        cfg["leaves"] = args.leaves
    if args.gpus_per_leaf is not None:
        cfg["gpus_per_leaf"] = args.gpus_per_leaf

    topo = generate(cfg["spines"], cfg["leaves"], cfg["gpus_per_leaf"])

    # Summary to stderr so it doesn't pollute the JSON output
    n_nodes = len(topo["nodes"])
    n_ifaces = len(topo["interfaces"])
    n_eps = len(topo["endpoints"])
    n_edges = len(topo["edges"])
    leaf_pairs = cfg["leaves"] * (cfg["leaves"] - 1)
    gpu_pairs  = (cfg["leaves"] * cfg["gpus_per_leaf"])
    gpu_pairs  = gpu_pairs * (gpu_pairs - 1)
    print(f"Generated: {n_nodes} nodes, {n_ifaces} interfaces, "
          f"{n_eps} endpoints, {n_edges} edges", file=sys.stderr)
    print(f"  GPU pairs (old response size): {gpu_pairs:,}", file=sys.stderr)
    print(f"  Leaf pairs (new response size): {leaf_pairs:,}", file=sys.stderr)
    print(f"  Reduction factor: {gpu_pairs/max(leaf_pairs,1):.0f}x", file=sys.stderr)

    json.dump(topo, sys.stdout, indent=2)
    print()  # trailing newline


if __name__ == "__main__":
    main()
