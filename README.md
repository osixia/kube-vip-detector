# kube-network-detector

Detect VIP routing and peer TCP connectivity, and maintain Kubernetes node labels.


The application runs as a DaemonSet on the node network (`hostNetwork: true`).
It supports IPv4 and IPv6 for both VIPs and peers. Modes are selected automatically:

- Configure `--vips` to detect which node receives traffic for each VIP.
- Configure `--peers` to test TCP connectivity from each node to named endpoints.
- Configure both lists to run both detections; at least one target is required.

VIP detection requires a shared HMAC key and uses per-VIP leader election.
Peer detection needs no cooperating detector on the target and runs on every node.

## VIP routing

Detect which Kubernetes node receives traffic for an IPv4 or IPv6 virtual IP (VIP) and
maintain one node label per VIP. IPv4 uses `kube-network-detector/<IPv4>=true`;
IPv6 uses an encoded suffix described in Configuration. The detector observes
routing; it does not assign VIPs to interfaces or move them between providers.

With VIPs configured, the application starts one authenticated responder per node. It can also be
embedded in another Go application through the public [`detector`](detector/)
package.

### How VIP detection works

Every instance receives the same VIP list. With `hostNetwork: true`, it checks
node interface addresses using `net.InterfaceAddrs()`:

- A VIP configured locally makes the node ineligible to observe that VIP.
- Other nodes participate in a Kubernetes Lease election for that VIP.
- The leader probes the VIP and authenticates the responding node.
- Instances with configured VIPs respond to authenticated probes.

Local addresses are checked at the configured interval, before each probe and
after its result. If a VIP becomes local or interface inspection fails, the
instance cancels its worker and waits for it to stop. Its Lease stops renewing;
another eligible instance can take over after expiration. A node becomes a
candidate again when the VIP is remote and its interfaces can be inspected.

By default, two consecutive successes identifying the same node confirm a
winner. The detector removes the VIP label from other nodes before adding it to
the winner. If removal fails, it does not add the new label. Three consecutive
probe failures remove the label from all nodes. Other labels are preserved.

An API list failure resets confirmation counters without concluding that the
VIP failed. Patches include `resourceVersion`; conflicts are retried with a fresh
node list and probe in a later cycle.

The application has no provider-specific logic or required provider labels.
Check your routes, WireGuard configuration, NAT and proxies: a VIP absent from
local interfaces does not prove that traffic follows its public inbound route.
Do not put a Service, NodePort, ingress or load balancer in front of the probe
port, since that can change which node answers.

### VIP probe protocol

The server listens on all node addresses and accepts HTTP `POST /v1/probe`.
Requests are limited to 2048 bytes and use this JSON structure:

```json
{"timestamp":1234567890,"nonce":"64 hex characters","target":"203.0.113.10:9876","mac":"64 hex characters"}
```

HMAC-SHA256 authenticates compact JSON arrays of strings:

```text
Request: ["v1/request","POST","/v1/probe",timestamp_decimal,nonce,target]
Response: ["v1/response",timestamp_decimal,nonce,target,node_name]
```

The response contains only `{"mac":"..."}`. The observer identifies the node by
comparing signatures computed for node names listed from Kubernetes. The key
and node name are not sent in plaintext over the probe port.

Each challenge has a random 32-byte nonce and a timestamp accepted within
±30 seconds. Responders remember up to 10,000 nonces until their authentication
window expires. Keep node clocks synchronized. Restarting a responder clears
its cache, so a still-valid request can be replayed after restart; its response
cannot authenticate a fresh challenge.

MAC comparisons use constant-time comparison. Probes reject redirects, ignore
environment HTTP proxies and open a new TCP connection on each attempt.
HMAC authenticates messages but does not encrypt traffic or establish the
network path against an active relay. Any instance with the shared key can sign
for any node. Restrict access to the probe port at the firewall where possible.

## Peer connectivity

