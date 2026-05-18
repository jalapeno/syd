### mrc upload data

Same structure as multi-plane.md — here are the steps:


NODE_IP=<node-ip>
SYD=http://${NODE_IP}:30080

# Day-0: per-plane fabric
for P in 0 1 2 3; do
  curl -s -X POST ${SYD}/topology -H 'Content-Type: application/json' \
    -d @test-data/mrc-p${P}-fabric.json | python3 -m json.tool
done

# Day-1: compute overlay (merges onto fabric-p{P})
for P in 0 1 2 3; do
  curl -s -X POST ${SYD}/topology -H 'Content-Type: application/json' \
    -d @test-data/mrc-p${P}-compute.json | python3 -m json.tool
done

# Day-0 compose: build mrc-cluster from all 4 planes
curl -s -X POST ${SYD}/topology/compose -H 'Content-Type: application/json' \
  -d @test-data/mrc-compose.json | python3 -m json.tool

# Day-N: tenant overlays (both merge onto mrc-cluster)
curl -s -X POST ${SYD}/topology -H 'Content-Type: application/json' \
  -d @test-data/mrc-tenant-green.json | python3 -m json.tool
curl -s -X POST ${SYD}/topology -H 'Content-Type: application/json' \
  -d @test-data/mrc-tenant-yellow.json | python3 -m json.tool
Then verify:


# List all topologies (should show mrc-p0..3 + mrc-cluster)
curl -s ${SYD}/topology | python3 -m json.tool

# Check cluster node count
curl -s ${SYD}/topology/mrc-cluster | python3 -m json.tool

# Quick path request — green pair across plane 0
curl -s -X POST ${SYD}/paths/request \
  -H 'Content-Type: application/json' \
  -d '{
    "topology_id": "mrc-cluster",
    "workload_id":  "mrc-green-test",
    "endpoints": [
      {"id": "green-host00-p0-nic"},
      {"id": "green-host15-p0-nic"}
    ],
    "pairing_mode": "bidir_paired"
  }' | python3 -m json.tool

# Retrieve segment lists
curl -s ${SYD}/paths/mrc-green-test/flows | python3 -m json.tool