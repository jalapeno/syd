# syd Kubernetes Deployment

Deploys the full syd stack — NATS, gobmp-nats, and syd — into a single `syd`
namespace. The existing Jalapeno stack (gobmp → Kafka) is left untouched.

## Architecture

```
containerlab (XRd / FRR)
  ├─ BMP/TCP ──→ port 30511 ──→ gobmp (jalapeno ns, existing)
  │                                └─ Kafka ──→ igp-graph / arango consumers
  │
  └─ BMP/TCP ──→ port 30512 ──→ gobmp-nats (syd ns)
                                   └─ NATS JetStream ──→ syd  ←── scheduler / UI
                                        (all in syd namespace)
```

Routers send BMP to **both** ports. The syd namespace is fully self-contained —
no cross-namespace dependencies on the Jalapeno stack.

---

## Prerequisites

- Existing Jalapeno stack running (gobmp on port 30511 → Kafka)
- k8s node with internet access (pulls `sbezverk/gobmp:latest` and `nats:2-alpine`)
- `syd:latest` image built and loaded (see step 1 below)

---

## 1. Build and load the syd image

```bash
# From the repo root:
docker build -t syd:latest .

# For k3s:
docker save syd:latest | sudo k3s ctr images import -
```

---

## 2. Deploy everything

One command deploys NATS, gobmp-nats, and syd together:

```bash
kubectl apply -k deploy/k8s/
```

Watch the rollout:

```bash
kubectl -n syd get pods -w
```

Expected pods:

```
NAME                          READY   STATUS    RESTARTS   AGE
nats-xxx                      1/1     Running   0          30s
gobmp-nats-xxx                1/1     Running   0          30s
syd-xxx                       1/1     Running   0          30s
```

---

## 3. Configure routers to send BMP to port 30512

Point your XRd / FRR routers at the k8s node IP on port **30512** (in addition
to port 30511 for the existing Jalapeno gobmp).

---

## 4. Verify

Check syd is connected and receiving BMP data:

```bash
kubectl -n syd logs -f deployment/syd
```

Expected startup:

```
level=INFO msg="bmp collector configured" nats_url=nats://nats:4222
level=INFO msg="syd starting" addr=:8080 bmp=true encap_mode=host
level=INFO msg="bmp collector started" handlers=6
```

Once routers are sending BMP, topology messages will appear:

```
level=INFO msg="topology updated" topology_id=underlay-v6 ...
```

Check NATS stream and consumer state:

```bash
kubectl -n syd port-forward svc/nats 8222:8222 &
curl -s "http://localhost:8222/jsz?streams=1&consumers=1" | python3 -m json.tool
kill %1
```

---

## 5. Access the API

**NodePort (from your laptop or containerlab VM):**

```bash
kubectl get nodes -o wide   # find node IP
curl http://<node-ip>:30080/topology
```

**Port-forward:**

```bash
kubectl -n syd port-forward deployment/syd 8080:8080
curl http://localhost:8080/topology
```

---

## Updating

```bash
# After rebuilding syd image:
kubectl -n syd rollout restart deployment/syd

# After pulling a new gobmp image:
kubectl -n syd rollout restart deployment/gobmp-nats
```

## Teardown

```bash
kubectl delete -k deploy/k8s/
```