In addition to VIP routing detection, each DaemonSet instance can test TCP connectivity
from its own node to named IPv4 or IPv6 endpoints. Peers run independently of VIP
leader election and local VIP eligibility; multiple nodes can reach the same peer.

```sh
kube-network-detector \
  --disable-crds \
  --peers='database=10.0.0.20:5432,api=[2001:db8::1]:443' \
  --peer-label-prefix=network.example.net/peer-
```

Set `KUBE_NETWORK_DETECTOR_NODE_NAME` with the Downward API, as in the example
manifest. VIP detection and CRD discovery additionally require `KUBE_NETWORK_DETECTOR_POD_NAMESPACE`
and `KUBE_NETWORK_DETECTOR_POD_UID`.
Each peer requires a unique name. The name and prefix together must form a valid
Kubernetes label key, distinct from configured VIP label keys.

After `--success-threshold` consecutive TCP successes (default 2), the instance sets
`network.example.net/peer-database=true` on its own node. After `--failure-threshold`
consecutive failures (default 3), it removes that label. `--interval` (default 5s),
`--timeout` (default 2s), and `--dry-run` apply to both VIP and peer probes.
API errors retry on later cycles and do not count as connectivity failures.
A successful TCP connection checks reachability, not application health.

Peer flags also accept `KUBE_NETWORK_DETECTOR_` environment variables,
including `KUBE_NETWORK_DETECTOR_PEERS`.

Use `hostNetwork: true` to measure connectivity from the node network.
Peer detection requires `get/patch` on Nodes and no Lease permissions.
A ready-to-use peers-only example is provided in
[`docs/examples/kubernetes/peers-only.yaml`](docs/examples/kubernetes/peers-only.yaml).
It omits the VIP responder, HMAC secret, TCP health probes and Lease RBAC.

Labels remain on shutdown and are reconciled by the next running instance.
When removing a peer or changing its name/prefix, remove its old labels explicitly;
the detector only manages configured labels. Use different names for separate
endpoints. Avoid multiple deployments writing the same label on a node.

## Kubernetes deployment

The combined VIP and peer [manifest](docs/examples/kubernetes/kube-network-detector.yaml) includes
the namespace, service account, RBAC and DaemonSet. It targets Linux nodes,
including control-plane nodes with standard `NoSchedule` taints. Add tolerations
for any custom taints on nodes that must run a responder.

For a peer-only deployment, use
[`peers-only.yaml`](docs/examples/kubernetes/peers-only.yaml); it requires no HMAC
Secret or probe server port. The steps below describe the combined deployment.

Before deploying:

- Install both CRDs from `docs/crds/`, or add `--disable-crds` for static-only operation.
- Replace the example VIPs and peers in the DaemonSet's `args`.
- Remove `--vips` or `--peers` if only one detection mode is needed.
- Check that TCP port 9876 is free on each node and reachable through the VIPs
  from the nodes performing probes.
- Allow `hostNetwork` under your cluster's admission rules.

Create a shared HMAC key, then apply the manifest:

```bash
kubectl create namespace kube-network-detector
kubectl -n kube-network-detector create secret generic kube-network-detector-auth \
  --from-literal=hmac-key="$(openssl rand -hex 32)"
kubectl apply -f docs/crds/
kubectl wait --for=condition=Established --timeout=60s \
  crd/vips.network.osixia.net crd/peers.network.osixia.net
kubectl apply -f docs/examples/kubernetes/kube-network-detector.yaml
kubectl -n kube-network-detector rollout status daemonset/kube-network-detector
kubectl -n kube-network-detector logs -l app.kubernetes.io/name=kube-network-detector --prefix --tail=100
kubectl get nodes -L kube-network-detector/203.0.113.10
```

Create the namespace and Secret once; reuse the same key for all responders and
observers. The key contains 64 hexadecimal characters representing 32 random
bytes. Kubernetes supplies it through `secretKeyRef`; the application does not
read Secrets through the API or load a key file.

