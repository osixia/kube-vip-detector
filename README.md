# kube-vip-detector

Detect which Kubernetes node receives traffic for an IPv4 virtual IP (VIP) and
maintain a `kube-vip-detector/<IPv4>=true` node label. The detector observes
routing; it does not assign VIPs to interfaces or move them between providers.

The application runs as a DaemonSet with one responder per node. It can also be
embedded in another Go application through the public [`detector`](detector/)
package.

## Kubernetes deployment

The example [manifest](docs/examples/kubernetes/kube-vip-detector.yaml) includes
the namespace, service account, RBAC and DaemonSet. It targets Linux nodes,
including control-plane nodes with standard `NoSchedule` taints. Add tolerations
for any custom taints on nodes that must run a responder.

Before deploying:

- Replace the example VIPs in the DaemonSet's `args`.
- Check that TCP port 9876 is free on each node and reachable through the VIPs
  from the nodes performing probes.
- Allow `hostNetwork` under your cluster's admission rules.

Create a shared HMAC key, then apply the manifest:

```bash
kubectl create namespace kube-vip-detector
kubectl -n kube-vip-detector create secret generic kube-vip-detector-auth \
  --from-literal=hmac-key="$(openssl rand -hex 32)"
kubectl apply -f docs/examples/kubernetes/kube-vip-detector.yaml
kubectl -n kube-vip-detector rollout status daemonset/kube-vip-detector
kubectl -n kube-vip-detector logs -l app.kubernetes.io/name=kube-vip-detector --prefix --tail=100
kubectl get nodes -L kube-vip-detector/203.0.113.10
```

Create the namespace and Secret once; reuse the same key for all responders and
observers. The key contains 64 hexadecimal characters representing 32 random
bytes. Kubernetes supplies it through `secretKeyRef`; the application does not
read Secrets through the API or load a key file.

The manifest uses a non-root user, drops all capabilities and makes the root
filesystem read-only. Reading interface addresses normally requires no added
capabilities. TCP readiness and liveness probes check that the server accepts
connections; they do not verify VIP routing or successful label updates.

## How detection works

Every instance receives the same VIP list. With `hostNetwork: true`, it checks
node interface addresses using `net.InterfaceAddrs()`:

- A VIP configured locally makes the node ineligible to observe that VIP.
- Other nodes participate in a Kubernetes Lease election for that VIP.
- The leader probes the VIP and authenticates the responding node.
- All instances respond to probes, including those with no VIPs to observe.

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

## Configuration

The CLI runs inside Kubernetes and uses in-cluster credentials. The DaemonSet
supplies these required identity variables through the Downward API:

| Variable | Source |
| --- | --- |
| `KUBE_VIP_DETECTOR_NODE_NAME` | `spec.nodeName` |
| `KUBE_VIP_DETECTOR_POD_NAMESPACE` | `metadata.namespace` |
| `KUBE_VIP_DETECTOR_POD_UID` | `metadata.uid` |

| Option | Default | Purpose |
| --- | --- | --- |
| `--vips` | Empty | Comma-separated canonical unicast IPv4 VIPs; repeatable |
| `--key` | Required | Hex-encoded 32-byte HMAC key |
| `--vip-label-prefix` | `kube-vip-detector/` | Literal prefix for each VIP label |
| `--port` | `9876` | TCP probe port on each node |
| `--interval` | `5s` | Delay between probes and interval for local-address checks |
| `--timeout` | `2s` | Timeout for each probe |
| `--success-threshold` | `2` | Consecutive successes identifying the same node |
| `--failure-threshold` | `3` | Consecutive probe failures before clearing labels |
| `--dry-run` | `false` | Observe and log proposals without Kubernetes writes |

Run `kube-vip-detector --help` for all flags. `--version` prints the embedded
image reference, such as `osixia/kube-vip-detector`
(`osixia/kube-vip-detector:develop` in an unversioned local build).

Omit `--vips`, use `--vips=`, or set an empty `KUBE_VIP_DETECTOR_VIPS` for
responder-only operation. The key and pod identity are still required.
Duplicates, whitespace, IPv6, unspecified, loopback and multicast VIPs are
rejected. Ports must be between 1024 and 65535; durations must be positive and
confirmation thresholds must be at least 1.

The label prefix is literal: `--vip-label-prefix=network.example.net/vip-`
produces `network.example.net/vip-203.0.113.10=true`. No separator is added.
Label syntax is validated at startup; see the
[Kubernetes label syntax](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set).
Use consistent VIP lists, keys, ports and label prefixes across instances.

### Environment variables

Detector options accept environment variables with the `KUBE_VIP_DETECTOR_`
prefix. Replace hyphens with underscores: `--success-threshold` becomes
`KUBE_VIP_DETECTOR_SUCCESS_THRESHOLD`.

Explicit flags take precedence over environment variables, which take
precedence over defaults. For example, inside a pod with the required identity
and credentials:

