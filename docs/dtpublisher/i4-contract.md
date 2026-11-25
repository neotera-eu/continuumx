# Interface I-4: resource-context publication (draft `i4/v1alpha1`)

Status: **first draft**, as implemented by the Device Twins Resources Publisher (CID 93) in this
repository. Not yet agreed with consumers. Interface I-4 is "ContinuumX → digital-twin data
consumers: digital-twin data publication / subscription" (UP2DATE4SDV D3.3). NEO owns its
contract, which D3.3 [Final] (M30) has to contain: topics, schema, subscription and access
model. This page is the starting point for that.

The page has two layers:

- **Data model and semantics** (sections 1 to 4): keys, envelope, payloads, revisions,
  subscription behaviour, access model. These are meant to hold for any transport.
- **HTTP/SSE binding** (section 5): how the current, **PROVISIONAL** transport `httpsse` carries
  them. The I-4 transport is an open decision (Zenoh is expected by ClOrch, to be discussed with
  ZSC and DLR); a Zenoh binding is not defined here and nothing in this page assumes one.

The publisher only publishes. Which consumer uses the data for what, including any scheduling,
placement or offloading decision, is outside I-4 and outside ContinuumX.

## 1. Resources and keys

Two kinds of resources are published:

| Kind | Key | What it is |
|---|---|---|
| `Node` | `nodes/<category>/<node>` | A Kubernetes node: cloud control plane, cloud worker, or edge worker (a Central ECU running edgecore). |
| `Device` | `devices/<namespace>/<name>` | A device twin of a device without its own network connection, reached through a mapper on an edge node. |

Node categories:

| Category | Rule |
|---|---|
| `edge-worker` | The node is a ContinuumX edge node (label `node-role.kubernetes.io/edge`), even if it also has the control-plane label. |
| `cloud-control-plane` | Label `node-role.kubernetes.io/control-plane`. |
| `cloud-worker` | Any other node. |

Keys are `/`-separated segments with no empty segment and none of `* ? # $`. A consumer selects
what it receives by **key prefix**, for example `nodes/`, `nodes/edge-worker/`, `devices/` or
`devices/default/`. The empty prefix selects everything. A key does not change while the
resource exists; a node whose category changes is a delete under the old key and a new
resource under the new one.

## 2. Envelope

Every published resource has the same envelope:

| Field | Type | Meaning |
|---|---|---|
| `schemaVersion` | string | `i4/v1alpha1`. |
| `kind` | string | `Node` or `Device`. |
| `key` | string | See section 1. |
| `revision` | integer | Increases with every accepted change in one publisher process; see section 4. |
| `observedAt` | string (RFC 3339) | When the publisher accepted this version. |
| `source.resourceVersion` | string | Kubernetes `resourceVersion` of the object the resource was built from; for a device with a status, `<Device resourceVersion>,<DeviceStatus resourceVersion>`. For tracing only. |
| `spec` | object | Kind-specific payload (sections 3.1, 3.2). |

## 3. Payloads

### 3.1 Node

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Node name, for example `u2deb-edge`. |
| `category` | string | Section 1. |
| `labels` | map | All node labels. |
| `annotations` | map | Only the annotation keys the operator allowlisted; none by default. |
| `addresses` | list of `{type, address}` | Node addresses. |
| `capacity` | map | Total resources, Kubernetes quantities (for example `cpu: "4"`, `memory: "8029960Ki"`). |
| `allocatable` | map | Resources available to pods, Kubernetes quantities. |
| `conditions` | list of `{type, status, reason, message, lastTransitionTime}` | Node conditions, without per-condition heartbeat times. |
| `ready` | string | Status of the `Ready` condition: `True`, `False` or `Unknown`. `Unknown` when the node stopped reporting, for example an edge node that lost its connection. |
| `lastHeartbeatTime` | string | Last heartbeat of the `Ready` condition. The only heartbeat field, so a node heartbeat changes only this field (and still produces an update). |
| `nodeInfo` | object | `machineID`, `systemUUID`, `bootID`, `kernelVersion`, `osImage`, `containerRuntimeVersion`, `kubeletVersion`, `kubeProxyVersion`, `operatingSystem`, `architecture`. |
| `runtimeHandlers` | list | Container runtime handlers, with features. |

Pod-level usage ("allocatable minus requested") and live usage metrics are **not** published in
`v1alpha1`.

### 3.2 Device

A device twin joins the `Device`, its `DeviceModel` and its `DeviceStatus`
(`devices.kubeedge.io/v1beta1`). It is published as soon as the `Device` exists; model and status
fields appear when those objects exist.

| Field | Type | Meaning |
|---|---|---|
| `namespace`, `name` | string | The Device. |
| `model` | string | Name of the DeviceModel. |
| `protocol` | string | Protocol of the Device, or of its model if the Device sets none. |
| `node` | string | The edge node hosting the device, for example the Central ECU `u2deb-edge`. Equals `spec.name` of an `edge-worker` node. |
| `labels` | map | All Device labels. |
| `annotations` | map | Allowlisted annotation keys only; none by default. |
| `state` | string | Device state reported by the mapper; empty if never reported. |
| `lastOnlineTime` | string | When the device was last online, as reported by the mapper. |
| `properties` | list | One entry per property: those of the model in model order, then any property the Device declares that the model does not. |

