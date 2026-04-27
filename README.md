# ContinuumX

[![Go Report Card](https://goreportcard.com/badge/github.com/neotera-eu/continuumx)](https://goreportcard.com/report/github.com/neotera-eu/continuumx)
[![LICENSE](https://img.shields.io/github/license/neotera-eu/continuumx.svg?style=flat-square)](/LICENSE)
[![Releases](https://img.shields.io/github/release/neotera-eu/continuumx/all.svg?style=flat-square)](https://github.com/neotera-eu/continuumx/releases)

> **ContinuumX** is a branded distribution of [KubeEdge](https://github.com/kubeedge/kubeedge), maintained by [Neotera](https://neotera.eu).
> It tracks KubeEdge upstream and layers Neotera-specific integrations on top.
> See [docs/upstream-sync.md](./docs/upstream-sync.md) for the upstream sync runbook.

ContinuumX extends Kubernetes with native edge computing capabilities — bringing containerised application orchestration and device management to edge nodes.
It consists of a cloud component and an edge component, providing core infrastructure support for networking, application deployment, and metadata synchronisation between cloud and edge.
It also supports **MQTT**, enabling edge devices to communicate through edge nodes.

## Advantages

- **Kubernetes-native support**: Manage edge applications and edge devices from the cloud using fully compatible Kubernetes APIs.
- **Cloud-Edge Reliable Collaboration**: Reliable message delivery over unstable cloud-edge networks.
- **Edge Autonomy**: Edge nodes run autonomously even when the cloud-edge network is unstable or the edge is offline.
- **Edge Device Management**: Manage edge devices through Kubernetes native APIs implemented by CRD.
- **Extremely Lightweight Edge Agent**: Minimal footprint EdgeCore for resource-constrained edge hardware.

## How It Works

ContinuumX consists of a cloud part and an edge part.

### Architecture

<div align="center">
<img src="./docs/images/kubeedge_arch.png" width="85%" align="center">
</div>

### In the Cloud
- **CloudHub**: WebSocket server responsible for watching changes at the cloud side, caching and sending messages to EdgeHub.
- **EdgeController**: Extended Kubernetes controller managing edge nodes and pod metadata.
- **DeviceController**: Extended Kubernetes controller managing device metadata and status sync between edge and cloud.

### On the Edge
- **EdgeHub**: WebSocket client interacting with Cloud Service, syncing resource updates and reporting edge status.
- **Edged**: Agent running on edge nodes, managing containerised applications.
- **EventBus**: MQTT client offering publish/subscribe capabilities to other components.
- **ServiceBus**: HTTP client providing REST capabilities to reach HTTP servers running at edge.
- **DeviceTwin**: Stores device status and syncs it to the cloud.
- **MetaManager**: Message processor between Edged and EdgeHub; stores/retrieves metadata in a lightweight SQLite database.

## Kubernetes Compatibility

|                           | Kubernetes 1.27 | Kubernetes 1.28 | Kubernetes 1.29 | Kubernetes 1.30 | Kubernetes 1.31 | Kubernetes 1.32 |
|---------------------------|-----------------|-----------------|-----------------|-----------------|-----------------|-----------------|
| ContinuumX 1.23 (KE 1.23) | +               | +               | +               | ✓               | ✓               | ✓               |

Key:
* `✓` ContinuumX and the Kubernetes version are exactly compatible.
* `+` ContinuumX has features or API objects that may not be present in the Kubernetes version.
* `-` The Kubernetes version has features or API objects that ContinuumX can't use.

## Getting Started

```bash
# Install the cloud component
cxadm init --advertise-address=<CLOUD_IP>

# Join an edge node
cxadm join --cloudcore-ipport=<CLOUD_IP>:10000 --edgenode-name=<NODE_NAME>
```

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for details on submitting patches, the contribution workflow, and how this project tracks KubeEdge upstream.

## Security

Report suspected vulnerabilities to the Neotera security team.
See [SECURITY.md](.github/SECURITY.md) for details.

## Upstream

ContinuumX tracks the KubeEdge `main` branch. See [docs/upstream-sync.md](./docs/upstream-sync.md) for the sync runbook.

## License

ContinuumX is under the Apache 2.0 license. See the [LICENSE](LICENSE) file for details.