The manifest uses a non-root user, drops all capabilities and makes the root
filesystem read-only. Reading interface addresses normally requires no added
capabilities. TCP readiness and liveness probes check that the server accepts
connections; they do not verify VIP routing or successful label updates.

## Configuration

The CLI runs inside Kubernetes and uses in-cluster credentials. The DaemonSet
supplies identity variables through the Downward API. The node name is always
required; namespace and pod UID are required for VIP detection or default CRD discovery:

| Variable | Source | Required for |
| --- | --- | --- |
| `KUBE_NETWORK_DETECTOR_NODE_NAME` | `spec.nodeName` | VIPs and peers |
| `KUBE_NETWORK_DETECTOR_POD_NAMESPACE` | `metadata.namespace` | VIPs or CRD discovery |
| `KUBE_NETWORK_DETECTOR_POD_UID` | `metadata.uid` | VIPs or CRD discovery |

| Option | Default | Purpose |
| --- | --- | --- |
| `--vips` | Empty | Comma-separated canonical unicast IPv4/IPv6 VIPs; repeatable |
| `--key` | Empty | Hex-encoded 32-byte HMAC key; required for VIPs |
| `--vip-label-prefix` | `kube-network-detector/` | Literal prefix for each VIP label |
| `--port` | `9876` | VIP responder TCP port on each node |
| `--peers` | Empty | Named IPv4/IPv6 TCP endpoints (`name=IP:port`); repeatable |
| `--peer-label-prefix` | `kube-network-detector/peer-` | Literal prefix for each peer name |
| `--disable-crds` | `false` | Disable default VIP and Peer CRD discovery; use only static targets |
| `--interval` | `5s` | Delay between probes and interval for local-address checks |
| `--timeout` | `2s` | Timeout for each probe |
| `--success-threshold` | `2` | Consecutive VIP successes identifying the same node, or peer TCP successes |
| `--failure-threshold` | `3` | Consecutive probe failures before clearing labels |
| `--dry-run` | `false` | Observe and log proposals without Kubernetes writes |

Run `kube-network-detector --help` for all flags. `--version` prints the embedded
image reference, such as `osixia/kube-network-detector`
(`osixia/kube-network-detector:develop` in an unversioned local build).

Omit `--vips`, use `--vips=`, or set an empty `KUBE_NETWORK_DETECTOR_VIPS` for
peer-only operation when peers are configured and `--disable-crds` is set. The key
and VIP pod identity are required when VIPs are configured or CRD discovery is enabled.
Duplicates, whitespace, noncanonical addresses, unspecified, loopback, multicast,
link-local, zoned IPv6 and IPv4-mapped IPv6 VIPs are rejected. The VIP responder
port must be between 1024 and 65535; peer ports must be between 1 and 65535.
Durations must be positive and confirmation thresholds must be at least 1.

The label prefix is literal: `--vip-label-prefix=network.example.net/vip-`
produces `network.example.net/vip-203.0.113.10=true`. No separator is added.
IPv4 labels keep their existing form. IPv6 labels use `ipv6-` followed by the
32 hexadecimal digits of the address, because Kubernetes label keys cannot contain
colons. For `2001:db8::1`, the default label is
`kube-network-detector/ipv6-20010db8000000000000000000000001=true`.
Both address families can be configured together, for example
`--vips=203.0.113.10,2001:db8::1`. IPv6 addresses in `--vips` have no brackets;
peer endpoints include brackets, for example `--peers=api=[2001:db8::1]:443`.
The node and its network must have IPv6 connectivity; the responder listens on the
wildcard TCP address using Go's platform dual-stack support.
Label syntax is validated at startup; see the
[Kubernetes label syntax](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set).
Use consistent VIP lists, keys, ports and label prefixes across instances.

### Environment variables

Detector options accept environment variables with the `KUBE_NETWORK_DETECTOR_`
prefix. Replace hyphens with underscores: `--success-threshold` becomes
`KUBE_NETWORK_DETECTOR_SUCCESS_THRESHOLD`.

