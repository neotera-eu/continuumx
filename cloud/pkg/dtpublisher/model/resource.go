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

// Package model defines what the Device Twins Resources Publisher publishes over I-4:
// the resource envelope, the schema version and the key scheme.
//
// The I-4 contract is a first draft (plan 2026-11-24-cid093-dt-publisher, section 5).
// Changes within SchemaVersion must be additive.
package model

import (
	"errors"
	"fmt"
	"time"
)

// SchemaVersion identifies the version of the published payload schema.
const SchemaVersion = "i4/v1alpha1"

// Kind is the kind of a published resource.
type Kind string

const (
	// KindNode is a Kubernetes node: cloud control plane, cloud worker or edge worker.
	KindNode Kind = "Node"
)

// Resource is the envelope of every published resource.
//
// Sources fill Kind, Key, Source and Spec. The store sets SchemaVersion, Revision and
// ObservedAt when it accepts a change.
type Resource struct {
	// SchemaVersion is the payload schema version, see SchemaVersion.
	SchemaVersion string `json:"schemaVersion"`
	// Kind is the resource kind.
	Kind Kind `json:"kind"`
	// Key addresses the resource, for example "nodes/edge-worker/u2deb-edge".
	Key string `json:"key"`
	// Revision increases with every accepted change in a publisher process.
	Revision uint64 `json:"revision"`
	// ObservedAt is when the publisher accepted this version of the resource.
	ObservedAt time.Time `json:"observedAt"`
	// Source references the Kubernetes object version the resource was built from.
	Source SourceRef `json:"source"`
	// Spec is the kind-specific payload, for example *NodeSpec. Treat it as read-only.
	Spec interface{} `json:"spec"`
}

// SourceRef references the object a resource was built from.
type SourceRef struct {
	// ResourceVersion is the Kubernetes resourceVersion of the source object.
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

// Validate checks the fields a source must set.
func (r *Resource) Validate() error {
	if r.Kind == "" {
		return errors.New("resource kind is empty")
	}
	if err := ValidateKey(r.Key); err != nil {
		return err
	}
	if r.Spec == nil {
		return fmt.Errorf("resource %q has no spec", r.Key)
	}
	return nil
}