Each property:

| Field | Type | Source | Meaning |
|---|---|---|---|
| `name` | string | model | Property name, for example `frontLeftPressure`. |
| `description`, `type`, `accessMode`, `unit`, `minimum`, `maximum` | string | model | As declared in the DeviceModel. `type` tells how to read the values (for example `DOUBLE`, `BOOLEAN`). |
| `desired` | `{value, metadata}` | Device | The value requested on the cloud side, if any. |
| `observedDesired` | `{value, metadata}` | DeviceStatus | The desired value as seen at the edge, if any. |
| `reported` | `{value, metadata}` | DeviceStatus | The value reported by the device; `metadata` carries `timestamp` and `type`. |

Values are strings, as ContinuumX stores them; there is no unit conversion and no standard
signal naming. Only properties the `Device` declares reach the cloud (the Device Controller
drops other twins), so only those can be published.

### 3.3 Example (illustrative, shortened)

```json
{
  "schemaVersion": "i4/v1alpha1",
  "kind": "Device",
  "key": "devices/default/automotive-tire-pressure-ecu-fw-001",
  "revision": 1842,
  "observedAt": "2026-11-25T10:21:07.412Z",
  "source": {"resourceVersion": "48211,48390"},
  "spec": {
    "namespace": "default",
    "name": "automotive-tire-pressure-ecu-fw-001",
    "model": "automotive-tire-pressure-ecu-fw-demo",
    "protocol": "virtual-ethernet",
    "node": "u2deb-edge",
    "properties": [
      {
        "name": "frontLeftPressure", "type": "FLOAT", "accessMode": "ReadOnly", "unit": "kPa",
        "reported": {"value": "231.5", "metadata": {"timestamp": "1764066067000", "type": "float"}}
      },
      {
        "name": "pressureThreshold", "type": "FLOAT", "accessMode": "ReadWrite", "unit": "kPa",
        "minimum": "150", "maximum": "300",
        "desired": {"value": "210.0"},
        "observedDesired": {"value": "210.0"},
        "reported": {"value": "210.0", "metadata": {"timestamp": "1764066067000", "type": "float"}}
      }
    ]
  }
}
```

## 4. Subscription semantics

Subscription is the default operation of I-4: a **standing data flow**, not a request/response.

- **Initial state.** A new subscription first receives the complete current state for its
  prefix: a reset marker, one resource per key in key order, then a *synced* marker carrying a
  cursor. After *synced* the consumer holds the complete state of its prefix.
- **Changes.** Afterwards it receives an upsert (the full new envelope) whenever a published
  field of a resource changes, and a delete (`key`, `revision`) when a resource disappears.
  Updates that change nothing published are suppressed. There is no periodic re-publication.
- **Rate.** Telemetry can change every 10 to 20 s per device, and every node status heartbeat
  updates its node's `lastHeartbeatTime`. The operator can set a minimum
  interval per key (`minPublishInterval`, default 0: every change). A change arriving earlier is
  held back and replaced by later ones, so the **latest value is always delivered** when the
  interval ends. Deletes are never delayed.
- **Revisions and epoch.** `revision` grows by one per accepted change within one publisher
  process and orders all events. The **epoch** is a random identifier of that process; revisions
  restart after a publisher restart, and the epoch tells a consumer that this happened. A
  **cursor** is `(epoch, revision)`.
- **Resume.** A consumer that reconnects with its last cursor receives only what changed since
  then: the current value of each changed resource and a delete for each removed key, then
  *synced*. If the publisher cannot resume from that cursor (other epoch, cursor older than the
  last 1024 deletes, or ahead of the publisher) the consumer gets a fresh initial state,
  starting with the reset marker, and must drop what it held for the prefix.
- **Keep-alive.** A quiet subscription receives a heartbeat with the current cursor (default
  every 15 s).
- **Slow consumers.** The publisher never waits for a consumer. A consumer more than 256 events
  behind receives an error with `reason: overflow` and a cursor, and the subscription ends; it
  resumes from that cursor.
- **Consistency.** Resources are published as ContinuumX holds them in the Kubernetes API (fed by
  the Edge Controller and Device Controller from the edge). If cloudcore or the edge is down, the
  publisher keeps serving the last known state, and nodes that stop reporting go `ready:
  Unknown`.

Optional request/response operations, **off by default** and enabled by the operator:

- **List** nodes (optionally one category) or devices (optionally one namespace and/or hosting
  node). The answer carries the cursor of the state it shows, so a subscription can continue
  from it.
- **Get** one node by name or one device by namespace and name, optionally only selected
  properties. A requested property that does not exist is "not found", not an empty answer.

