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

// Package transport is the boundary between the published state and the protocol that
// carries it to consumers over Interface I-4.
//
// The I-4 transport is an open decision (brief section 5, item 1; OPN-04). Every protocol is
// an adapter in its own package that turns the store into a Publisher. The core packages
// (model, store, source) never import a transport; the binary picks one by name from a
// Registry. The adapters are:
//   - httpsse: HTTP/JSON with Server-Sent Events. PROVISIONAL stand-in.
//   - zenoh: registered name only, not implemented; to be discussed with ZSC and DLR.
package transport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
)

var (
	// ErrNotImplemented is returned by the factory of a transport that is named but not built.
	ErrNotImplemented = errors.New("transport not implemented")
	// ErrUnknownTransport is returned for a transport name that is not registered.
	ErrUnknownTransport = errors.New("unknown transport")
)

// Publisher carries the published state to consumers.
type Publisher interface {
	// Name is the transport name, for example "httpsse".
	Name() string
	// Run publishes until ctx is done. Cancelling ctx is a normal stop and returns nil.
	Run(ctx context.Context) error
}

// Deps is what every transport gets from the core.
type Deps struct {
	// Store is the published state.
	Store *store.Store
	// Info describes what is published, for the capabilities operation.
	Info Info
}

// Factory creates a Publisher. Transport-specific options are bound when the factory is made.
type Factory func(deps Deps) (Publisher, error)

// Registry maps transport names to factories.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register adds a factory under name. A name can be registered once.
func (r *Registry) Register(name string, f Factory) error {
	if name == "" || f == nil {
		return errors.New("transport name and factory must be set")
	}
	if _, ok := r.factories[name]; ok {
		return fmt.Errorf("transport %q is already registered", name)
	}
	r.factories[name] = f
	return nil
}

// Names returns the registered names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// New creates the Publisher of the transport name.
func (r *Registry) New(name string, deps Deps) (Publisher, error) {
	f, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("%w %q, registered: %v", ErrUnknownTransport, name, r.Names())
	}
	if deps.Store == nil {
		return nil, errors.New("transport needs a store")
	}
	return f(deps)
}

// Info is the static part of the capabilities: what this publisher instance publishes.
// The binary fills it from its configuration.
type Info struct {
	// Kinds lists the published kinds.
	Kinds []KindInfo
	// MinPublishInterval is the minimum interval between two published changes of one key,
	// zero if changes are not throttled.
	MinPublishInterval time.Duration
}

// KindInfo describes one published kind.
type KindInfo struct {
	// Kind is the resource kind.
	Kind model.Kind `json:"kind"`
	// Prefix is the key prefix of every resource of the kind, for example "nodes/".
	Prefix string `json:"prefix"`
	// Categories lists the second key segment values, for kinds that have categories.
	Categories []string `json:"categories,omitempty"`
	// AnnotationKeys lists the annotation keys that are published, none by default.
	AnnotationKeys []string `json:"annotationKeys,omitempty"`
}

// Operations tells which I-4 operations a publisher serves.
type Operations struct {
	// Watch is the subscription to the standing data flow. Always served.
	Watch bool `json:"watch"`
	// List is the opt-in request for the current resources under a prefix.
	List bool `json:"list"`
	// Get is the opt-in request for one resource.
	Get bool `json:"get"`
}

// Capabilities is the answer of the capabilities operation. It is the same for every
// transport, so a consumer can learn what is published before it subscribes.
type Capabilities struct {
	// SchemaVersion is the payload schema version, see model.SchemaVersion.
	SchemaVersion string `json:"schemaVersion"`
	// Transport is the transport name.
	Transport string `json:"transport"`
	// Epoch identifies the publisher's store. Revisions of different epochs are not comparable.
	Epoch string `json:"epoch"`
	// Revision is the current store revision.
	Revision uint64 `json:"revision"`
	// Operations lists the served operations.
	Operations Operations `json:"operations"`
	// Kinds lists the published kinds.
	Kinds []KindInfo `json:"kinds"`
	// MinPublishInterval is Info.MinPublishInterval as a Go duration string, for example "1s".
	// Empty when changes are not throttled.
	MinPublishInterval string `json:"minPublishInterval,omitempty"`
}

// NewCapabilities builds the capabilities of a transport from the current store state.
func NewCapabilities(transportName string, st *store.Store, info Info, ops Operations) Capabilities {
	kinds := info.Kinds
	if kinds == nil {
		kinds = []KindInfo{}
	}
	c := Capabilities{
		SchemaVersion: model.SchemaVersion,
		Transport:     transportName,
		Epoch:         st.Epoch(),
		Revision:      st.Revision(),
		Operations:    ops,
		Kinds:         kinds,
	}
	if info.MinPublishInterval > 0 {
		c.MinPublishInterval = info.MinPublishInterval.String()
	}
	return c
}
