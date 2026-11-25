/*
Copyright 2026 Neotera.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package dtpublisher is the Device Twins Resources Publisher (UP2DATE4SDV CID 93).
//
// It publishes the resource context of the vehicle-to-cloud continuum, Kubernetes nodes and
// device twins, to authorised consumers over Interface I-4. It only reads and publishes; it
// never selects targets, schedules, rolls out or offloads workloads.
//
// Layout:
//   - model:     the published resource envelope, schema version and key scheme.
//   - store:     the current published state, with revisions, an epoch, no-op suppression,
//     fan-out to subscribers and resume from a known revision.
//   - source:    producers of resources (nodes, device twins) and a per-key throttle.
//   - transport: the boundary to the I-4 protocol (open decision OPN-04), with the
//     PROVISIONAL httpsse adapter and the reserved, unimplemented zenoh name.
//
// Consumer access and security of I-4 are open decisions; they are kept behind separate
// packages added in later increments.
package dtpublisher