**Capabilities** (always on): schema version, transport, current cursor, which operations are
served, the kinds with their key prefixes, node categories and published annotation keys, and
the minimum publish interval.

## 5. HTTP/SSE binding (`httpsse`, PROVISIONAL)

Base path `/i4/v1alpha1`. JSON over HTTPS; the subscription is
[Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html).

| Operation | Request | Answer |
|---|---|---|
| Capabilities | `GET /i4/v1alpha1` | `200` JSON, see below |
| Subscribe | `GET /i4/v1alpha1/watch?prefix=<p>[&since=<rev>&epoch=<epoch>]`, or header `Last-Event-ID: <epoch>:<rev>` | `200` `text/event-stream` |
| List nodes (opt-in) | `GET /i4/v1alpha1/nodes[?category=<c>]` | `200` `{"epoch", "revision", "items": [...]}` |
| List devices (opt-in) | `GET /i4/v1alpha1/devices[?namespace=<ns>][&node=<node>]` | `200` `{"epoch", "revision", "items": [...]}` |
| Get node (opt-in) | `GET /i4/v1alpha1/nodes/<name>` | `200` resource |
| Get device (opt-in) | `GET /i4/v1alpha1/devices/<ns>/<name>[?property=<p>&property=...]` | `200` resource |

Capabilities example:

```json
{
  "schemaVersion": "i4/v1alpha1",
  "transport": "httpsse",
  "epoch": "9f2c41d07a5be318",
  "revision": 1842,
  "operations": {"watch": true, "list": false, "get": false},
  "kinds": [
    {"kind": "Node", "prefix": "nodes/", "categories": ["cloud-control-plane", "cloud-worker", "edge-worker"]},
    {"kind": "Device", "prefix": "devices/"}
  ]
}
```

Subscription events (`event:` field), each with a JSON `data:` line:

| Event | Data | Event ID |
|---|---|---|
| `RESET` | cursor `{"epoch", "revision"}` | none |
| `UPSERT` | resource envelope | `<epoch>:<revision>` for live changes; none during the initial state |
| `DELETE` | `{"key", "revision"}` | `<epoch>:<revision>` |
| `SYNCED` | cursor | `<epoch>:<revision>` |
| `HEARTBEAT` | cursor | `<epoch>:<revision>` |
| `ERROR` | `{"reason", "message", "epoch", "revision"}`; ends the stream | none |

Event IDs are set only where the consumer holds a consistent state, so a standard SSE client
that reconnects with `Last-Event-ID` resumes correctly.

Errors are JSON `{"reason": "...", "error": "..."}`:

| HTTP | `reason` | When |
|---|---|---|
| 400 | `BadRequest` | Invalid parameter, for example a prefix with a wildcard. |
| 401 | `Unauthenticated` | A consumer list is configured and no verified client certificate was presented. |
| 403 | `Forbidden` | The consumer is not in the consumer list, or may not read what it asked for. |
| 404 | `NotFound` | No such node, device or property. |
| 404 | `QueryDisabled` | List or get while the opt-in is off. |
| 500 | `Internal` | The publisher failed to answer. |

## 6. Access model (PROVISIONAL)

- **Transport security.** TLS (1.2 or later) is required; plain HTTP exists only behind an
  explicit development flag. Client certificates can be off, optional or required. The
  consumer identity is the common name of its verified client certificate.
- **Authorization.** Without a consumer list, every consumer the TLS layer accepts may read
  everything. With a list, each named consumer may read the key prefixes granted to it; each
  grant ends with `/`, so the finest grant is a node category or a device namespace. A request
  broader than the grants (for example `devices/` for a consumer granted `devices/default/`) is
  refused with 403, not silently narrowed. A get searches only what the consumer may read, so
  it does not reveal resources outside its grants.

The mechanism required by REQ-ORP-0040 (secure handshake, mutual authentication) is to be
defined with WP4 (Cloud SASE Library, CID 30); this model is a stand-in until then.

## 7. Versioning

The version appears in the path and in `schemaVersion`. Within a version only additive changes
are made (new optional fields, new kinds, new categories); consumers must ignore fields they do
not know. A breaking change means a new version, served alongside the old one for a transition
period.

## 8. Open points

| Point | Status |
|---|---|
| Transport | `httpsse` is a stand-in. ClOrch expects Zenoh; which side adapts, and how keys and events map to Zenoh key expressions, is to be discussed with ZSC and DLR. |
| Consumers | ClOrch is the primary candidate; a cloud-side vECU may also consume. Which attributes, update rate and access each consumer needs is not agreed. |
| Resource model | REQ-ORP-0038 names CPU, memory, network and energy. `v1alpha1` publishes node capacity, allocatable and conditions, and the device twins; no usage, network-throughput or energy figures, and no standard signal names. Which component produces which attribute is not agreed. |
| Security | To be defined with WP4; section 6 is a stand-in. |
| Hardware configuration changes (REQ-ORP-0048) | Visible today as node label and `nodeInfo` changes and as device twins appearing, changing or disappearing; a dedicated representation is not defined. |