```bash
KUBE_VIP_DETECTOR_VIPS=203.0.113.10,198.51.100.20 \
KUBE_VIP_DETECTOR_INTERVAL=5s \
KUBE_VIP_DETECTOR_DRY_RUN=true \
kube-vip-detector
```

Supply the key with `KUBE_VIP_DETECTOR_KEY` or `--key`. Configuration is read at
startup; there is no configuration file or live reload. The CLI's environment
prefix and default label prefix are defined in [config/config.go](config/config.go).

### Logging

Logging uses the pinned `container-baseimage/log` dependency. Set the level and
format with CLI flags or the logger's own environment variables:

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--log-level` | `CONTAINER_LOG_LEVEL` | `info` |
| `--log-format` | `CONTAINER_LOG_FORMAT` | `console` |

Explicit logging flags override the logger's environment settings.
`KUBE_VIP_DETECTOR_LOG_LEVEL` and `KUBE_VIP_DETECTOR_LOG_FORMAT` do not configure
the logger. `--quiet` limits application logs to errors.

Levels are `error`, `warning`, `info`, `debug` and `trace`. Formats are `console`
and `json`. Application JSON logs contain `datetime`, `level` and `message`;
VIP, node and action details appear in the message. Application errors go to
stderr and other levels to stdout. Client-go uses its own logger for election
messages. The application does not log the HMAC key.

## Dry-run mode

Set `--dry-run=true` in the manifest or `KUBE_VIP_DETECTOR_DRY_RUN=true` in the
pod environment. Each eligible node then probes directly without acquiring a
Lease. No Node or Lease is written, and the responder remains active.

Proposed label changes are logged after the same confirmation thresholds as in
normal mode. A proposal can recur because actual labels remain unchanged.
Dry-run applies per instance: other pods running in normal mode can still write
to Kubernetes during a rollout.

A deployment used exclusively for dry-run only needs `list` on Nodes. The
example manifest retains the permissions required for normal operation.

## Updating configuration

Change the DaemonSet's arguments and apply the manifest to update VIPs or other
options. A pod template change triggers a rollout. Updating a Secret used as an
environment variable requires restarting the pods; see
[Kubernetes Secrets](https://kubernetes.io/docs/concepts/configuration/secret/):

```bash
kubectl -n kube-vip-detector rollout restart daemonset/kube-vip-detector
kubectl -n kube-vip-detector rollout status daemonset/kube-vip-detector
```

Only one key is supported at a time. During key rotation, mismatched keys can
cause probe failures and label removal.

Removing a VIP or changing the label prefix does not clean up existing labels.
After all instances use the new configuration, remove obsolete labels yourself:

```bash
kubectl label nodes --all kube-vip-detector/203.0.113.10-
```

Use the old configured prefix for cleanup. Old Lease objects are retained; their
leadership expires when no instance renews them.

## Probe protocol

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

## Go library

Import `github.com/osixia/kube-vip-detector/detector`. Supply your own Kubernetes
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
    VIPLabelPrefix:   "kube-vip-detector/",
    Port:             9876,
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

Import `time` alongside `detector`. `New` validates options and copies the VIP
list without applying CLI defaults. Use a unique `PodUID` per instance and
cancel `ctx` to stop the server and workers. Run in the node network namespace
when inspecting node addresses and serving probes, as the DaemonSet does with
`hostNetwork`. A compiling usage example is in
[detector/example_test.go](detector/example_test.go).

## Build and release

The root module declares Go 1.25 and uses client-go v0.35.0. The Dockerfile
defaults to `golang:1.25`; GitHub Actions selects Go 1.26.1.

Run from the repository root:

```bash
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o kube-vip-detector .
docker build --build-arg IMAGE=osixia/kube-vip-detector \
  -t osixia/kube-vip-detector .
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

Normal operation uses `list/patch` on Nodes and `get/create/update` on Leases in
the detector namespace. No Secret or ConfigMap API permissions are required.
Node `patch` permission covers entire Nodes; the code only changes labels for
the configured prefix and VIPs.

Lease duration is 30 seconds, renewal deadline 20 seconds and retry interval
5 seconds. When leadership ends, the Lease expires naturally rather than being
released immediately. Handover can take around 30 seconds plus election and
confirmation time. Leases do not provide strict fencing during pauses or
network partitions.

Labels have no TTL. If every node has the VIP locally, no observer is eligible;
existing labels remain. The same applies when all observers or the API are
unavailable. A detector's network failure can remove a label even if the VIP is
reachable from another observation point.

Address inspection and network operations are not atomic, and label updates
across nodes are not a transaction. Changes do not wait for application pods to
stop. Validate routing and failover in your own cluster before relying on these
labels for scheduling.

Tests cover option validation, authentication, replay protection, confirmation
thresholds, label reconciliation, eligibility transitions and dry-run behavior.
They use a fake Kubernetes client and local HTTP servers; they do not establish
real provider routing, cluster admission or failover guarantees. Run them with
`go test -race ./...`.
