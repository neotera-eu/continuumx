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

package httpsse

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
)

const (
	// BasePath is the root of the I-4 HTTP API. GET on it returns the capabilities.
	BasePath = "/i4/v1alpha1"
	// WatchPath is the subscription endpoint. It streams Server-Sent Events.
	WatchPath = BasePath + "/watch"
	// NodesPath lists nodes; NodesPath/{name} gets one. Opt-in.
	NodesPath = BasePath + "/nodes"
	// DevicesPath lists devices; DevicesPath/{namespace}/{name} gets one. Opt-in.
	DevicesPath = BasePath + "/devices"

	// ParamPrefix selects the keys of a subscription. Empty selects every key.
	ParamPrefix = "prefix"
	// ParamSince is the revision a resuming subscriber holds the state of.
	ParamSince = "since"
	// ParamEpoch is the epoch of ParamSince.
	ParamEpoch = "epoch"
	// HeaderLastEventID is the standard SSE resume header. It carries an event ID.
	HeaderLastEventID = "Last-Event-ID"

	// ParamCategory selects the nodes of one category when listing nodes.
	ParamCategory = "category"
	// ParamNamespace selects the devices of one namespace when listing devices.
	ParamNamespace = "namespace"
	// ParamNode selects the devices hosted by one node when listing devices.
	ParamNode = "node"
	// ParamProperty selects device properties when getting a device. It can be repeated.
	ParamProperty = "property"
)

// Server-Sent Event names of the watch stream.
//
// A stream starts in one of two ways. Fresh: RESET, one UPSERT per current resource (sorted
// by key), SYNCED. Resumed (ParamSince and ParamEpoch match the publisher): the UPSERT and
// DELETE events since then (in revision order), SYNCED. After SYNCED the subscriber holds the
// complete state of its prefix; live UPSERT and DELETE events follow, with HEARTBEAT events
// on a quiet stream. A subscriber that falls too far behind gets ERROR and the stream ends;
// it reconnects with the ID of the last event it received.
const (
	// EventReset tells the subscriber to drop what it holds for the prefix. Data: Cursor.
	EventReset = "RESET"
	// EventUpsert carries a resource that was added or changed. Data: model.Resource.
	EventUpsert = "UPSERT"
	// EventDelete carries the key of a removed resource. Data: Deletion.
	EventDelete = "DELETE"
	// EventSynced marks the end of the initial state. Data: Cursor.
	EventSynced = "SYNCED"
	// EventHeartbeat is a keep-alive on a quiet stream. Data: Cursor.
	EventHeartbeat = "HEARTBEAT"
	// EventError ends the stream. Data: StreamError.
	EventError = "ERROR"
)

// ReasonOverflow is the StreamError reason of a subscriber that fell too far behind.
const ReasonOverflow = "overflow"

// ErrorBody reasons.
const (
	// ReasonBadRequest: a parameter is invalid.
	ReasonBadRequest = "BadRequest"
	// ReasonNotFound: the resource or a requested property does not exist.
	ReasonNotFound = "NotFound"
	// ReasonQueryDisabled: list and get are not enabled on this publisher.
	ReasonQueryDisabled = "QueryDisabled"
	// ReasonInternal: the publisher failed to answer.
	ReasonInternal = "Internal"
)

// ErrorBody is the JSON body of every error response.
type ErrorBody struct {
	// Reason is a machine-readable reason, for example ReasonNotFound.
	Reason string `json:"reason"`
	// Error is a human-readable description.
	Error string `json:"error"`
}

// List is the answer of a list operation: the matching resources, sorted by key, at Cursor.
// A subscriber can continue with a watch from Cursor.
type List struct {
	Cursor
	// Items are the matching resources.
	Items []model.Resource `json:"items"`
}

// Cursor is a position in the publisher's change sequence.
type Cursor struct {
	// Epoch identifies the publisher's store.
	Epoch string `json:"epoch"`
	// Revision is the last revision the subscriber has received the effect of.
	Revision uint64 `json:"revision"`
}

// Deletion is the data of a DELETE event.
type Deletion struct {
	// Key is the key of the removed resource.
	Key string `json:"key"`
	// Revision is the revision of the delete.
	Revision uint64 `json:"revision"`
}

// StreamError is the data of an ERROR event.
type StreamError struct {
	// Reason is a machine-readable reason, for example ReasonOverflow.
	Reason string `json:"reason"`
	// Message is a human-readable description.
	Message string `json:"message"`
	// Cursor is where to resume.
	Cursor
}

// EventID returns the SSE event ID of a cursor: "<epoch>:<revision>". Only events after which
// a subscriber holds a consistent state carry an ID: SYNCED, live UPSERT and DELETE, HEARTBEAT.
func EventID(c Cursor) string {
	return c.Epoch + ":" + strconv.FormatUint(c.Revision, 10)
}

// ParseEventID parses an ID made by EventID.
func ParseEventID(id string) (Cursor, error) {
	i := strings.LastIndex(id, ":")
	if i <= 0 {
		return Cursor{}, fmt.Errorf("event ID %q is not <epoch>:<revision>", id)
	}
	revision, err := strconv.ParseUint(id[i+1:], 10, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("event ID %q: revision: %w", id, err)
	}
	return Cursor{Epoch: id[:i], Revision: revision}, nil
}
