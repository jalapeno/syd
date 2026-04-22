#!/usr/bin/env bash
# test-local.sh — build syd, run it locally, push test data, and exercise
# the push-API curl scenarios from notes.md. No NATS/BMP required.
#
# Usage:
#   ./scripts/test-local.sh               # build and run all tests
#   ./scripts/test-local.sh --no-build    # skip go build (use existing /tmp/syd-test)
#   ./scripts/test-local.sh --port 18080  # use a specific port (default: 18080)
#
# Exit code: 0 if all tests pass, 1 if any fail.

set -euo pipefail

# --- Configuration -----------------------------------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT=18080
BUILD=1
SYD_PID=""

# --- Argument parsing --------------------------------------------------------
while [[ $# -gt 0 ]]; do
  case $1 in
    --no-build) BUILD=0 ;;
    --port) PORT="$2"; shift ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
  shift
done

BASE="http://localhost:${PORT}"

# --- Helpers -----------------------------------------------------------------
PASS=0; FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

# check_status label want got
check_status() {
  local label="$1" want="$2" got="$3"
  if [[ "$got" == "$want" ]]; then pass "$label (HTTP $got)";
  else fail "$label: want HTTP $want, got $got"; fi
}

# check_json_field label field body — passes if JSON body contains "field"
check_json_field() {
  local label="$1" field="$2" body="$3"
  if echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); assert '$field' in str(d)" 2>/dev/null; then
    pass "$label"
  else
    fail "$label (field '$field' not in body)"
    echo "    body: ${body:0:200}"
  fi
}

# json_get label body python_expr — runs python_expr on parsed body, prints result
json_get() {
  echo "$2" | python3 -c "import sys,json; d=json.load(sys.stdin); print($3)" 2>/dev/null || echo ""
}

