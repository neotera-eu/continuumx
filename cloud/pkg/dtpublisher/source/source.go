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

// Package source defines the producers of published resources.
//
// The resource model is an open decision (brief section 5, item 3). Each kind of resource
// context is a Source; adding a metric source later means adding a Source, not changing the
// store or the transport.
package source

import (
	"context"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
)

// Sink receives the resources a Source produces. *store.Store implements it.
type Sink interface {
	// Upsert adds or replaces a resource and reports whether the published state changed.
	Upsert(r model.Resource) (bool, error)
	// Delete removes a resource and reports whether one was removed.
	Delete(key string) bool
}

// Source produces resources into a Sink.
type Source interface {
	// Name identifies the source, for example "nodes".
	Name() string
	// Run feeds sink until ctx is done. It returns an error if the source cannot start.
	Run(ctx context.Context, sink Sink) error
}
