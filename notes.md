1. On the k8s node — clone and deploy NATS first


git clone git@github.com:jalapeno/scoville.git
cd scoville

kubectl apply -f deploy/k8s/nats.yaml

# Wait for it to be ready
kubectl -n jalapeno rollout status deployment/nats

```
nats-server: /etc/nats/nats-server.conf:15:3: "$G" is a Reserved Account
```

# Quick sanity check — JetStream should show up
kubectl -n jalapeno port-forward svc/nats 8222:8222 &
curl -s http://localhost:8222/jsz | python3 -m json.tool | grep -E "config|memory"
kill %1
2. Redeploy GoBMP with NATS config


kubectl apply -f deploy/k8s/gobmp-collector.yaml

kubectl -n jalapeno rollout status deployment/gobmp
kubectl -n jalapeno logs -f deployment/gobmp
In the logs you should see GoBMP connecting to NATS and publishing on gobmp.parsed.* subjects once your BMP sources are pointed at it.

3. Build and deploy scoville

You'll need to build the image on the node (or on your Mac and load it):


# On the k8s node, from the repo root:
docker build -t scoville:latest .

# For k3s:
docker save scoville:latest | sudo k3s ctr images import -

# For kind:
kind load docker-image scoville:latest
Then:


# Update the NATS URL in the configmap to point at your jalapeno namespace NATS
# It should be: nats://nats.jalapeno:4222
# (the default in configmap.yaml is already set to that)

kubectl apply -k deploy/k8s/
kubectl -n scoville rollout status deployment/scoville
kubectl -n scoville logs -f deployment/scoville
You should see:


level=INFO msg="bmp collector configured" nats_url=nats://nats.jalapeno:4222
level=INFO msg="scoville starting" addr=:8080 bmp=true encap_mode=host
Once the containerlab BMP streams are flowing, the topology will start populating and you can hit curl http://<node-ip>:30080/topology from your laptop.