cleanup() {
  if [[ -n "$SYD_PID" ]]; then
    kill "$SYD_PID" 2>/dev/null || true
    wait "$SYD_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# --- Kill anything already on PORT -------------------------------------------
if lsof -ti tcp:"$PORT" &>/dev/null; then
  echo "==> Port $PORT in use — killing existing process..."
  lsof -ti tcp:"$PORT" | xargs kill -9 2>/dev/null || true
  sleep 0.5
fi

# --- Build -------------------------------------------------------------------
cd "$REPO_ROOT"
if [[ $BUILD -eq 1 ]]; then
  echo "==> Building syd..."
  go build -o /tmp/syd-test ./cmd/syd
fi

# --- Start syd ---------------------------------------------------------------
echo "==> Starting syd on :${PORT}..."
/tmp/syd-test --addr ":${PORT}" &>/tmp/syd-test.log &
SYD_PID=$!

# Wait until the server accepts connections (up to 10 s), then verify syd is
# still running (it would exit immediately if the port was still in use).
for i in $(seq 1 20); do
  if curl -s -o /dev/null "$BASE/topology" 2>/dev/null; then break; fi
  sleep 0.5
done
if ! kill -0 "$SYD_PID" 2>/dev/null; then
  echo "ERROR: syd exited unexpectedly. Log:" >&2
  cat /tmp/syd-test.log >&2
  exit 1
fi
if ! curl -s -o /dev/null "$BASE/topology" 2>/dev/null; then
  echo "ERROR: syd did not start within 10 s. Log:" >&2
  cat /tmp/syd-test.log >&2
  exit 1
fi
echo "    syd is up (pid $SYD_PID)"

# =============================================================================
echo
echo "==> Test: topology list (empty)"
# =============================================================================
body=$(curl -s "$BASE/topology")
check_json_field "empty topology list returns topology_ids" "topology_ids" "$body"

# =============================================================================
echo
echo "==> Test: push clos-fabric topology"
# =============================================================================
body=$(curl -s -X POST "$BASE/topology" \
  -H 'Content-Type: application/json' \
  -d @test-data/clos-fabric.json)
check_json_field "clos-fabric push returns topology_id" "clos-fabric" "$body"

node_count=$(json_get "" "$body" "d['stats']['nodes']")
if [[ "${node_count:-0}" -ge 12 ]]; then pass "clos-fabric stats: nodes=$node_count";
else fail "clos-fabric stats: expected >=12 nodes, got ${node_count:-?}"; fi

# =============================================================================
echo
echo "==> Test: clos-fabric path request (gpu-001 → gpu-017)"
# =============================================================================
body=$(curl -s -X POST "$BASE/paths/request" \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "clos-fabric",
    "workload_id": "test-clos-01",
    "endpoints": [{"id":"gpu-001"},{"id":"gpu-017"}],
    "pairing_mode": "all_directed"
  }')

path_count=$(json_get "" "$body" "len(d.get('paths',[]))")
if [[ "${path_count:-0}" -ge 2 ]]; then pass "clos paths returned: $path_count";
else fail "clos paths: expected >=2, got ${path_count:-?}"; fi

has_vids=$(json_get "" "$body" "'yes' if d['paths'][0].get('vertex_ids') else 'no'")
if [[ "$has_vids" == "yes" ]]; then pass "clos path has vertex_ids";
else fail "clos path missing vertex_ids"; fi

has_sids=$(json_get "" "$body" "'yes' if d['paths'][0].get('segment_list',{}).get('sids') else 'no'")
if [[ "$has_sids" == "yes" ]]; then pass "clos path has SIDs";
else fail "clos path missing SIDs"; fi

echo "$(json_get "" "$body" "chr(10).join(
  f'    {p[\"src_id\"]} -> {p[\"dst_id\"]}  hops={p[\"metric\"][\"hop_count\"]}  sids={p[\"segment_list\"][\"sids\"]}'
  for p in d[\"paths\"][:2])")"

curl -s -X POST "$BASE/paths/test-clos-01/complete" \
  -H 'Content-Type: application/json' -d '{"immediate":true}' > /dev/null

# =============================================================================
echo
echo "==> Test: clos-fabric all-to-all (4 endpoints, link disjoint)"
# =============================================================================
body=$(curl -s -X POST "$BASE/paths/request" \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "clos-fabric",
    "workload_id": "test-clos-alltoall",
    "endpoints": [
      {"id":"gpu-001"},{"id":"gpu-005"},
      {"id":"gpu-017"},{"id":"gpu-021"}
    ],
    "disjointness": "link",
    "pairing_mode": "all_directed"
  }')
path_count=$(json_get "" "$body" "len(d.get('paths',[]))")
if [[ "${path_count:-0}" -ge 4 ]]; then pass "clos all-to-all paths: $path_count";
else fail "clos all-to-all: expected >=4 paths, got ${path_count:-?}"; fi

curl -s -X POST "$BASE/paths/test-clos-alltoall/complete" \
  -H 'Content-Type: application/json' -d '{"immediate":true}' > /dev/null

# =============================================================================
echo
echo "==> Test: push minimal SRv6 topology (uA+uN SIDs)"
# =============================================================================
# Note: srv6_ua_sids items embed srv6.SID, so the JSON uses "sid" for the
# address value and "structure" for the bit-field layout (not "func_len").
body=$(curl -s -X POST "$BASE/topology" \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "test-srv6",
    "nodes": [
      {
        "id": "0000.0000.0001", "name": "r1",
        "srv6_locators": [
          {"prefix":"fc00:0:1::/48","block_len":32,"node_len":16,"func_len":0,"arg_len":0,"algo_id":0}
        ]
      },
      {
        "id": "0000.0000.0002", "name": "r2",
        "srv6_locators": [
          {"prefix":"fc00:0:2::/48","block_len":32,"node_len":16,"func_len":0,"arg_len":0,"algo_id":0}
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
      {"id":"r1r2","type":"igp_adjacency","src_id":"0000.0000.0001","dst_id":"0000.0000.0002",
       "igp_metric":1,"local_iface_id":"r1-eth0","remote_iface_id":"r2-eth0"},
      {"id":"r2r1","type":"igp_adjacency","src_id":"0000.0000.0002","dst_id":"0000.0000.0001",
       "igp_metric":1,"local_iface_id":"r2-eth0","remote_iface_id":"r1-eth0"}
    ]
  }')
check_json_field "test-srv6 push" "test-srv6" "$body"

body=$(curl -s -X POST "$BASE/paths/request" \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "test-srv6",
    "workload_id": "wl-srv6",
    "endpoints": [{"id":"0000.0000.0001"},{"id":"0000.0000.0002"}]
  }')
sids=$(json_get "" "$body" "str(d['paths'][0].get('segment_list',{}).get('sids',[]))" )
if echo "$sids" | grep -qE "e001"; then
  pass "test-srv6 path has uA SIDs: $sids"
else
  fail "test-srv6 path missing expected uA SIDs (got: $sids)"
fi

curl -s -X POST "$BASE/paths/wl-srv6/complete" \
  -H 'Content-Type: application/json' -d '{"immediate":true}' > /dev/null
curl -s -X DELETE "$BASE/topology/test-srv6" > /dev/null

# =============================================================================
echo
echo "==> Test: incremental topology push + drain on element removal"
# =============================================================================
# Nodes need SRv6 node SIDs so path computation can succeed. Without them,
# BuildSegmentList returns an error and no workload is created.
curl -s -X POST "$BASE/topology" \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "test-incr",
    "nodes": [
      {"id":"A","srv6_node_sid":{"sid":"fc00:0:a::","behavior":"End","structure":{"locator_block_len":32,"locator_node_len":16,"function_len":0,"argument_len":0}}},
      {"id":"B","srv6_node_sid":{"sid":"fc00:0:b::","behavior":"End","structure":{"locator_block_len":32,"locator_node_len":16,"function_len":0,"argument_len":0}}},
      {"id":"C","srv6_node_sid":{"sid":"fc00:0:c::","behavior":"End","structure":{"locator_block_len":32,"locator_node_len":16,"function_len":0,"argument_len":0}}}
    ],
    "edges": [
      {"id":"AB","type":"igp_adjacency","src_id":"A","dst_id":"B","igp_metric":1},
      {"id":"BA","type":"igp_adjacency","src_id":"B","dst_id":"A","igp_metric":1},
      {"id":"BC","type":"igp_adjacency","src_id":"B","dst_id":"C","igp_metric":1},
      {"id":"CB","type":"igp_adjacency","src_id":"C","dst_id":"B","igp_metric":1}
    ]
  }' > /dev/null

# Allocate a workload that traverses C
wl_body=$(curl -s -X POST "$BASE/paths/request" \
  -H 'Content-Type: application/json' \
  -d '{"topology_id":"test-incr","workload_id":"wl-through-c","endpoints":[{"id":"A"},{"id":"C"}]}')
wl_paths=$(json_get "" "$wl_body" "len(d.get('paths',[]))")
if [[ "${wl_paths:-0}" -ge 1 ]]; then pass "test-incr workload created: $wl_paths paths";
else fail "test-incr workload creation failed: 0 paths (body: ${wl_body:0:100})"; fi

# Push v2 — node C removed
curl -s -X POST "$BASE/topology" \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "test-incr",
    "nodes": [
      {"id":"A","srv6_node_sid":{"sid":"fc00:0:a::","behavior":"End","structure":{"locator_block_len":32,"locator_node_len":16,"function_len":0,"argument_len":0}}},
      {"id":"B","srv6_node_sid":{"sid":"fc00:0:b::","behavior":"End","structure":{"locator_block_len":32,"locator_node_len":16,"function_len":0,"argument_len":0}}}
    ],
    "edges": [
      {"id":"AB","type":"igp_adjacency","src_id":"A","dst_id":"B","igp_metric":1},
      {"id":"BA","type":"igp_adjacency","src_id":"B","dst_id":"A","igp_metric":1}
    ]
  }' > /dev/null

# wl-through-c should be DRAINING with reason=topology_change
state=$(json_get "" "$(curl -s "$BASE/paths/wl-through-c")" "d.get('state','?')")
reason=$(json_get "" "$(curl -s "$BASE/paths/wl-through-c")" "d.get('drain_reason','?')")
if [[ "$(echo "$state" | tr '[:upper:]' '[:lower:]')" == "draining" ]]; then pass "incremental push: workload drained (state=$state reason=$reason)";
else fail "incremental push: expected draining, got state=${state:-?}"; fi

curl -s -X DELETE "$BASE/topology/test-incr" > /dev/null

# =============================================================================
echo
echo "==> Test: policy mapping"
# =============================================================================
curl -s -X POST "$BASE/topology" \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "test-policy",
    "nodes": [
      {"id":"X","srv6_node_sid":{"sid":"fc00:0:e::","behavior":"End","structure":{"locator_block_len":32,"locator_node_len":16,"function_len":0,"argument_len":0}}},
      {"id":"Y","srv6_node_sid":{"sid":"fc00:0:f::","behavior":"End","structure":{"locator_block_len":32,"locator_node_len":16,"function_len":0,"argument_len":0}}}
    ],
    "edges": [
      {"id":"XY","type":"igp_adjacency","src_id":"X","dst_id":"Y","igp_metric":1},
      {"id":"YX","type":"igp_adjacency","src_id":"Y","dst_id":"X","igp_metric":1}
    ]
  }' > /dev/null

body=$(curl -s -X POST "$BASE/topology/test-policy/policies" \
  -H 'Content-Type: application/json' \
  -d '{"policies":[{"name":"latency-optimized","algo_id":128},{"name":"carbon-optimized","algo_id":130}]}')
check_json_field "policy set" "policies" "$body"

body=$(curl -s "$BASE/topology/test-policy/policies")
has_lat=$(json_get "" "$body" "'yes' if any(p['name']=='latency-optimized' for p in d.get('policies',[])) else 'no'")
if [[ "$has_lat" == "yes" ]]; then pass "policy get: latency-optimized present";
else fail "policy get: latency-optimized missing (body: ${body:0:100})"; fi

# Unknown policy should return 422
status=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/paths/request" \
  -H 'Content-Type: application/json' \
  -d '{"topology_id":"test-policy","workload_id":"bad-policy",
       "endpoints":[{"id":"X"},{"id":"Y"}],"policy":"nonexistent"}')
