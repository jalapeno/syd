# syd Kubernetes Deployment

Deploys syd alongside an existing Jalapeno stack (NATS, GoBMP).

## Architecture

```
containerlab (XRd / FRR)
  ├─ BMP/TCP ──→ port 30511 ──→ gobmp pod (existing Jalapeno)
  │                                └─ Kafka ──→ igp-graph / arango consumers
  │
  └─ BMP/TCP ──→ port 30512 ──→ gobmp-nats pod  (deployed here)
                                   └─ NATS JetStream ──→ syd pod  ←── scheduler / UI
                                                          (namespace: syd)
```

Routers send BMP to **both** ports. The existing Jalapeno gobmp is left untouched.
syd only connects **out** to NATS and listens on HTTP :8080.

---

## Prerequisites

- Jalapeno stack already running (NATS, existing gobmp on port 30511)
- k3s / containerd node with docker available for local image builds

---

## 1. Build and load the gobmp-nats image

The standard `sbezverk/gobmp:latest` image does not support NATS output. You must
build `gobmp:nats` from source using `Dockerfile.gobmp` in this directory.

```bash
# Clone gobmp source (do this once, anywhere convenient)
git clone https://github.com/sbezverk/gobmp.git ~/gobmp

# Build — run from INSIDE the gobmp source tree, pointing at syd's Dockerfile
cd ~/gobmp
docker build -f ~/src/syd/deploy/k8s/Dockerfile.gobmp -t gobmp:nats .

# Load into k3s (no registry needed)
docker save gobmp:nats | sudo k3s ctr images import -
```

---

## 2. Deploy gobmp-nats

```bash
kubectl apply -f deploy/k8s/gobmp-nats.yaml
kubectl -n jalapeno rollout status deployment/gobmp-nats
kubectl -n jalapeno logs -f deployment/gobmp-nats
```

In the logs you should see gobmp-nats connecting to NATS and starting to publish
on `gobmp.parsed.*` subjects once your BMP sources are pointed at port 30512.

Configure your routers to send BMP to **both** ports:
- port **30511** → existing gobmp (Kafka / Jalapeno consumers)
- port **30512** → gobmp-nats (NATS / syd)

---

## 3. Build and load the syd image

```bash
# From the repo root:
docker build -t syd:latest .

# For k3s:
docker save syd:latest | sudo k3s ctr images import -
```

---

## 4. Configure and deploy syd

The default `configmap.yaml` already points `NATS_URL` at `nats://nats.jalapeno:4222`.
If your NATS service is in a different namespace or address, edit it first:

```bash
# Check where NATS is running
kubectl get svc -A | grep nats
```

Then deploy:

```bash
kubectl apply -k deploy/k8s/
kubectl -n syd rollout status deployment/syd
kubectl -n syd logs -f deployment/syd
```

Expected startup output:

```
level=INFO msg="bmp collector configured" nats_url=nats://nats.jalapeno:4222
level=INFO msg="syd starting" addr=:8080 bmp=true encap_mode=host
```

---

## 5. Access the API

**NodePort (from your laptop or the containerlab VM):**

```bash
# Find your node IP
kubectl get nodes -o wide

curl http://<node-ip>:30080/topology
```

**Port-forward (one-off testing):**

```bash
kubectl -n syd port-forward deployment/syd 8080:8080
curl http://localhost:8080/topology
```

**From inside the cluster:**

```
http://syd.syd.svc.cluster.local:8080
```

---

## 6. Verify BMP topology ingestion

Once your containerlab nodes are sending BMP to port 30512, syd will start
building the underlay topology. Watch the log:

```bash
kubectl -n syd logs -f deployment/syd | grep -E "topology|bmp"
```

Then query:

```bash
# List learned topologies
curl http://<node-ip>:30080/topology

# Node list for a topology
curl http://<node-ip>:30080/topology/underlay-v6/nodes | python3 -m json.tool | grep name
```

---

## Updating the deployment

```bash
# After rebuilding syd image:
kubectl -n syd rollout restart deployment/syd
kubectl -n syd rollout status deployment/syd

# After rebuilding gobmp:nats image:
kubectl -n jalapeno rollout restart deployment/gobmp-nats
```

## Teardown

```bash
# Remove syd only (leaves gobmp-nats and NATS in place)
kubectl delete -k deploy/k8s/

# Remove gobmp-nats (leaves existing Jalapeno gobmp untouched)
kubectl delete -f deploy/k8s/gobmp-nats.yaml
```