Explicit flags take precedence over environment variables, which take
precedence over defaults. For example, inside a pod with the required identity
and credentials:

```bash
KUBE_NETWORK_DETECTOR_VIPS=203.0.113.10,2001:db8::1 \
KUBE_NETWORK_DETECTOR_PEERS=database=10.0.0.20:5432 \
KUBE_NETWORK_DETECTOR_INTERVAL=5s \
KUBE_NETWORK_DETECTOR_DRY_RUN=true \
kube-network-detector
```

Supply the key with `KUBE_NETWORK_DETECTOR_KEY` or `--key`. Flags and environment variables are read at
startup; CRD targets are watched dynamically when enabled. The CLI's environment
prefix and default label prefix are defined in [config/config.go](config/config.go).

### Targets declared in Kubernetes

The CLI discovers `network.osixia.net/v1alpha1` resources across all namespaces
by default. Use `--disable-crds` (no value needed), or
`KUBE_NETWORK_DETECTOR_DISABLE_CRDS=true`, to use only static targets without
CRDs or CRD API permissions. `--disable-crds=false` explicitly keeps discovery
enabled and overrides the environment variable.
CRD targets are added to `--vips` and `--peers`, not substituted for them.
An initially empty target list is allowed in CRD mode.

Install both namespaced CRDs and grant the detector read permissions:

```bash
kubectl apply -f docs/crds/
kubectl wait --for=condition=Established --timeout=60s \
  crd/vips.network.osixia.net crd/peers.network.osixia.net
```

Then apply the combined DaemonSet manifest, which includes CRD reader RBAC.
Custom deployments can use `docs/examples/kubernetes/crd-reader-rbac.yaml` to
grant the existing detector ServiceAccount these permissions.
Use the combined deployment even when only Peer objects initially exist: CRD mode
keeps the authenticated VIP responder running so future VIP objects work without
another rollout. It requires the HMAC key, responder port, pod namespace and UID,
Node `get/list/patch`, Lease `get/create/update`, and CRD `list/watch` permissions.
All instances must use the same CRD mode, key, port and label prefixes.
The supplied CRD schemas use the Kubernetes CEL IP library (Kubernetes 1.30+).

Applications can declare targets in their own namespaces:

```yaml
apiVersion: network.osixia.net/v1alpha1
kind: VIP
metadata:
  name: public-web
  namespace: traefik
spec:
  address: 203.0.113.10
---
apiVersion: network.osixia.net/v1alpha1
kind: Peer
metadata:
  name: database
  namespace: nextcloud
spec:
  address: 10.0.0.20
  port: 5432
```

Namespaces must already exist. Examples are in
[`targets.yaml`](docs/examples/kubernetes/targets.yaml). To delegate target
management without access to Nodes, bind the example
[`writer Role`](docs/examples/kubernetes/crd-writer-role.yaml) in each allowed
namespace. Readers are cluster-wide; writers can remain namespace-scoped.

VIP objects use the existing IP-based labels and share one worker and Lease per
IP, including duplicates declared by flags. Removing one declaration does not
stop detection while another still exists. Peer objects use the label
`<peer-label-prefix>crd.<namespace>.<name>`, for example
`kube-network-detector/peer-crd.nextcloud.database=true`. Long identities use
`crd.` plus the first 32 hex digits of their SHA-256 hash. The final label key is
validated; excessively long custom prefixes can cause targets to be rejected.
Static peer names and labels are unchanged. If a CRD generates a label already
used for a different target, the static target takes precedence and the conflicting
CRD is ignored with a warning. Do not deliberately reuse CRD-generated names in flags.

Adds, changes and deletions reconcile without restarting the DaemonSet. Changed
workers are canceled and joined before their labels are cleared and replacement
workers start with fresh confirmation counters. Invalid objects are ignored with
a warning and do not terminate other targets. API list/watch failures retain the
last observed configuration; they never count as deletion. Initial discovery
fails clearly if either CRD is missing or cannot be listed.