check_status "unknown policy returns 422" "422" "$status"

curl -s -X DELETE "$BASE/topology/test-policy" > /dev/null

# =============================================================================
echo
echo "==> Test: workload status and flows"
# =============================================================================
curl -s -X POST "$BASE/paths/request" \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id":"clos-fabric","workload_id":"wl-flows-check",
    "endpoints":[{"id":"gpu-001"},{"id":"gpu-017"}]
  }' > /dev/null

status_body=$(curl -s "$BASE/paths/wl-flows-check")
wl_state=$(json_get "" "$status_body" "d.get('state','?')")
if [[ "$(echo "$wl_state" | tr '[:upper:]' '[:lower:]')" == "active" ]]; then pass "workload status: active";
else fail "workload status: expected active, got ${wl_state:-?}"; fi

flows_body=$(curl -s "$BASE/paths/wl-flows-check/flows")
flow_count=$(json_get "" "$flows_body" "len(d.get('flows',[]))")
if [[ "${flow_count:-0}" -ge 1 ]]; then pass "flows endpoint: $flow_count flows";
else fail "flows endpoint: expected >=1 flows, got ${flow_count:-?}"; fi

has_outer_da=$(json_get "" "$flows_body" "'yes' if d['flows'][0].get('outer_da') else 'no'")
if [[ "$has_outer_da" == "yes" ]]; then pass "flow has outer_da";
else fail "flow missing outer_da"; fi

curl -s -X POST "$BASE/paths/wl-flows-check/complete" \
  -H 'Content-Type: application/json' -d '{"immediate":true}' > /dev/null

# =============================================================================
echo
echo "==> Test: allocation state endpoint"
# =============================================================================
status=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/paths/state")
check_status "allocation state returns 200" "200" "$status"

# =============================================================================
echo
echo "==> Cleanup: delete clos-fabric"
# =============================================================================
status=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$BASE/topology/clos-fabric")
check_status "DELETE clos-fabric" "204" "$status"

# =============================================================================
echo
echo "==> Results"
# =============================================================================
TOTAL=$((PASS + FAIL))
echo "    $PASS/$TOTAL tests passed"
if [[ $FAIL -gt 0 ]]; then
  echo "    $FAIL FAILED"
  exit 1
fi
echo "    All tests passed."