Each instance cleans obsolete CRD labels on its own Node, retries API errors and
conflicts, and preserves unrelated labels and annotations. The Node annotation
`kube-network-detector/crd-labels` records CRD label ownership, allowing cleanup
after an instance restarts following an offline deletion. Labels still used by
flags are protected. Shutdown retains labels; cleanup requires an instance
running on the affected Node with CRD mode enabled. Switching back to flag-only
mode does not clean CRD labels automatically. Multiple detector deployments
must not share the same Nodes and inventory annotation.

Global probe settings still come from flags/environment variables; these resources
have no per-target settings or status subresource. The HMAC key stays in the
existing Secret. In dry-run mode neither the inventory nor labels or Leases are
written.

### Logging

Logging uses the pinned `container-baseimage/log` dependency. Set the level and
format with CLI flags or the logger's own environment variables:

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--log-level` | `CONTAINER_LOG_LEVEL` | `info` |
| `--log-format` | `CONTAINER_LOG_FORMAT` | `console` |

Explicit logging flags override the logger's environment settings.
`KUBE_NETWORK_DETECTOR_LOG_LEVEL` and `KUBE_NETWORK_DETECTOR_LOG_FORMAT` do not configure
the logger. `--quiet` limits application logs to errors.

Levels are `error`, `warning`, `info`, `debug` and `trace`. Formats are `console`
and `json`. Application JSON logs contain `datetime`, `level` and `message`;
VIP, node and action details appear in the message. Application errors go to
stderr and other levels to stdout. Client-go uses its own logger for election
messages. The application does not log the HMAC key.

## Dry-run mode

Set `--dry-run=true` in the manifest or `KUBE_NETWORK_DETECTOR_DRY_RUN=true` in the
pod environment. For VIPs, each eligible node probes directly without acquiring
a Lease, and the responder remains active. For peers, every node continues TCP
probing its configured endpoints. No Node or Lease is written.

Proposed label changes are logged after the same confirmation thresholds as in
normal mode. A proposal can recur because actual labels remain unchanged.
Dry-run applies per instance: other pods running in normal mode can still write
to Kubernetes during a rollout.

A deployment used exclusively for dry-run needs `list` on Nodes for VIPs and
`get` on Nodes for peers. The example manifests retain the permissions required
for normal operation.

## Updating configuration

Change the DaemonSet's arguments and apply the manifest to update VIPs, peers or other
options. A pod template change triggers a rollout. Updating a Secret used as an
environment variable requires restarting the pods; see
[Kubernetes Secrets](https://kubernetes.io/docs/concepts/configuration/secret/):

```bash
kubectl -n kube-network-detector rollout restart daemonset/kube-network-detector
kubectl -n kube-network-detector rollout status daemonset/kube-network-detector
```

Only one key is supported at a time. During key rotation, mismatched keys can
cause probe failures and label removal.

For flag-only targets, removing a VIP or peer, renaming a peer, or changing a label prefix does not
clean up existing labels.
After all instances use the new configuration, remove obsolete labels yourself:

```bash
kubectl label nodes --all kube-network-detector/203.0.113.10-
```

Use the old configured prefix for cleanup. Old Lease objects are retained; their
leadership expires when no instance renews them.

## Migration from kube-vip-detector

The binary, image, Go module, environment prefix, example manifest and default label
prefix now use `kube-network-detector`. Update deployments and selectors accordingly.
Existing labels are not migrated automatically. To retain existing VIP selectors,
set `--vip-label-prefix=kube-vip-detector/` explicitly; the old environment prefix
is no longer read. The repository directory remains `kube-vip-detector` locally.

## Go library

Import `github.com/osixia/kube-network-detector/detector`. Supply your own Kubernetes
client, identity and options; the library does not read environment variables,
create the client or register signal handlers. This fragment assumes `client`,
`ctx` and `sharedKey` are supplied by the calling application:

```go
service, err := detector.New(client, detector.Identity{
    Node:      "node-1",
    Namespace: "networking",
    PodUID:    "unique-instance-id",
}, detector.Options{
    VIPs:             []string{"203.0.113.10"},
    Key:              sharedKey, // 64 hexadecimal characters
    VIPLabelPrefix:   "kube-network-detector/",
    Port:             9876,
    Peers:            []string{"database=10.0.0.20:5432"},
    PeerLabelPrefix:  "kube-network-detector/peer-",
    Interval:         5 * time.Second,
    Timeout:          2 * time.Second,
    SuccessThreshold: 2,
    FailureThreshold: 3,
})
if err != nil {
    return err
}
return service.Run(ctx)
```

For CRD discovery, set `Options.WatchCRDs = true` and use
`detector.NewWithDynamicClient(client, dynamicClient, identity, options)` with a
`dynamic.Interface` from `k8s.io/client-go/dynamic`. The dynamic client's timeout
must allow long-lived watches. The existing `New` API stays unchanged for static
targets and rejects CRD mode without a dynamic client.

Import `time` alongside `detector`. `New` validates options and copies the VIP
and peer lists without applying CLI defaults. Use a unique `PodUID` per VIP instance and
cancel `ctx` to stop the server and workers. Run in the node network namespace
when inspecting node addresses and serving probes, as the DaemonSet does with
`hostNetwork`. A compiling usage example is in
[detector/example_test.go](detector/example_test.go).

## Build and release

Both modules require Go 1.26.9 or newer. The root module uses client-go v0.35.0.
The Dockerfile defaults to `golang:1.26.9`; GitHub Actions also selects Go 1.26.9.

Run from the repository root:

```bash
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o kube-network-detector .
docker build --build-arg IMAGE=osixia/kube-network-detector \
  -t osixia/kube-network-detector .
```

The image runs from `scratch` as UID/GID 65532. The separate [build module](build/README.md)
uses Dagger to build Linux amd64 and arm64 images. Its container smoke test
checks that `--version` returns the exact image reference supplied through the
Dockerfile's `IMAGE` build argument. The Dockerfile splits that reference into
`config.ImageName` and `config.ImageTag`; there is no separate application
version to keep synchronized.

The GitHub workflow configures linting and vulnerability checks for both Go
modules, then container build/testing. Version tags such as `1.0.0` publish Docker Hub images and a GitHub release, sign image digests with
cosign, and attach SPDX SBOM attestations. Set repository secrets
`REGISTRY_USERNAME` and `REGISTRY_PASSWORD` before a tag release; use tags
without a leading `v`. See [build/README.md](build/README.md) for details.

## Permissions and limitations

VIP detection uses `list/patch` on Nodes and `get/create/update` on Leases in
the detector namespace. Peer detection uses `get/patch` on Nodes and no Leases. No Secret or ConfigMap API permissions are required.
Node `patch` permission covers entire Nodes; the code only changes labels for
configured VIPs and peers, plus the CRD inventory annotation when enabled.

Lease duration is 30 seconds, renewal deadline 20 seconds and retry interval
5 seconds. When leadership ends, the Lease expires naturally rather than being
released immediately. Handover can take around 30 seconds plus election and
confirmation time. Leases do not provide strict fencing during pauses or
network partitions.

Labels have no TTL. If every node has the VIP locally, no observer is eligible;
existing labels remain. The same applies when all observers or the API are
unavailable. A detector's network failure can remove a label even if the VIP is
reachable from another observation point. For peers, a connectivity failure
removes only the affected node's peer label after the failure threshold.

Address inspection and network operations are not atomic, and label updates
across nodes are not a transaction. Changes do not wait for application pods to
stop. Validate routing and failover in your own cluster before relying on these
labels for scheduling.

Tests cover option validation, authentication, replay protection, confirmation
thresholds, label reconciliation, eligibility transitions, automatic mode selection,
IPv4/IPv6 probes and dry-run behavior.
They use a fake Kubernetes client and local HTTP servers; they do not establish
real provider routing, cluster admission or failover guarantees. Run them with
`go test -race ./...`.
